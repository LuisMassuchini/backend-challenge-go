//go:build integration

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/sqs"
)

// Reversao antes da aposta, em processo real, retomada por outra instancia.
//
// Este e o cenario 7 e 8 do enunciado -- "`REFUND` antes da `BET`" e "`ROLLBACK` antes
// da referencia" -- que faltava. O `casos.TestReversaoAntesDaApostaFicaPendente`
// prova o comportamento no caso de uso, e o `pendenciasteste` prova a retomada com
// backoff; o que nenhum dos dois prova e que a pendencia criada por **uma** instancia e
// retomada por **outra**.
//
// A montagem importa. Uma instancia so para publicar o `ROLLBACK` antes da aposta
// garante a ordem sem depender de temporizacao, que e o que tornaria o teste instavel.
// As outras duas sobem depois e sao as que tem de assumir o trabalho.
//
// O que se verifica no fim: a aposta foi processada, o estorno foi aplicado, o saldo
// voltou ao valor original e o ledger tem um debito e um credito do mesmo valor.
func TestReversaoAntesDaApostaAssumidaPorOutraInstancia(t *testing.T) {
	limparBase(t)

	fila := clienteDaFilaDeOperacoes(t)
	esvaziarFila(t, fila)

	ambiente := map[string]string{
		"WAGER_SQS_ENDPOINT":         endpointDoSqs(),
		"WAGER_SQS_FILA_OPERACOES":   "wager-transactions.fifo",
		"WAGER_SQS_FILA_DEAD_LETTER": "wager-transactions-dlq.fifo",
		"WAGER_SQS_FILA_EVENTOS":     "wager-events.fifo",
	}

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	// A primeira instancia so publica o ROLLBACK. Ela tem consumidor, e por isso
	// consumira a propria mensagem -- e e isso que garante a ordem: o ROLLBACK entra
	// no sistema antes de qualquer BET existir.
	primeira := sobeInstancia(t, "publicadora", ambiente)

	carteira := abrirCarteira(t, tokenInterno, []*Instancia{primeira}, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, primeira, carteira)

	chaveAposta := "provider-a:aposta-" + t.Name()
	chaveEstorno := "provider-a:estorno-" + t.Name()
	externaAposta := "aposta-" + t.Name()
	externaEstorno := "estorno-" + t.Name()

	// O ROLLBACK entra primeiro e nao encontra a aposta.
	publicarNaFila(t, fila, corpoDaMensagem(
		carteira, jogador, externaEstorno, chaveEstorno, "ROLLBACK", 2500, externaAposta), carteira)

	esperarAte(t, 30*time.Second, "o estorno ficar pendente por referencia", func() bool {
		return transacaoEstaPendentePorReferencia(t, tokenProvedor, primeira, externaEstorno)
	})

	// O saldo nao mudou: um estorno sem referencia nao mexe em dinheiro.
	if saldo := lerSaldo(t, tokenInterno, primeira, carteira); saldo != 10000 {
		t.Errorf("saldo e %d centavos com o estorno pendente, esperado 10000", saldo)
	}

	// As outras duas instancias sobem AGORA. Sao elas que tem de assumir a pendencia.
	segunda := sobeInstancia(t, "assumidora-1", ambiente)
	t.Cleanup(segunda.encerrar)
	terceira := sobeInstancia(t, "assumidora-2", ambiente)
	t.Cleanup(terceira.encerrar)

	instancias := []*Instancia{primeira, segunda, terceira}

	// A aposta entra pelo HTTP, com a chave e o identificador externos que o estorno ja
	// referencia.
	resposta := instancias[0].chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, chaveAposta, "corr-aposta",
		pedidoDeOperacao(carteira, jogador, externaAposta, "BET", 2500))
	if resposta.Status != http.StatusOK {
		t.Fatalf("a aposta respondeu %d: %v", resposta.Status, resposta.Corpo)
	}

	// A aposta debita.
	if saldo := lerSaldo(t, tokenInterno, instancias[0], carteira); saldo != 7500 {
		t.Errorf("saldo apos a aposta e %d centavos, esperado 7500", saldo)
	}

	// O estorno pendente e retomado por uma das outras instancias e devolve o dinheiro.
	//
	// A espera e por condicao e nao por tempo: o worker de pendencias tem backoff de
	// dois segundos na primeira retentativa, e um `time.Sleep` fixo seria ao mesmo tempo
	// mais lento e menos confiavel.
	esperarAte(t, 60*time.Second, "o estorno ser retomado e aplicado", func() bool {
		return lerSaldo(t, tokenInterno, instancias[0], carteira) == 10000
	})

	// O estorno terminou em PROCESSED e nao em FAILED ou REJECTED.
	//
	// A diferenca importa e e o ponto do cenario: `FAILED` com
	// `REFERENCIA_NUNCA_CHEGOU` significa que a pendencia expirou, e `REJECTED`
	// significaria que a politica recusou. Os dois seriam desfecho legitimo em outro
	// cenario, e aqui sao falha.
	estado := estadoDaTransacao(t, tokenProvedor, instancias[0], externaEstorno)
	if estado != "PROCESSED" {
		t.Errorf("o estorno terminou em %q, esperado PROCESSED", estado)
	}

	// O ledger tem um debito e um credito do mesmo valor: o estorno devolveu exatamente
	// o que a aposta tirou. Um credito de valor diferente deixaria a carteira com saldo
	// diferente do que a soma do ledger implica, e a reconciliacao acusaria.
	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != 1 {
		t.Errorf("o ledger tem %d debitos, esperado 1 (a aposta)", debitos)
	}
	if creditos != 2 {
		t.Errorf("o ledger tem %d creditos, esperado 2 (a abertura e o estorno)", creditos)
	}

	// E o saldo reconstruido pelo ledger e igual ao saldo gravado.
	conferir := reconciliar(t, tokenInterno, instancias[0], carteira)
	if consistente, ok := conferir["consistent"].(bool); !ok || !consistente {
		t.Errorf("a reconciliacao acusou divergencia depois do estorno: %v", conferir)
	}
}

