//go:build integration

package e2e

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// enviosDaAposta e o numero de envios do enunciado.
//
// Cinquenta e o que torna a corrida provavel: com tres envios a disputa quase nunca
// acontece, e um teste que passa por sorte nao prova nada. Com cinquenta a chance de
// as tres instancias entrarem na mesma transacao ao mesmo tempo e alta, e e o
// comentario da E8 registrou que dois creditos de 80 sobre 100 precisam chegar a ler o
// mesmo saldo para o lock ser testado.
const enviosDaAposta = 50

// resultadoDeEnvio e o desfecho de uma requisicao de aposta.
//
// E um tipo com nome e nao um par de retornos porque o teste precisa de tres
// informacoes por envio -- status, se foi replay, e o saldo devolvido -- e tres
// valores posicionais se confundem com frequencia em um cenario de cinquenta goroutines.
type resultadoDeEnvio struct {
	// status e o codigo HTTP.
	status int
	// replay informa se o resultado veio do banco.
	replay bool
	// saldo e o saldo devolvido, em centavos. Zero quando a resposta nao trouxe.
	saldo int64
	// corpo e o JSON bruto, para o diagnostico de falha.
	corpo map[string]any
}

// A mesma aposta enviada cinquenta vezes em paralelo produz um unico debito.
//
// Este e o primeiro cenario do enunciado e o que prova a idempotencia em processo
// real. As tres instancias sao independentes: pool proprio, memoria propria, e o
// `FOR UPDATE` do PostgreSQL como unico arbitro. Se a idempotencia dependesse de
// memoria de processo, tres instancias teriam tres respostas diferentes e o ledger
// teria tres debitos.
//
// A chave de idempotencia e a mesma nos cinquenta envios e o identificador externo tambem.
// E o enunciado que diz "recebimento repetido da mesma operacao", e a garantia e a chave:
// a mesma chave com o mesmo conteudo devolve o resultado persistido, e nao move dinheiro.
func TestApostaUnicaSobCinquentaEnviosEmTresInstancias(t *testing.T) {
	limparBase(t)
	instancias := sobeInstancias(t, nil)

	// Os dois tokens sao buscados separados porque os escopos sao separados no IdP: o
	// provedor tem `wager:operacoes` e nunca tem `wager:carteira:abertura`, e o cliente
	// de servico e o inverso. Abrir carteira com o token do provedor daria 403 -- que e
	// a separacao funcionando, e nao um defeito de concorrencia.
	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	carteira := abrirCarteira(t, tokenInterno, instancias, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	chave := "provider-a:" + t.Name()
	externa := "transaction-" + t.Name()
	corpo := pedidoDeOperacao(carteira, jogador, externa, "BET", 2500)

	var (
		grupo      sync.WaitGroup
		protecao   sync.Mutex
		resultados = make([]resultadoDeEnvio, 0, enviosDaAposta)
	)

	for i := 0; i < enviosDaAposta; i++ {
		grupo.Add(1)
		// A instancia e escolhida por indice, e nao ao acaso: `i % 3` garante que as
		// tres recebem parte das requisicoes, e um teste que so mandasse para a
		// primeira mediria concorrencia dentro de um processo.
		go func(indice int) {
			defer grupo.Done()

			instant := instancias[indice%len(instancias)]

			// Sem `t.Fatal` aqui: um fatal em goroutine encerra apenas ela e a falha
			// apareceria como contagem errada. O erro vira resultado, e quem decide o
			// que fazer com ele e o teste, na thread principal.
			status, bruto := instant.chamarSemFatal("POST", "/wagering/transactions",
				tokenProvedor, chave, "corr-"+t.Name(), corpo)

			protecao.Lock()
			defer protecao.Unlock()
			resultados = append(resultados, interpretar(t, status, bruto))
		}(i)
	}
	grupo.Wait()

	// Todos os envios precisam ter sido atendidos. Um 500 ou uma conexao recusada
	// indicaria falha de infraestrutura, e um 409 indicaria que o sistema tratou
	// reentrega como conflito -- o que seria um defeito de idempotencia.
	var (
		conflitos int
		erros     int
		replays   int
		primeiro  resultadoDeEnvio
	)
	for indice, resultado := range resultados {
		switch {
		case resultado.status == http.StatusConflict:
			conflitos++
		case resultado.status >= http.StatusInternalServerError:
			erros++
		case resultado.status != http.StatusOK:
			t.Errorf("envio %d respondeu %d: %v", indice, resultado.status, resultado.corpo)
		}

		if resultado.replay {
			replays++
		}
		if indice == 0 {
			primeiro = resultado
		}
	}

	if conflitos != 0 {
		t.Errorf("%d envios receberam 409: reentrega foi tratada como conflito", conflitos)
	}
	if erros != 0 {
		t.Errorf("%d envios receberam 5xx", erros)
	}
	if len(resultados) != enviosDaAposta {
		t.Fatalf("foram atendidos %d de %d envios", len(resultados), enviosDaAposta)
	}

	// Pelo menos um envio tem que ser o original, e os outros 49 tem que ser replay.
	// Se os cinquenta fossem replay, a operacao nunca teria sido aplicada -- o que
	// significaria que a idempotencia reconheceu uma chave que nao existia.
	if replays >= enviosDaAposta {
		t.Errorf("todos os %d envios foram replay: nenhum foi o processamento original", replays)
	}
	if replays == 0 {
		t.Error("nenhum envio foi marcado como replay: a deteccao de reentrega nao funciona")
	}

	// O saldo devolvido e o do processamento original, em todos os envios. E o que o
	// enunciado exige: replay devolve o saldo observado no processamento original, e nao
	// o saldo atual. Se devolvesse o saldo de hoje, um reenvio depois de outra operacao
	// mostraria um numero que o provedor nunca viu.
	for indice, resultado := range resultados {
		if resultado.saldo != primeiro.saldo {
			t.Errorf("envio %d devolveu saldo %d, o original devolveu %d: "+
				"o replay esta devolvendo o saldo atual e nao o persistido",
				indice, resultado.saldo, primeiro.saldo)
		}
	}

	// O saldo da carteira caiu exatamente uma vez.
	const saldoEsperado = int64(10000 - 2500)
	if saldo := lerSaldo(t, tokenInterno, instancias[0], carteira); saldo != saldoEsperado {
		t.Errorf("saldo final e %d centavos, esperado %d", saldo, saldoEsperado)
	}

	// E o ledger tem um unico debito: a abertura e a aposta.
	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != 1 {
		t.Errorf("o ledger tem %d debitos, esperado 1", debitos)
	}
	if creditos != 1 {
		t.Errorf("o ledger tem %d creditos, esperado 1 (a abertura)", creditos)
	}

	// E a reconciliacao concorda com o saldo.
	conferir := reconciliar(t, tokenInterno, instancias[0], carteira)
	if consistente, ok := conferir["consistent"].(bool); !ok || !consistente {
		t.Errorf("a reconciliacao acusou divergencia: %v", conferir)
	}
}

// interpretar le a resposta e extrai o que o teste precisa.
//
// O saldo e convertido aqui e nao no teste, e o motivo e o mesmo de sempre: um valor
// monetario medido por dois parsers diferentes mede a diferenca entre eles.
func interpretar(t *testing.T, status int, bruto string) resultadoDeEnvio {
	t.Helper()

	resultado := resultadoDeEnvio{status: status}

	var corpo map[string]any
	if bruto != "" {
		if err := deserializar(bruto, &corpo); err != nil {
			resultado.corpo = map[string]any{"bruto": bruto}
			return resultado
		}
	}
	resultado.corpo = corpo

	if replay, ok := corpo["idempotentReplay"].(bool); ok {
		resultado.replay = replay
	}
	if saldo, ok := corpo["balance"].(map[string]any); ok {
		if valor, ok := saldo["amount"].(string); ok {
			resultado.saldo = centavosDe(t, valor)
		}
	}
	return resultado
}

// esperarSaldo utilitario de diagnostico: espera a carteira chegar a um saldo.
func esperarSaldo(t *testing.T, token string, instante *Instancia, carteira string, esperado int64) {
	t.Helper()

	esperarAte(t, 10*time.Second, "saldo da carteira", func() bool {
		return lerSaldo(t, token, instante, carteira) == esperado
	})
}
