// Package consumidor e o worker que le a fila de operacoes.
//
// O worker e grosso de proposito: ele cuida de receber, converter, chamar o caso de
// uso, apagar ou devolver, e nada mais. Nenhuma regra de negocio mora aqui. Se o
// worker precisar saber o que e uma reversao, o desenho esta errado.
package consumidor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/fingerprint"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
	"github.com/LuisMassuchini/backend-challenge-go/internal/sqs"
)

// nomeDoConsumidor e como este consumidor se registra na inbox.
//
// O nome importa porque e ele que fecha a entrega por consumidor. Se duas versoes do
// mesmo consumidor usassem nomes diferentes, cada uma teria sua propria linha e a
// segunda nao reconheceria a entrega da primeira.
const nomeDoConsumidor = "wager-service"

// tentativasDaFilaMorta e a partir de qual recebimento a mensagem vai para o cartao
// morto.
//
// O valor espelha o `MAX_RECEIVE` de `deploy/localstack/init/00-filas.sh`, e ele esta
// aqui duplicado de proposito, nao por esquecimento: o `receive count` da fila e a
// unica fonte que o consumidor tem, e o valor nao vem na mensagem. A duplicia e o
// preco de nao ter o parametro da fila na borda, e a alternativa seria expor a
// politica de redrive no codigo e no script -- dois lugares para mudar quando a
// politica mudar.
//
// O sintoma de a duplicia ficar errada e uma metrica de fila morta que conta na
// margem: nem zero quando deveria contar, nem o valor da fila quando nao devia.
const tentativasDaFilaMorta = 3

// MensagemOperacao e o corpo da mensagem da fila.
//
// Os nomes sao os mesmos do contrato HTTP. E uma escolha, e nao um acaso: a mesma
// operacao que entra por uma ponta tem que produzir o mesmo resumo de conteudo nas
// duas, e o resumo e calculado a partir destes campos.
type MensagemOperacao struct {
	// ProviderId e o provedor que envia.
	ProviderId string `json:"providerId"`

	// ExternalTransactionId e o identificador da operacao no provedor.
	ExternalTransactionId string `json:"externalTransactionId"`

	// PlayerId e o jogador dono da carteira.
	PlayerId string `json:"playerId"`

	// WalletId e a carteira alvo.
	WalletId string `json:"walletId"`

	// RoundId e a rodada de jogos.
	RoundId string `json:"roundId"`

	// GameId e o jogo.
	GameId string `json:"gameId"`

	// Kind e o tipo da operacao.
	Kind string `json:"kind"`

	// Money e o valor.
	Money struct {
		// Amount e o valor decimal.
		Amount string `json:"amount"`

		// Currency e o codigo da moeda.
		Currency string `json:"currency"`
	} `json:"money"`

	// ReferenceExternalTransactionId aponta a operacao que a reversao desfaz.
	ReferenceExternalTransactionId string `json:"referenceExternalTransactionId,omitempty"`

	// IdempotencyKey e a chave recebida do produtor.
	//
	// Vem no corpo e nao em um cabecalho porque a mensagem e um envelope opaco para
	// o SQS: um atributo de mensagem customizado seria possivel, mas o produtor
	// simples, sem a SDK, manda so o corpo, e exigir os dois tornaria o produtor
	// dependente da SDK.
	IdempotencyKey string `json:"idempotencyKey"`
}

// erroDeMensagem e uma mensagem que o consumidor nao consegue interpretar.
//
// E separado do erro de processamento porque os dois tem destinos diferentes: uma
// mensagem malformada nao melhora com repeticao e precisa sair da fila agora, e uma
// operacao que falhou por saldo pode ser reenviada com sucesso depois de um deposito.
type erroDeMensagem struct {
	// motivo explica o que nao prestou.
	motivo string
}

func (e *erroDeMensagem) Error() string {
	return "consumidor: mensagem invalida: " + e.motivo
}

// ErroDeMensagem diz se o erro e de mensagem malformada.
func ErroDeMensagem(err error) bool {
	var malformada *erroDeMensagem
	return errors.As(err, &malformada)
}

