//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/sqs"
)

// endpointDoSqs devolve o endereco do LocalStack.
func endpointDoSqs() string {
	if valor := os.Getenv("WAGER_TEST_SQS_ENDPOINT"); valor != "" {
		return valor
	}
	return "http://localhost:4566"
}

// clienteDaFilaDeOperacoes monta o cliente SQS da fila de entrada.
func clienteDaFilaDeOperacoes(t *testing.T) *sqs.Cliente {
	t.Helper()

	cliente, err := sqs.Novo(contextoDeTeste(t), sqs.Opcoes{
		Endpoint:        endpointDoSqs(),
		Regiao:          "us-east-1",
		ChaveDeAcesso:   "test",
		SegredoDeAcesso: "test",
		FilaOperacoes:   "wager-transactions.fifo",
		FilaDeadLetter:  "wager-transactions-dlq.fifo",
	})
	if err != nil {
		t.Skipf("a fila de operacoes nao esta acessivel: %v", err)
	}
	return cliente
}

// publicarNaFila envia uma mensagem de operacao.
//
// O grupo de particao e a carteira, pelo mesmo motivo que a E13 decidiu: duas operacoes
// da mesma carteira sao relacionadas e precisam de ordem.
//
// **A chave de deduplicacao precisa ser unica por mensagem, e nao por grupo.** A
// deduplicacao do SQS e uma janela de cinco minutos: duas mensagens do mesmo grupo com
// a mesma `MessageDeduplicationId` fazem o broker descartar a segunda, e o teste passa
// sem que o consumidor tenha visto nada. Foi o que aconteceu na primeira execucao do
// cruzamento -- a mensagem conflitante nunca chegou ao consumidor, e o sintoma
// aparecia como "a inbox nao registrou" em vez de "a mensagem nao foi publicada".
func publicarNaFila(t *testing.T, cliente *sqs.Cliente, corpo, grupo string) {
	t.Helper()

	chave := "e2e-" + uuid.NewString()
	if err := cliente.Publicar(contextoDeTeste(t), grupo, chave, 0, corpo); err != nil {
		t.Fatalf("publicar na fila: %v", err)
	}
}

// esvaziarFila remove as mensagens visiveis.
//
// E no comeco de cada cenario: uma mensagem deixada pelo teste anterior seria consumida
// aqui e o resultado passaria a depender da ordem de execucao.
func esvaziarFila(t *testing.T, cliente *sqs.Cliente) {
	t.Helper()

	for i := 0; i < 40; i++ {
		mensagens, err := cliente.Receber(contextoDeTeste(t), 10)
		if err != nil {
			t.Fatalf("recebimento na limpeza: %v", err)
		}
		if len(mensagens) == 0 {
			return
		}
		for _, mensagem := range mensagens {
			if err := cliente.Concluir(contextoDeTeste(t), mensagem.ReceiptHandle); err != nil {
				t.Fatalf("limpeza da fila: %v", err)
			}
		}
	}
}

// contextoDeTeste devolve um contexto com prazo.
func contextoDeTeste(t *testing.T) context.Context {
	t.Helper()
	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelar)
	return ctx
}

