// Package app e a camada de casos de uso: onde um comando externo vira decisao
// financeira persistida.
//
// A dependencia aponta sempre para baixo e para o dominio: app conhece os
// repositorios e o dominio, e o dominio nao conhece nenhum dos dois. E o que
// permite testar a regra de negocio sem banco e a persistencia sem regra.
//
// O que esta camada decide, e o que ninguem acima dela pode decidir: a fronteira
// da transacao SQL. Cada metodo publico de caso de uso abre uma unidade de
// trabalho e a fecha, e nenhum repositorio abre transacao propria. A carteira, o
// lancamento, a transacao, a inbox e a outbox sao confirmados no mesmo commit
// porque estao dentro da mesma unidade -- e nao porque cada gravacao cuida de
// confirmar a si mesma.
package app

import (
	"errors"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// Ator e a identidade autenticada que autoriza o comando.
//
// Ele chega ja resolvido da borda e nao e construido aqui. O caso de uso recebe a
// identidade e decide o que ela pode fazer; ele nao sabe de onde ela veio, e nao
// sabe ler token. E por isso que o mesmo caso de uso serve a requisicao HTTP e a
// mensagem SQS, e por isso que o isolamento entre provedores nao depende de qual
// transporte trouxe o comando.
type Ator struct {
	// Cliente e o client_id do token, que vem do claim azp.
	Cliente string
	// Provedor e o identificador de provedor autorizado. Vazio para o cliente
	// interno, que nao e provedor.
	Provedor string
	// Escopos sao os escopos concedidos pelo IdP, que e onde a separacao entre
	// "provedor" e "servico interno" mora.
	Escopos []string
}

// Escopos do contrato de autorizacao.
//
// Nao sao strings livres: a lista e fechada porque um escopo nao conhecido no
// token e um escopo que nao autoriza nada, e o caso de uso precisa saber a
// diferenca entre "nao tem este escopo" e "isto aqui nao existe".
const (
	EscopoOperacoes        = "wager:operacoes"
	EscopoAberturaCarteira = "wager:carteira:abertura"
	EscopoReconciliacao    = "wager:reconciliacao"
)

// Interno informa se o ator e o cliente interno, e nao um provedor.
//
// A distincao e o que separa "abrir carteira" de "enviar operacao". Um provedor
// tem escopo de operacoes e nunca tem escopo de abertura, e a checagem aqui e a
// segunda: a primeira e o escopo, que vem do IdP.
func (a Ator) Interno() bool { return a.Provedor == "" }

// EhProvedor informa se o ator e um provedor de jogos.
func (a Ator) EhProvedor() bool { return a.Provedor != "" }

// TemEscopo informa se o ator tem o escopo.
//
// A comparacao e exata. Escopo por prefixo transformaria "wager:operacoes" em
// "wager:operacoes:qualquer-coisa", que e um escopo novo que o IdP nunca concedeu.
func (a Ator) TemEscopo(escopo string) bool {
	for _, concedido := range a.Escopos {
		if concedido == escopo {
			return true
		}
	}
	return false
}

// Erros de caso de uso.
//
// Sao erros de decisao, e nao de driver: o chamador precisa saber se recusa, se
// repete ou se volta depois, e essa escolha e dele.
var (
	// ErrNaoAutorizado cobre ator sem o escopo do comando.
	ErrNaoAutorizado = errors.New("app: ator nao autorizado")
	// ErrProvedorDivergente cobre comando cujo payload aponta para outro provedor.
	ErrProvedorDivergente = errors.New("app: provedor do comando diferente do autorizado")
	// ErrRequisicaoInvalida cobre dado de entrada ausente ou malformado.
	ErrRequisicaoInvalida = errors.New("app: requisicao invalida")
	// ErrConflitoDeChave cobre reuso de chave de idempotencia com conteudo diferente.
	ErrConflitoDeChave = errors.New("app: chave de idempotencia com conteudo diferente")
	// ErrDivergenciaDeSaldo cobre reconciliacao que encontrou divergencia.
	ErrDivergenciaDeSaldo = errors.New("app: divergencia de saldo")
)

// Relogio fornece o instante atual.
//
// E interface, e nao time.Now(), por um motivo que e o mesmo de todo o resto: um
// caso de uso que chama time.Now nao tem como ser testado de forma deterministica,
// e os testes de ordem de insercao na outbox dependem do instante.
type Relogio interface {
	// Agora devolve o instante corrente em UTC.
	Agora() time.Time
}

// RelogioDeSistema usa o relogio do processo.
type RelogioDeSistema struct{}

// Agora devolve o instante corrente em UTC.
func (RelogioDeSistema) Agora() time.Time { return time.Now().UTC() }

// Servicos e o conjunto de dependencias dos casos de uso.
//
// E um unico parametro em vez de sete argumentos posicionais: com sete, trocar
// dois repositorios compila e produz a operacao errada em producao.
type Servicos struct {
	// Unidade e a fronteira da transacao SQL.
	Unidade *pg.Unidade
	// Carteiras, Ledger, Transacoes, Inbox e Outbox sao os repositorios.
	Carteiras  *pg.RepositorioCarteira
	Ledger     *pg.RepositorioLedger
	Transacoes *pg.RepositorioTransacoes
	Inbox      *pg.RepositorioInbox
	Outbox     *pg.RepositorioOutbox
	// Relogio fornece o instante das operacoes.
	Relogio Relogio
	// Correlacao gera o identificador de correlacao de um comando quando a borda
	// nao informou um.
	Correlacao func() string
	// Metricas mede os desfechos e as latencias.
	//
	// Entra em Servicos e nao como parametro de cada caso de uso porque medir e parte
	// do que o caso de uso decide: quem sabe o desfecho e a duracao do commit e o
	// mesmo codigo que decide. Nil desliga a medicao, e o que permite ao processo de
	// migrations e ao teste de dominio rodarem sem registro.
	Metricas *obs.Metricas
}

// registraDesfecho conta o desfecho de uma operacao.
//
// E metodo em `Servicos` e nao funcao solta porque todos os caminhos de desfecho
// precisam contar, e um helper que cada um esquece de chamar e um helper que nao
// conta. Com o metodo, o `nil` ja e a condicao de "nao medir", e nenhum caminho
// precisa perguntar se o registro existe.
func (s Servicos) registraDesfecho(estado string) {
	if s.Metricas != nil {
		s.Metricas.Operacoes.Inc("estado", estado)
	}
}

// registraDuplicata conta um replay reconhecido pela idempotencia.
//
// Contar replay como operacao seria errado nas duas direcoes: o painel mostraria
// mais operacoes do que aconteceram, e a duplicata -- que e o que precisa ser
// visivel -- ficaria misturada com o caminho feliz.
func (s Servicos) registraDuplicata(via string) {
	if s.Metricas != nil {
		s.Metricas.Duplicatas.Inc("via", via)
	}
}

// registraConflitoLock conta uma transacao que perdeu a disputa pelo lock.
//
// E metodo em `Servicos` porque a traducao do erro de lock para o rotulo da metrica
// e responsabilidade do caso de uso: o repositorio devolve o erro do driver, e quem
// sabe o que aquele erro significa no contexto da carteira e o codigo que pediu a
// operacao.
func (s Servicos) registraConflitoLock(motivo string) {
	if s.Metricas != nil {
		s.Metricas.ConflitosLock.Inc("motivo", motivo)
	}
}

// verifica confere que os servicos minimos existem.
//
// Falhar aqui, na montagem, e melhor do que um nil panic no meio de uma transacao
// financeira, que deixa a transacao abierta ate o rollback por contexto.
func (s Servicos) verifica(necessarios ...string) error {
	faltando := []string{}
	for _, nome := range necessarios {
		switch nome {
		case "unidade":
			if s.Unidade == nil {
				faltando = append(faltando, "Unidade")
			}
		case "carteiras":
			if s.Carteiras == nil {
				faltando = append(faltando, "Carteiras")
			}
		case "ledger":
			if s.Ledger == nil {
				faltando = append(faltando, "Ledger")
			}
		case "transacoes":
			if s.Transacoes == nil {
				faltando = append(faltando, "Transacoes")
			}
		case "inbox":
			if s.Inbox == nil {
				faltando = append(faltando, "Inbox")
			}
		case "outbox":
			if s.Outbox == nil {
				faltando = append(faltando, "Outbox")
			}
		case "relogio":
			if s.Relogio == nil {
				faltando = append(faltando, "Relogio")
			}
		case "correlacao":
			if s.Correlacao == nil {
				faltando = append(faltando, "Correlacao")
			}
		}
	}

	if len(faltando) > 0 {
		return fmt.Errorf("%w: %v", ErrRequisicaoInvalida, faltando)
	}
	return nil
}

// agora devolve o instante corrente, com o relogio de sistema como reserva.
//
// A reserva existe porque um Servicos sem relogio aparece em teste e em ferramenta
// de administracao, e um nil panic em nome de instancia seria pior que um
// relogio honesto.
func (s Servicos) agora() time.Time {
	if s.Relogio == nil {
		return RelogioDeSistema{}.Agora()
	}
	return s.Relogio.Agora()
}

// correlacao devolve o identificador de correlacao do comando.
func (s Servicos) correlacao(informada string) string {
	if informada != "" {
		return informada
	}
	if s.Correlacao == nil {
		return ""
	}
	return s.Correlacao()
}

// garantirJogador evita repetir a checagem de identificador em cada caso de uso.
func garantirJogador(jogador wallet.Identificador) error {
	if !jogador.Valida() {
		return fmt.Errorf("%w: jogador ausente", ErrRequisicaoInvalida)
	}
	return nil
}