// Worker percorre a fila.
type Worker struct {
	// fila e o cliente da fila.
	fila *sqs.Cliente

	// servicos sao os casos de uso.
	servicos app.Servicos

	// lote e quantas mensagens sao lidas por chamada.
	lote int32

	// ocioso e quanto tempo se espera entre ciclos sem mensagem.
	//
	// A espera existe para que uma fila vazia nao vire um laco apertado. A espera
	// real e o long polling da propria fila; este piso cobre o caso em que a fila
	// respondeu vazio rapido, e evita transformar o consumidor em um queimador de
	// requisicao com a fila fora.
	ocioso time.Duration

	// renovaVisibilidade e de quanto em quanto tempo a visibilidade da mensagem em
	// processamento e renovada.
	renovaVisibilidade time.Duration

	// metricas conta retentativa e fila morta, quando ha registro.
	metricas *obs.Metricas

	// limiteDeVisibilidade e por quanto tempo a visibilidade e estendida quando
	// o processamento passa do intervalo de renovacao.
	limiteDeVisibilidade time.Duration
}

// Dependencias e o que o worker precisa.
type Dependencias struct {
	// Fila e o cliente da fila.
	Fila *sqs.Cliente

	// Servicos sao os casos de uso.
	Servicos app.Servicos

	// Lote e o tamanho do lote lido.
	Lote int32

	// Ocioso e a espera entre ciclos sem mensagem.
	Ocioso time.Duration

	// RenovaVisibilidade e o intervalo de renovacao da visibilidade.
	RenovaVisibilidade time.Duration

	// Metricas conta retentativa e fila morta.
	//
	// Nil desliga a contagem. E o que permite ao consumidor ser montado em teste sem
	// registro.
	Metricas *obs.Metricas
}

// Novo monta o worker.
func Novo(d Dependencias) *Worker {
	ocioso := d.Ocioso
	if ocioso <= 0 {
		ocioso = time.Second
	}
	renova := d.RenovaVisibilidade
	if renova <= 0 {
		renova = 20 * time.Second
	}

	return &Worker{
		fila:                 d.Fila,
		servicos:             d.Servicos,
		lote:                 d.Lote,
		ocioso:               ocioso,
		renovaVisibilidade:   renova,
		limiteDeVisibilidade: 60 * time.Second,
		metricas:             d.Metricas,
	}
}

// Rodar percorre a fila ate o contexto ser cancelado.
//
// O contexto e o unico sinal de parada. Um worker com um campo de "parar" propio
// teria duas fontes de verdade para a mesma decisao, e o shutdown terminaria de
// um jeito diferente do cancelamento, sem que ninguem pudesse dizer qual dos dois
// aconteceu.
func (w *Worker) Rodar(ctx context.Context) error {
	slog.Info("consumidor de operacoes no ar",
		"fila", w.fila.URL(),
		"lote", w.lote,
		"ocioso", w.ocioso.String(),
	)

	for {
		// O cancelamento e conferido antes de cada chamada: uma fila em long polling
		// segura a chamada por ate vinte segundos, e sem esta checagem o shutdown
		// esperaria esse tempo inteiro.
		if ctx.Err() != nil {
			return nil
		}

		mensagens, err := w.fila.Receber(ctx, w.lote)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Falha de recebimento e problema de infraestrutura, e nao da mensagem.
			// Voltar a tentar imediatamente transformaria uma fila fora em um laco
			// apertado de log.
			obs.Log(obs.De(ctx)).Error("falha ao receber mensagens", obs.ErroCom(err))
			if !dormir(ctx, w.ocioso) {
				return nil
			}
			continue
		}

		if len(mensagens) == 0 {
			if !dormir(ctx, w.ocioso) {
				return nil
			}
			continue
		}

		for _, mensagem := range mensagens {
			if !w.tratar(ctx, mensagem) {
				return nil
			}
		}
	}
}

