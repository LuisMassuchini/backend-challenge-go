// Package outbox e o relay que publica os eventos gravados pela operacao financeira.
//
// Ele existe porque nenhum evento pode ser publicado dentro da transacao que o
// originou. A publicacao e efeito no mundo externo -- uma fila que outro processo
// le -- e um commit que ainda pode falhar depois da publicacao. Se a chamada ao
// broker estivesse dentro da transacao, um commit que falhasse deixaria o evento
// publicado para um mundo que nunca teve a operacao.
//
// Por isso o relay vive em outro processo logico, ou pelo menos em outro ciclo: ele
// le a outbox depois do commit, publica e so entao marca o registro como publicado.
// Se ele morrer entre publicar e confirmar, o registro volta e a republicacao
// carrega o mesmo eventId, que e o que permite ao consumidor reconhecer que e o
// mesmo evento.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/eventos"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// Publicador e o destino dos eventos.
//
// E interface, e nao o cliente SQS concreto, porque o relay decide tres coisas --
// reservar, publicar, confirmar -- e nenhuma delas e sobre SQS. Um relay que
// conhecesse a SDK teria o contrato do broker dentro da logica de publicacao, e o
// teste do relay passaria a exigir um broker para provar uma decisao que e do relay.
type Publicador interface {
	// Publicar envia o corpo do evento para a fila.
	//
	// O primeiro argumento e a chave de particao, que e o agregado do evento. E o
	// que define a ordem na fila de saida: eventos do mesmo agregado nao saem em
	// paralelo, e o consumidor ve a transacao processada antes do saldo alterado,
	// que e a ordem em que os fatos aconteceram.
	Publicar(ctx context.Context, chaveDeParticao, corpo string) error
}

// Relogio fornece o instante.
//
// Mesma razao do relogio nos casos de uso: um relay que chama time.Now direto nao
// tem como ser testado de forma deterministica, e o teste da reserva vencida
// depende de medir a janela de reserva contra um instante conhecido.
type Relogio interface {
	// Agora devolve o instante corrente em UTC.
	Agora() time.Time
}

// Dependencias e o que o relay precisa.
type Dependencias struct {
	// Unidade e a fronteira da transacao SQL.
	//
	// Entra aqui, e nao um pool, porque o relay precisa da mesma fronteira dos casos
	// de uso: a reserva tem de confirmar antes de publicar, e a confirmacao da
	// publicacao tem de ser um commit proprio.
	Unidade *pg.Unidade

	// Outbox e o repositorio da fila de publicacao.
	Outbox *pg.RepositorioOutbox

	// Publicador e o destino dos eventos.
	Publicador Publicador

	// Editor e o nome deste relay, gravado na reserva.
	//
	// O nome existe para que o operador saiba quem tem o registro. Sem ele, um relay
	// parado com trabalho reservado aparece como registro preso sem dono.
	Editor string

	// Relogio e a fonte do instante corrente.
	Relogio Relogio

	// Lote e quantos registros sao reservados por ciclo.
	Lote int

	// Ocioso e quanto tempo se espera entre ciclos sem registro.
	Ocioso time.Duration

	// Janela e por quanto tempo a reserva vale.
	Janela time.Duration

	// Backoff e a espera da primeira retentativa.
	Backoff time.Duration

	// MaximoTentativas e quantas vezes insistir antes de desistir.
	//
	// O valor e alto de proposito. Um evento que falhou duas vezes por causa de
	// broker fora quase sempre e problema temporario, e desistir cedo seria perder o
	// evento. O relay nunca apaga o registro: desistir aqui e parar de tentar, com o
	// motivo gravado para o operador ver.
	MaximoTentativas int
}

// Relay publica os eventos pendentes.
type Relay struct {
	unidade          *pg.Unidade
	outbox           *pg.RepositorioOutbox
	publicador       Publicador
	editor           string
	relogio          Relogio
	lote             int
	ocioso           time.Duration
	janela           time.Duration
	backoff          time.Duration
	maximoTentativas int
}

// valoresPadrao do relay.
//
// O ocioso e curto porque a outbox nao tem long polling: o relay consulta o banco e,
// quando nao ha nada pronto, espera. Um segundo e o piso que evita varrer a tabela a
// cada milissegundo sem ganho nenhum.
const (
	lotePadrao             = 50
	ociosoPadrao           = time.Second
	janelaPadrao           = 30 * time.Second
	backoffPadrao          = 2 * time.Second
	maximoTentativasPadrao = 12
)