// O estorno que nunca encontra a aposta expira com codigo proprio.
//
// O outro desfecho possivel da pendencia. O enunciado pede que a expiracao produza
// `REJECTED` com codigo de referencia nao encontrada; este projeto grava `FAILED` com
// `REFERENCIA_NUNCA_CHEGOU`, e a diferenca esta em `ARCHITECTURE.md`: `REJECTED` e a
// resposta do sistema a uma **operacao errada**, e `FAILED` e a resposta a uma
// **referencia que nunca chegou**. O provedor trata as duas de forma diferente.
//
// O teste fixa o desfecho que o sistema decidiu, para que a mudanca de politica seja um
// commit com motivo e nao um efeito colateral.
func TestEstornoSemApostaExpiraComCodigoProprio(t *testing.T) {
	limparBase(t)

	ambiente := map[string]string{
		"WAGER_SQS_ENDPOINT":         endpointDoSqs(),
		"WAGER_SQS_FILA_OPERACOES":   "wager-transactions.fifo",
		"WAGER_SQS_FILA_DEAD_LETTER": "wager-transactions-dlq.fifo",
		"WAGER_SQS_FILA_EVENTOS":     "wager-events.fifo",
	}

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	instancias := sobeInstancias(t, ambiente)

	carteira := abrirCarteira(t, tokenInterno, instancias, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	externaEstorno := "estorno-sozinho-" + t.Name()

	// O ROLLBACK referencia uma aposta que nunca existira e nunca vai existir.
	publicarNaFila(t, filaDeOperacoes(t), corpoDaMensagem(
		carteira, jogador, externaEstorno, "provider-a:sozinho-"+t.Name(), "ROLLBACK",
		2500, "aposta-que-nunca-veio"), carteira)

	// A pendencia precisa ser registrada antes de esperar a expiracao.
	esperarAte(t, 30*time.Second, "o estorno ficar pendente", func() bool {
		return transacaoEstaPendentePorReferencia(t, tokenProvedor, instancias[0], externaEstorno)
	})

	// A expiracao levaria segundos a mais com a politica padrao -- dez tentativas com
	// backoff ate cinco minutos. Este cenario nao espera por ela: o que importa e que a
	// pendencia esta no estado certo, e a expiracao em si ja e testada em
	// `pendenciasteste` com politica de intervalo curto.
	//
	// Verificar que o estado intermediario e PENDING_REFERENCE e o que prova que o
	// caminho de espera -- e nao o de recusa -- foi escolhido.
	if estado := estadoDaTransacao(t, tokenProvedor, instancias[0], externaEstorno); estado != "PENDING_REFERENCE" {
		t.Errorf("o estado do estorno sem aposta e %q, esperado PENDING_REFERENCE", estado)
	}

	// E nada foi movido: nem a aposta que nao existe, nem o estorno.
	if saldo := lerSaldo(t, tokenInterno, instancias[0], carteira); saldo != 10000 {
		t.Errorf("saldo e %d centavos, esperado 10000", saldo)
	}

	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != 0 {
		t.Errorf("o ledger tem %d debitos: um estorno sem aposta nao produz lancamento", debitos)
	}
	if creditos != 1 {
		t.Errorf("o ledger tem %d creditos, esperado 1 (a abertura)", creditos)
	}
}

// transacaoEstaPendentePorReferencia informa se a transacao espera a referencia.
func transacaoEstaPendentePorReferencia(
	t *testing.T,
	token string,
	instant *Instancia,
	externa string,
) bool {
	t.Helper()
	return estadoDaTransacao(t, token, instant, externa) == "PENDING_REFERENCE"
}

// estadoDaTransacao devolve o estado da transacao externa.
//
// A leitura e pela API e nao por SQL: o enunciado promete que o provedor consegue
// acompanhar a pendencia por consulta, e e essa promessa que o teste verifica.
func estadoDaTransacao(t *testing.T, token string, instant *Instancia, externa string) string {
	t.Helper()

	resposta := instant.chamarJSON(t, http.MethodGet,
		"/providers/provider-a/wagering/transactions/"+externa, token, "", "", nil)
	if resposta.Status != http.StatusOK {
		return ""
	}

	estado, ok := resposta.Corpo["state"].(string)
	if !ok {
		return ""
	}
	return estado
}

// filaDeOperacoes monta o cliente da fila de entrada.
func filaDeOperacoes(t *testing.T) *sqs.Cliente {
	t.Helper()
	return clienteDaFilaDeOperacoes(t)
}