// tratar processa uma mensagem e devolve false quando o worker deve parar.
func (w *Worker) tratar(ctx context.Context, mensagem sqs.Mensagem) bool {
	if ctx.Err() != nil {
		return false
	}

	inicio := time.Now()

	// A visibilidade e renovada enquanto o processamento dura. Sem isso, um
	// processamento mais lento que o tempo de visibilidade veria a mensagem ser
	// entregue a outra instancia no meio do trabalho.
	// O canal de renovacao comeca aberto e fecha no fim do processamento. E ele que
	// tira o goroutine de renovacao do ar quando a mensagem acaba: sem isso, cada
	// mensagem deixaria um goroutine renovando a visibilidade de algo que ja foi
	// processado.
	renovar := make(chan struct{})
	defer close(renovar)

	go func() {
		caso := time.NewTicker(w.renovaVisibilidade)
		defer caso.Stop()

		for {
			select {
			case <-renovar:
			case <-ctx.Done():
				return
			case <-caso.C:
				if err := w.fila.EstenderVisibilidade(ctx, mensagem.ReceiptHandle,
					w.limiteDeVisibilidade); err != nil {
					// Falha ao renovar nao invalida o processamento em andamento: a
					// transacao do banco ainda garante que o dinheiro nao se move duas
					// vezes. Vale o log, e nao o abandono.
					obs.Log(obs.De(ctx).ComMensagem(mensagem.ID)).
						Warn("nao foi possivel renovar a visibilidade", obs.ErroCom(err))
				}
			}
		}
	}()

	comando, err := comandoDaMensagem(mensagem)
	if err != nil {
		// Mensagem malformada nao melhora com repeticao: o produtor mandou algo que
		// este servico nao entende, e reentregar seria repetir o erro um numero
		// limitado de vezes ate a fila morta, sem chance de sucesso.
		//
		// A linha leva o identificador da mensagem e nao o do produtor: e o que o
		// operador tem para achar a mensagem exata na fila, que e o unico lugar onde
		// ela ainda existe.
		obs.Log(obs.De(ctx).ComMensagem(mensagem.ID)).
			Error("mensagem descartada por ser invalida",
				"tentativas", mensagem.Tentativas,
				obs.ErroCom(err),
			)
		return w.apagar(ctx, mensagem)
	}

	// O Contexto de log viaja do worker para o caso de uso. E o que amarra a linha da
	// operacao a linha da entrega: quem tem o messageId acha a operacao, e quem tem a
	// correlacao acha a entrega.
	//
	// A chave de idempotencia entra como correlacao quando o produtor nao mandou
	// nenhuma, porque a chave e o unico identificador estavel que existe antes do
	// caso de uso devolver o `transactionId`.
	registro := obs.De(ctx).
		ComMensagem(mensagem.ID).
		ComProvedor(string(comando.Provedor)).
		ComCarteira(comando.Carteira.String()).
		ComCorrelacao(observavelCorrelacao(comando))

	// O ator do consumidor e o provedor da propria mensagem. A autorizacao nao e
	// conferida aqui de novo: o produtor ja nao tem como escolher outro provedor,
	// porque a mensagem carrega o providerId e o caso de uso compara com o ator.
	ator := app.Ator{
		Cliente:  string(comando.Provedor),
		Provedor: string(comando.Provedor),
		Escopos:  []string{app.EscopoOperacoes},
	}

	resposta, err := app.ProcessarOperacao(registro.Anulado(), w.servicos, ator, comando)
	switch {
	case err == nil:
		obs.Log(registro.ComTransacao(resposta.TransacaoID.String())).
			Info("operacao concluida pela fila",
				"estado", string(resposta.Estado),
				"replay", resposta.Replay,
				obs.Duracao("duracao_ms", time.Since(inicio).Milliseconds()),
			)
		// Sucesso e recusa de regra contam como sucesso da mensagem: a operacao foi
		// concluida e registrada, e a resposta esta no banco e nos eventos. Reentregar
		// uma recusa de saldo so faria o provedor esperar mais uma vez.
		return w.apagar(ctx, mensagem)

	case ErroDeMensagem(err):
		obs.Log(registro).Error("mensagem descartada", obs.ErroCom(err))
		return w.apagar(ctx, mensagem)

	default:
		// Falha de infraestrutura: a mensagem volta. A idempotencia garante que a
		// reentrega nao mova dinheiro de novo, e o limite de tentativas da fila leva
		// o que sempre falha para o cartao morto.
		//
		// A contagem distingue os dois destinos: enquanto `tentativas` estiver abaixo
		// do maximo da fila, a mensagem vai voltar; quando passar, ela foi para o
		// cartao morto. Um contador so de retentativa esconderia o segundo caso, que e
		// o que o operador precisa para ir ver o cartao morto.
		if w.metricas != nil {
			if mensagem.Tentativas >= tentativasDaFilaMorta {
				w.metricas.MensagensFilaMorta.Inc("fila", "operacoes")
			} else {
				w.metricas.Retentativas.Inc("origem", "operacoes")
			}
		}

		obs.Log(registro).
			Error("operacao falhou, mensagem sera reentregue",
				"tentativas", mensagem.Tentativas,
				obs.Duracao("duracao_ms", time.Since(inicio).Milliseconds()),
				obs.ErroCom(err),
			)
		return true
	}
}

// observavelCorrelacao devolve a correlacao do comando.
//
// A chave de idempotencia e o fallback, e nao um valor inventado: e o identificador
// que o produtor pode citar no chamado, e ele ja existia antes de qualquer codigo
// daqui rodar. Um UUID novo seria um identificador que ninguem, nem o operador nem o
// produtor, consegue usar para chegar no log.
func observavelCorrelacao(comando app.RequisicaoOperacao) string {
	if comando.Correlacao != "" {
		return comando.Correlacao
	}
	return string(comando.Chave)
}