// Erros do relay.
var (
	// ErrConfiguracao cobre dependencia ausente na montagem.
	ErrConfiguracao = errors.New("outbox: configuracao invalida")

	// ErrPublicacaoPermanente cobre evento que esgotou as tentativas.
	//
	// Differe do erro devolvido pelo publicador de proposito: um erro de rede e
	// transitorio e o registro volta para a fila, e uma recusa que o broker repete
	// doze vezes nao vai passar na decima terceira. O relay distingue os dois para nao
	// ficar em laco apertado contra um defeito que ja se provou permanente.
	ErrPublicacaoPermanente = errors.New("outbox: publicacao falhou definitivamente")
)

// Novo monta o relay.
func Novo(d Dependencias) (*Relay, error) {
	if d.Unidade == nil {
		return nil, fmt.Errorf("%w: unidade de trabalho ausente", ErrConfiguracao)
	}
	if d.Outbox == nil {
		return nil, fmt.Errorf("%w: repositorio da outbox ausente", ErrConfiguracao)
	}
	if d.Publicador == nil {
		return nil, fmt.Errorf("%w: publicador ausente", ErrConfiguracao)
	}
	if d.Editor == "" {
		return nil, fmt.Errorf("%w: editor sem nome", ErrConfiguracao)
	}

	lote := d.Lote
	if lote <= 0 {
		lote = lotePadrao
	}
	ocioso := d.Ocioso
	if ocioso <= 0 {
		ocioso = ociosoPadrao
	}
	janela := d.Janela
	if janela <= 0 {
		janela = janelaPadrao
	}
	backoff := d.Backoff
	if backoff <= 0 {
		backoff = backoffPadrao
	}
	tentativas := d.MaximoTentativas
	if tentativas <= 0 {
		tentativas = maximoTentativasPadrao
	}

	relogio := d.Relogio
	if relogio == nil {
		relogio = relogioDeSistema{}
	}

	return &Relay{
		unidade:          d.Unidade,
		outbox:           d.Outbox,
		publicador:       d.Publicador,
		editor:           d.Editor,
		relogio:          relogio,
		lote:             lote,
		ocioso:           ocioso,
		janela:           janela,
		backoff:          backoff,
		maximoTentativas: tentativas,
	}, nil
}

// relogioDeSistema usa o relogio do processo.
type relogioDeSistema struct{}

// Agora devolve o instante corrente em UTC.
func (relogioDeSistema) Agora() time.Time { return time.Now().UTC() }

// Rodar publica ate o contexto ser cancelado.
//
// O contexto e o unico sinal de parada, pelo mesmo motivo dos outros workers: duas
// fontes de verdade para a mesma decisao fariam o shutdown e o cancelamento
// divergirem sem que ninguem pudesse dizer qual dos dois aconteceu.
func (r *Relay) Rodar(ctx context.Context) error {
	slog.Info("relay da outbox no ar",
		"editor", r.editor,
		"lote", r.lote,
		"ocioso", r.ocioso.String(),
		"janela", r.janela.String(),
		"tentativas_maximas", r.maximoTentativas,
	)

	for {
		if ctx.Err() != nil {
			return nil
		}

		publicados, err := r.Ciclo(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Falha de infraestrutura no laco. Voltar a tentar imediatamente
			// transformaria o banco fora em laco apertado de log.
			slog.Error("falha no ciclo do relay", "erro", err.Error())
			if !dormir(ctx, r.ocioso) {
				return nil
			}
			continue
		}

		// Com trabalho feito o ciclo seguinte vem logo: ha registro esperando e cada
		// volta e uma chance de publicar. Sem trabalho, o relay espera.
		if publicados == 0 {
			if !dormir(ctx, r.ocioso) {
				return nil
			}
		}
	}
}

// Ciclo reserva, publica e confirma, e devolve quantos eventos foram publicados.
//
// A ordem e o que fecha a garantia de nao publicar antes do commit:
//
//  1. a reserva confirma em um commit, e a partir dai o registro tem dono;
//  2. a publicacao acontece fora de qualquer transacao financeira;
//  3. a confirmacao da publicacao e um commit proprio.
//
// Se o processo morrer entre 2 e 3, a reserva vence e outro relay republica com o
// mesmo eventId. Se ele morrer entre 1 e 2, o registro volta tambem. Nenhum dos dois
// caminhos perde o evento, e nenhum publica antes do commit que o originou.
func (r *Relay) Ciclo(ctx context.Context) (int, error) {
	registros, err := r.reservar(ctx)
	if err != nil {
		return 0, err
	}

	publicados := 0
	for _, registro := range registros {
		if ctx.Err() != nil {
			return publicados, nil
		}

		if err := r.tratar(ctx, registro); err != nil {
			return publicados, err
		}
		publicados++
	}
	return publicados, nil
}