// MensagemOperacao e o corpo da mensagem de operacao.
//
// Os nomes sao os do contrato HTTP, e sao os mesmos de proposito: a mesma operacao que
// entra por uma ponta tem que produzir o mesmo resumo de conteudo nas duas, e o resumo
// e calculado a partir destes campos.
type MensagemOperacao struct {
	ProviderId            string `json:"providerId"`
	ExternalTransactionId string `json:"externalTransactionId"`
	PlayerId              string `json:"playerId"`
	WalletId              string `json:"walletId"`
	RoundId               string `json:"roundId"`
	GameId                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"money"`
	ReferenceExternalTransactionId string `json:"referenceExternalTransactionId,omitempty"`
	IdempotencyKey                 string `json:"idempotencyKey"`
}

// corpoDaMensagem monta o corpo de uma mensagem de operacao.
func corpoDaMensagem(
	carteira, jogador, externa, chave, tipo string,
	centavos int64,
	referencia string,
) string {
	mensagem := MensagemOperacao{
		ProviderId:            "provider-a",
		ExternalTransactionId: externa,
		PlayerId:              jogador,
		WalletId:              carteira,
		RoundId:               "round-987",
		GameId:                "fortune-chimp",
		Kind:                  tipo,
		IdempotencyKey:        chave,
	}
	mensagem.Money.Amount = fmt.Sprintf("%d.%02d", centavos/100, abs(centavos%100))
	mensagem.Money.Currency = "BRL"
	mensagem.ReferenceExternalTransactionId = referencia

	bruto, err := json.Marshal(mensagem)
	if err != nil {
		// Um erro de marshal aqui e bug de teste, e `panic` e aceitavel: a funcao nao
		// recebe `t` de proposito, e um `t.Fatal` dentro dela mostraria a linha do
		// auxiliar em vez da linha de quem chamou.
		panic(err)
	}
	return string(bruto)
}

// A mesma operacao por HTTP e por SQS e aplicada uma vez so.
//
// Este e o cruzamento que o enunciado pede, e ele prova algo que nenhum dos dois caminhos
// prova sozinho: que o resumo canonico de conteudo -- de onde vem a decisao entre replay
// e conflito -- e o mesmo nos dois transportes. Se o hash differisse entre HTTP e SQS, o
// sistema trataria uma reentrega pelo outro caminho como CONFLITO, que e o mesmo defeito
// que a corrida de idempotencia teve, so que deterministico.
//
// O caminho escolhido e o que interessa: HTTP primeiro, SQS depois. O inverso -- SQS
// primeiro -- encontraria a chave ja registrada e transformaria o teste em uma
// verificacao de replay que o caminho HTTP faz desde a E9.
func TestMesmaOperacaoPorHTTPESQSAplicaUmaVez(t *testing.T) {
	limparBase(t)

	fila := clienteDaFilaDeOperacoes(t)
	esvaziarFila(t, fila)

	instancias := sobeInstanciadasComSqs(t, fila)

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	carteira := abrirCarteira(t, tokenInterno, instancias, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	chave := "provider-a:cruzamento-" + t.Name()
	externa := "transacao-cruzamento-" + t.Name()

	// Pelo HTTP primeiro.
	respostaHTTP := instancias[0].chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, chave, "corr-cruzamento-http",
		pedidoDeOperacao(carteira, jogador, externa, "BET", 2500))
	if respostaHTTP.Status != http.StatusOK {
		t.Fatalf("a operacao por HTTP respondeu %d: %v", respostaHTTP.Status, respostaHTTP.Corpo)
	}
	if replay, ok := respostaHTTP.Corpo["idempotentReplay"].(bool); ok && replay {
		t.Fatal("a primeira operacao por HTTP veio marcada como replay")
	}

	// Pela fila depois, com o MESMO conteudo de negocio.
	publicarNaFila(t, fila, corpoDaMensagem(carteira, jogador, externa, chave, "BET", 2500, ""), carteira)

	// A resposta da fila nao vem para o provedor, entao o que se verifica e o estado no
	// banco depois que o consumidor processou.
	esperarAte(t, 30*time.Second, "a mensagem da fila ser consumida", func() bool {
		return transacaoEstaTerminal(t, tokenProvedor, instancias[0], externa)
	})

	// O saldo caiu uma vez so: se a fila tivesse aplicado de novo, seriam 75.00 - 25.00.
	saldo := lerSaldo(t, tokenInterno, instancias[0], carteira)
	if esperado := int64(7500); saldo != esperado {
		t.Errorf("saldo e %d centavos, esperado %d: a operacao foi aplicada duas vezes", saldo, esperado)
	}

	// E o ledger tem um unico debito, que e a prova que nao depende do saldo.
	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != 1 {
		t.Errorf("o ledger tem %d debitos, esperado 1: a fila aplicou a operacao de novo", debitos)
	}
	if creditos != 1 {
		t.Errorf("o ledger tem %d creditos, esperado 1 (a abertura)", creditos)
	}

	// E a transacao na base e uma so. Duas linhas com a mesma chave violariam o indice
	// unico, entao este e um controle de sanidade: se ele falha, algo muito anterior
	// esta errado.
	conferir := reconciliar(t, tokenInterno, instancias[0], carteira)
	if consistente, ok := conferir["consistent"].(bool); !ok || !consistente {
		t.Errorf("a reconciliacao acusou divergencia depois do cruzamento: %v", conferir)
	}
}

// O inverso tambem: SQS primeiro, HTTP depois, e o resultado e o da fila.
//
// O caminho inverso importa porque e o que acontece em producao quando o provedor
// envia a mensagem e o cliente HTTP de controle pergunta o resultado. O provedor nao
// deve ver conflito, e o saldo nao deve mexer.
func TestMesmaOperacaoPorSQSEHTTPAplicaUmaVez(t *testing.T) {
	limparBase(t)

	fila := clienteDaFilaDeOperacoes(t)
	esvaziarFila(t, fila)

	instancias := sobeInstanciadasComSqs(t, fila)

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	carteira := abrirCarteira(t, tokenInterno, instancias, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	chave := "provider-a:cruzamento-inverso-" + t.Name()
	externa := "transacao-cruzamento-inverso-" + t.Name()

	// Pela fila primeiro.
	publicarNaFila(t, fila, corpoDaMensagem(carteira, jogador, externa, chave, "BET", 2500, ""), carteira)
	esperarAte(t, 30*time.Second, "a mensagem da fila ser consumida", func() bool {
		return transacaoEstaTerminal(t, tokenProvedor, instancias[0], externa)
	})

	// Pelo HTTP depois, com o mesmo conteudo.
	respostaHTTP := instancias[0].chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, chave, "corr-cruzamento-inverso",
		pedidoDeOperacao(carteira, jogador, externa, "BET", 2500))

	// O resultado que importa e o codigo. Conflito aqui significaria que os dois
	// transportes produziram resumos de conteudo diferentes -- que e exatamente o
	// defeito que este cenario existe para pegar.
	if respostaHTTP.Status == http.StatusConflict {
		t.Fatalf("o HTTP devolviu conflito para uma operacao ja aplicada pela fila: %v",
			respostaHTTP.Corpo)
	}
	if respostaHTTP.Status != http.StatusOK {
		t.Fatalf("o HTTP respondeu %d: %v", respostaHTTP.Status, respostaHTTP.Corpo)
	}
	if replay, ok := respostaHTTP.Corpo["idempotentReplay"].(bool); !ok || !replay {
		t.Errorf("o HTTP nao marcou a resposta como replay: %v", respostaHTTP.Corpo)
	}

	if saldo := lerSaldo(t, tokenInterno, instancias[0], carteira); saldo != int64(7500) {
		t.Errorf("saldo e %d centavos, esperado 7500", saldo)
	}
}

// conteudoDiferenteComAMesmaChaveEConflitoNosDoisCaminhos.
//
// E o outro lado do cruzamento: o hash canonico precisa distinguir "mesma operacao"
// de "outra operacao com a mesma chave". Se aceitasse as duas como a mesma, o provedor
// receberia o resultado de uma aposta como se fosse o de outra -- o dinheiro nao
// duplica, mas o provedor acredita que a aposta dele passou.
func TestConteudoDiferenteComAMesmaChaveEConflito(t *testing.T) {
	limparBase(t)

	fila := clienteDaFilaDeOperacoes(t)
	esvaziarFila(t, fila)

	instancias := sobeInstanciadasComSqs(t, fila)

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	carteira := abrirCarteira(t, tokenInterno, instancias, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	chave := "provider-a:cruzamento-conflito-" + t.Name()
	externa := "transacao-conflito-" + t.Name()

	// HTTP com 25.00.
	respostaHTTP := instancias[0].chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, chave, "corr-conflito-http",
		pedidoDeOperacao(carteira, jogador, externa, "BET", 2500))
	if respostaHTTP.Status != http.StatusOK {
		t.Fatalf("a primeira operacao respondeu %d: %v", respostaHTTP.Status, respostaHTTP.Corpo)
	}

	// Fila com 30.00 e a MESMA chave: e outra operacao, e a mesma chave.
	publicarNaFila(t, fila, corpoDaMensagem(carteira, jogador, externa, chave, "BET", 3000, ""), carteira)

	// O que se espera e a INBOX ter registrado a mensagem, e nao a mensagem sumir da
	// fila.
	//
	// A inbox e o que prova que o consumidor realmente pegou a mensagem: ela e
	// gravada na transacao do tratamento, entao sua presenca significa que o
	// processamento chegou ate o caso de uso. A alternativa -- esperar a mensagem
	// desaparecer da fila -- custaria um ciclo de redrive inteiro, porque uma mensagem
	// que falha volta depois do tempo de visibilidade.
	//
	// O `messageId` nao e conhecido aqui, entao a espera e pela CONTAGEM de mensagens
	// na inbox. Isso e valido porque a base foi limpa no inicio do cenario.
	esperarAte(t, 30*time.Second, "o consumidor registrar a mensagem conflitante", func() bool {
		return contarMensagensNaInbox(t) >= 1
	})

	// E o saldo nao mudou: o conflito nao move dinheiro.
	if saldo := lerSaldo(t, tokenInterno, instancias[0], carteira); saldo != int64(7500) {
		t.Errorf("saldo e %d centavos, esperado 7500: o conteudo diferente foi aplicado", saldo)
	}

	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, _ := contaLancamentosPorDirecao(lancamentos)
	if debitos != 1 {
		t.Errorf("o ledger tem %d debitos, esperado 1", debitos)
	}

	// E pelo HTTP com conteudo diferente tambem e conflito.
	resposta := instancias[0].chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, chave, "corr-conflito-http-2",
		pedidoDeOperacao(carteira, jogador, externa, "BET", 3000))
	if resposta.Status != http.StatusConflict {
		t.Errorf("o HTTP com conteudo diferente respondeu %d, esperado 409: %v",
			resposta.Status, resposta.Corpo)
	}
}

// sobeInstanciadasComSqs sobe as instancias com o consumidor de fila ligado.
//
// E separado de `sobeInstancias` porque o SQS e opcional no grafo: uma instancia sem
// `WAGER_SQS_ENDPOINT` sobe sem consumidor, e o teste de cruzamento precisa de tres
// instanas com consumidor para que a mensagem seja aplicada por uma delas.
func sobeInstanciadasComSqs(t *testing.T, fila *sqs.Cliente) []*Instancia {
	t.Helper()
	_ = fila

	instancias := sobeInstancias(t, map[string]string{
		"WAGER_SQS_ENDPOINT":         endpointDoSqs(),
		"WAGER_SQS_FILA_OPERACOES":   "wager-transactions.fifo",
		"WAGER_SQS_FILA_DEAD_LETTER": "wager-transactions-dlq.fifo",
		"WAGER_SQS_FILA_EVENTOS":     "wager-events.fifo",
	})
	return instancias
}