// apagar remove a mensagem da fila.
func (w *Worker) apagar(ctx context.Context, mensagem sqs.Mensagem) bool {
	if err := w.fila.Concluir(ctx, mensagem.ReceiptHandle); err != nil {
		obs.Log(obs.De(ctx).ComMensagem(mensagem.ID)).
			Error("nao foi possivel apagar a mensagem", obs.ErroCom(err))
		// Devolver true porque a falha e de infraestrutura: a mensagem volta e sera
		// reprocessada, e o idempotencia devolve o resultado sem mover dinheiro.
		return true
	}
	return true
}

// comandoDaMensagem converte o corpo no comando de caso de uso.
// comandoDaMensagem converte a mensagem no comando de caso de uso.
//
// A funcao recebe a `sqs.Mensagem` inteira, e nao so o corpo, porque o comando precisa
// do `messageId` da fila: e ele a identidade duravel da mensagem, e sem ele a inbox nao
// tem o que registrar. Passar so o corpo -- que foi o que a E13 fez -- deixava o
// `MensagemID` vazio, e a inbox nunca recebia nada, sem que nenhum teste percebesse
// porque a deduplicacao por chave de idempotencia ja resolvia a reentrega.
func comandoDaMensagem(mensagem sqs.Mensagem) (app.RequisicaoOperacao, error) {
	var corpo MensagemOperacao
	if err := json.Unmarshal([]byte(mensagem.Corpo), &corpo); err != nil {
		return app.RequisicaoOperacao{}, &erroDeMensagem{motivo: "corpo nao e JSON: " + err.Error()}
	}

	if corpo.IdempotencyKey == "" {
		return app.RequisicaoOperacao{}, &erroDeMensagem{
			motivo: "idempotencyKey ausente: sem chave o servidor nao pode prometer que reentregar nao move dinheiro duas vezes"}
	}

	valor, err := money.Parse(corpo.Money.Amount, money.Currency(corpo.Money.Currency))
	if err != nil {
		return app.RequisicaoOperacao{}, &erroDeMensagem{motivo: "money invalido: " + err.Error()}
	}

	jogador, err := wallet.IdentificadorDe(corpo.PlayerId)
	if err != nil {
		return app.RequisicaoOperacao{}, &erroDeMensagem{motivo: "playerId invalido"}
	}
	carteira, err := wallet.IdentificadorDe(corpo.WalletId)
	if err != nil {
		return app.RequisicaoOperacao{}, &erroDeMensagem{motivo: "walletId invalido"}
	}

	// O resumo e calculado com os mesmos campos e o mesmo algoritmo que o caminho
	// HTTP. E o que faz a mesma operacao ser reconhecida como a mesma quando chega
	// pelo outro caminho.
	resumo := fingerprint.Calcular(fingerprint.Entrada{
		Provedor:   corpo.ProviderId,
		Externa:    corpo.ExternalTransactionId,
		Jogador:    corpo.PlayerId,
		Carteira:   corpo.WalletId,
		Rodada:     corpo.RoundId,
		Jogo:       corpo.GameId,
		Tipo:       corpo.Kind,
		Valor:      valor.Decimal(),
		Moeda:      string(valor.Currency()),
		Referencia: corpo.ReferenceExternalTransactionId,
	})

	return app.RequisicaoOperacao{
		Provedor:         wagering.Provedor(corpo.ProviderId),
		TransacaoExterna: wagering.Externo(corpo.ExternalTransactionId),
		Chave:            wagering.Chave(corpo.IdempotencyKey),
		Fingerprint:      wagering.Hash(resumo),
		Carteira:         carteira,
		Jogador:          jogador,
		Rodada:           wagering.Rodada(corpo.RoundId),
		Jogo:             wagering.Jogo(corpo.GameId),
		Tipo:             wagering.Tipo(corpo.Kind),
		Valor:            valor,
		Referencia:       wagering.Referencia{Externa: wagering.Externo(corpo.ReferenceExternalTransactionId)},
		MensagemID:       mensagem.ID,
		Consumidor:       nomeDoConsumidor,
	}, nil
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

// FalhaDeMensagemE o erro de mensagem malformada com o motivo.
//
// Existe para o teste verificar o motivo sem depender de texto exato.
func FalhaDeMensagemE(err error) (string, bool) {
	var malformada *erroDeMensagem
	if errors.As(err, &malformada) {
		return malformada.motivo, true
	}
	return "", false
}