// reservar confirma a reserva em uma unidade propria.
func (r *Relay) reservar(ctx context.Context) ([]pg.RegistroPendente, error) {
	var reservados []pg.RegistroPendente

	if err := r.unidade.Executar(ctx, func(q pg.Querente) error {
		var err error
		reservados, err = r.outbox.Reservar(ctx, q, r.editor, r.lote, r.janela, r.relogio.Agora())
		return err
	}); err != nil {
		// Conflito de reserva e corrida benigna entre publishers: o registro tem dono
		// e sera publicado por ele. Nao e erro do relay.
		if errors.Is(err, pg.ErrConflitoDeReserva) {
			return nil, nil
		}
		return nil, err
	}
	return reservados, nil
}

// tratar publica um registro e decide o que fazer depois.
//
// O erro devolvido aqui e o de infraestrutura que interrompe o ciclo, e nao o erro do
// publicador: falha de publicacao e tratada dentro do registro, com backoff, porque o
// destino da falha e o registro e nao o worker.
func (r *Relay) tratar(ctx context.Context, registro pg.RegistroPendente) error {
	evento := registro.Evento

	envelope, err := evento.Envelope()
	if err != nil {
		// Envelope que nao serializa e um defeito do registro, nao do broker: repetir
		// a publicacao republicaria exatamente o mesmo conteudo invalido. E falha
		// permanente, e o registro para de ser retomado com o motivo gravado.
		return r.desistir(ctx, evento.EventID(),
			fmt.Errorf("envelope do evento nao serializa: %w", err))
	}

	// A chave de particao e o agregado. E a decisao que o plano mandava fechar antes
	// de codar, e ela esta em ARCHITECTURE.md: eventos do mesmo agregado nao saem em
	// paralelo na fila de saida.
	if err := r.publicador.Publicar(ctx, evento.AggregateID().String(), string(envelope)); err != nil {
		return r.reprogramar(ctx, registro, err)
	}

	// A confirmacao e DEPOIS da publicacao. Confirmar antes seria o caminho para
	// perder o evento: o registro sairia da fila sem nunca ter chegado a ninguem.
	return r.confirmar(ctx, evento)
}

// confirmar marca o registro como publicado.
func (r *Relay) confirmar(ctx context.Context, evento eventos.Evento) error {
	return r.unidade.Executar(ctx, func(q pg.Querente) error {
		return r.outbox.ConfirmarPublicacao(ctx, q, evento.EventID(), r.relogio.Agora())
	})
}

// reprogramar devolve o registro para a fila com backoff, ou desiste.
func (r *Relay) reprogramar(ctx context.Context, registro pg.RegistroPendente, causa error) error {
	if registro.Tentativas+1 >= r.maximoTentativas {
		return r.desistir(ctx, registro.Evento.EventID(), causa)
	}

	proximaEm := r.relogio.Agora().Add(r.backoff * pow2(registro.Tentativas))

	slog.Warn("falha ao publicar evento, sera republicado",
		"evento", registro.Evento.EventID().String(),
		"tipo", string(registro.Evento.Tipo()),
		"agregado", registro.Evento.AggregateID().String(),
		"tentativas", registro.Tentativas+1,
		"proxima_em", proximaEm.Format(time.RFC3339),
		"erro", causa.Error(),
	)

	if err := r.unidade.Executar(ctx, func(q pg.Querente) error {
		return r.outbox.Reprogramar(ctx, q, registro.Evento.EventID(), proximaEm)
	}); err != nil {
		return err
	}
	return nil
}

// desistir marca a falha permanente e nao devolve o registro para a fila.
//
// O registro nao e apagado. Apagar evento seria perder a evidencia de que ele existiu
// e nao foi publicado, e o papel de runtime nem tem DELETE. O que fica e o motivo
// gravado, que e o que o operador precisa para decidir.
func (r *Relay) desistir(ctx context.Context, id wallet.Identificador, causa error) error {
	slog.Error("evento nao pode ser publicado, desistindo",
		"evento", id.String(),
		"erro", causa.Error(),
	)

	if err := r.unidade.Executar(ctx, func(q pg.Querente) error {
		return r.outbox.Desistir(ctx, q, id, causa.Error(), r.relogio.Agora())
	}); err != nil {
		return err
	}
	return nil
}

// pow2 devolve dois elevado a expoente.
//
// O crescimento do intervalo e por repeticao, e nao por multiplicacao do intervalo
// acumulado: `inicio * 2^tentativas` estoura o int64 em poucas tentativas, e o
// intervalo precisa ser uma funcao do numero de tentativas, nao uma progressao
// acumulada.
func pow2(expoente int) time.Duration {
	resultado := time.Duration(1)
	for i := 0; i < expoente; i++ {
		resultado *= 2
	}
	return resultado
}

// dormir espera o tempo indicado e devolve false quando o contexto terminou antes.
func dormir(ctx context.Context, duracao time.Duration) bool {
	caso := time.NewTimer(duracao)
	defer caso.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-caso.C:
		return true
	}
}
