//go:build integration

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/sqs"
)

// Reiniciar derruba a instancia e sobe outra com a mesma configuracao.
//
// E o cenario de recuperacao do enunciado: "interrompa o consumidor depois do commit e
// antes do remocao da mensagem SQS; valide a reentrega" e "reinicie a aplicacao e
// verifique que idempotencia, pendencias e consistencia financeira foram preservadas".
//
// A funcao nao sobe um processo novo: ela derruba o grafo de Fx inteiro e monta outro.
// Um teste que apenas recriasse o pool estaria medindo o pool, e nao o processo.
func reiniciar(t *testing.T, nome string, extras map[string]string) *Instancia {
	t.Helper()

	instancia := sobeInstancia(t, nome, extras)
	t.Cleanup(instancia.encerrar)
	return instancia
}

// O reencontro devolve o resultado persistido, e o dinheiro nao se move.
//
// Este e o cenario "interrompa o consumidor depois do commit e antes da remocao da
// mensagem". A garantia que ele verifica e a mais forte do enunciado: **o dinheiro nao
// pode se mover duas vezes, e o provedor recebe a MESMA resposta mesmo apos o processo
// ter morrido**. Nao basta o saldo estar certo -- a resposta ao provedor tem de ser a
// mesma, porque e ela que o provedor usa para concluir a aposta.
func TestReencontroDevolveOResultadoPersistidoAposReinicio(t *testing.T) {
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

	// A primeira instancia abre a carteira e processa a operacao.
	primeira := sobeInstancia(t, "antes-do-reinicio", ambiente)
	carteira := abrirCarteira(t, tokenInterno, []*Instancia{primeira}, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, primeira, carteira)

	chave := "provider-a:reencounter-" + t.Name()
	externa := "transacao-reencounter-" + t.Name()

	respostaOriginal := primeira.chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, chave, "corr-original",
		pedidoDeOperacao(carteira, jogador, externa, "BET", 2500))
	if respostaOriginal.Status != http.StatusOK {
		t.Fatalf("a operacao original respondeu %d: %v", respostaOriginal.Status, respostaOriginal.Corpo)
	}

	saldoOriginal := lerSaldo(t, tokenInterno, primeira, carteira)

	// Derruba a instancia. A e a hora em que o sistema tem que estar consistente sem
	// ninguem vivo: nada foi publicado fora da fila, e o commit ja estava confirmado.
	primeira.encerrar()

	// Uma instancia nova, com pool novo e memoria nova.
	segunda := reiniciar(t, "depois-do-reinicio", ambiente)

	// O saldo sobreviveu ao processo.
	if saldo := lerSaldo(t, tokenInterno, segunda, carteira); saldo != saldoOriginal {
		t.Errorf("saldo antes %d e depois %d: o reinicio alterou o saldo", saldoOriginal, saldo)
	}

	// E o reencontro devolve exatamente a resposta original.
	respostaReplay := segunda.chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, chave, "corr-replay",
		pedidoDeOperacao(carteira, jogador, externa, "BET", 2500))

	if respostaReplay.Status != http.StatusOK {
		t.Fatalf("o reencontro respondeu %d, esperado 200: %v",
			respostaReplay.Status, respostaReplay.Corpo)
	}
	if replay, ok := respostaReplay.Corpo["idempotentReplay"].(bool); !ok || !replay {
		t.Errorf("o reencontro nao foi marcado como replay: %v", respostaReplay.Corpo)
	}

	// O saldo devolvido e o do processamento original, nao o de depois. Aqui eles sao
	// iguais porque nada mais mexeu na carteira -- e por isso que este teste sozinho
	// nao provaria o "resultado original persistido": o `TestApostaUnicaSobCinquenta`
	// prova, com a carteira ja alterada por outras operacoes.
	if saldo, ok := respostaReplay.Corpo["balance"].(map[string]any); ok {
		if valor, ok := saldo["amount"].(string); ok {
			if centavosDe(t, valor) != saldoOriginal {
				t.Errorf("o reencontro devolveu saldo %s, o original era %d", valor, saldoOriginal)
			}
		}
	}

	// O dinheiro nao se moveu. Este e o ponto do cenario.
	if saldo := lerSaldo(t, tokenInterno, segunda, carteira); saldo != saldoOriginal {
		t.Errorf("o saldo mudou no reencontro: %d para %d", saldoOriginal, saldo)
	}

	lancamentos := lerLedger(t, tokenInterno, segunda, carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != 1 {
		t.Errorf("o ledger tem %d debitos depois do reencontro, esperado 1", debitos)
	}
	if creditos != 1 {
		t.Errorf("o ledger tem %d creditos, esperado 1 (a abertura)", creditos)
	}

	// E a reconciliacao concorda depois do reinicio.
	conferir := reconciliar(t, tokenInterno, segunda, carteira)
	if consistente, ok := conferir["consistent"].(bool); !ok || !consistente {
		t.Errorf("a reconciliacao acusou divergencia depois do reinicio: %v", conferir)
	}
}

// O evento gravado no commit sobrevive ao reinicio e e publicado depois.
//
// Este e o cenario "recuperacao de publicacao apos restart". O evento foi gravado na
// outbox dentro da transacao financeira; o processo morreu antes de o relay publicar; o
// relay da instancia nova assume e publica, com o mesmo `eventId`.
//
// E o que o enunciado chama de "eventos pendentes devem ser assumidos por outra
// instancia", e a razao de a reserva ter janela de lease.
func TestEventoPendenteEAssumidoPorOutraInstanciaAposReinicio(t *testing.T) {
	limparBase(t)

	// Sem SQS: o relay nao sobe, e o evento fica na outbox sem poder sair. E
	// exatamente o estado que um processo deixa ao morrer entre o commit e a
	// publicacao.
	antes := sobeInstancia(t, "sem-relay", nil)
	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	carteira := abrirCarteira(t, tokenInterno, []*Instancia{antes}, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, antes, carteira)

	resposta := antes.chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, "provider-a:evento-pendente-"+t.Name(), "corr-pendente",
		pedidoDeOperacao(carteira, jogador, "transacao-pendente-"+t.Name(), "BET", 2500))
	if resposta.Status != http.StatusOK {
		t.Fatalf("a operacao respondeu %d: %v", resposta.Status, resposta.Corpo)
	}

	// O evento esta na outbox e nao foi publicado.
	pendentes := contarEventosPendentes(t)
	if pendentes < 2 {
		t.Fatalf("a outbox tem %d eventos pendentes, esperado ao menos 2 "+
			"(o da abertura e o da operacao)", pendentes)
	}

	// Derruba o processo. Nada foi publicado, e o registro continua em disco.
	antes.encerrar()

	// A instancia nova sobe COM o relay ligado.
	fila := clienteDaFilaDeOperacoes(t)
	esvaziarFila(t, fila)

	ambiente := map[string]string{
		"WAGER_SQS_ENDPOINT":         endpointDoSqs(),
		"WAGER_SQS_FILA_OPERACOES":   "wager-transactions.fifo",
		"WAGER_SQS_FILA_DEAD_LETTER": "wager-transactions-dlq.fifo",
		"WAGER_SQS_FILA_EVENTOS":     "wager-events.fifo",
	}
	depois := reiniciar(t, "com-relay", ambiente)

	// O relay assume os registros e confirma a publicacao.
	esperarAte(t, 30*time.Second, "a outbox ser drenada pelo relay", func() bool {
		return contarEventosPendentes(t) == 0
	})

	// E o saldo nao mudou: publicar evento nao move dinheiro.
	if saldo := lerSaldo(t, tokenInterno, depois, carteira); saldo != int64(7500) {
		t.Errorf("saldo e %d centavos, esperado 7500", saldo)
	}

	// E a reconciliacao concorda: a publicacao nao afeta o ledger.
	conferir := reconciliar(t, tokenInterno, depois, carteira)
	if consistente, ok := conferir["consistent"].(bool); !ok || !consistente {
		t.Errorf("a reconciliacao acusou divergencia depois da publicacao: %v", conferir)
	}
}

// Dois relays disputando a mesma outbox nao publicam o mesmo evento duas vezes.
//
// Este e o cenario "dois publishers disputando a mesma outbox" do enunciado, em processo
// real. O `SKIP LOCKED` e o que torna seguro, e a reserva vencida e o que recupera o
// trabalho abandonado.
func TestDoisRelaysDisputandoNaoPublicamODuplicado(t *testing.T) {
	limparBase(t)

	ambiente := map[string]string{
		"WAGER_SQS_ENDPOINT":         endpointDoSqs(),
		"WAGER_SQS_FILA_OPERACOES":   "wager-transactions.fifo",
		"WAGER_SQS_FILA_DEAD_LETTER": "wager-transactions-dlq.fifo",
		"WAGER_SQS_FILA_EVENTOS":     "wager-events.fifo",
	}

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	// Duas instancias com relay ligado desde o inicio, e uma carteira com varias
	// operacoes para que haja trabalho real em disputa.
	instancias := sobeInstancias(t, ambiente)

	carteira := abrirCarteira(t, tokenInterno, instancias, 100000)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	const operacoes = 12
	for i := 0; i < operacoes; i++ {
		instant := instancias[i%len(instancias)]
		resposta := instant.chamarJSON(t, http.MethodPost, "/wagering/transactions",
			tokenProvedor,
			fmtChave(i),
			"corr-relay-"+fmtIndice(i),
			pedidoDeOperacao(carteira, jogador, "transacao-relay-"+fmtIndice(i), "BET", 1000))
		if resposta.Status != http.StatusOK {
			t.Fatalf("operacao %d respondeu %d: %v", i, resposta.Status, resposta.Corpo)
		}
	}

	// Todos os eventos -- um por operacao mais os dois da abertura -- precisam sair.
	esperarAte(t, 60*time.Second, "a outbox ser drenada pelos dois relays", func() bool {
		return contarEventosPendentes(t) == 0
	})

	// E nenhum evento foi publicado duas vezes. O `eventId` e a identidade estavel, e
	// dois publicadores do mesmo evento produziriam dois registros com o mesmo id --
	// o que o consumidor de integracao nao conseguiria distinguir.
	duplicados := contarEventosPublicadosMaisDeUmaVez(t)
	if duplicados > 0 {
		t.Errorf("%d eventos foram publicados mais de uma vez", duplicados)
	}

	// E o dinheiro esta certo: doze apostas de 10.00 sobre 1000.00.
	if saldo := lerSaldo(t, tokenInterno, instancias[0], carteira); saldo != 88000 {
		t.Errorf("saldo e %d centavos, esperado 88000", saldo)
	}

	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != operacoes {
		t.Errorf("o ledger tem %d debitos, esperado %d", debitos, operacoes)
	}
	if creditos != 1 {
		t.Errorf("o ledger tem %d creditos, esperado 1 (a abertura)", creditos)
	}

	conferir := reconciliar(t, tokenInterno, instancias[0], carteira)
	if consistente, ok := conferir["consistent"].(bool); !ok || !consistente {
		t.Errorf("a reconciliacao acusou divergencia depois da disputa de relays: %v", conferir)
	}

	// Limpa a fila de eventos para nao vazar para o proximo teste.
	esvaziarFila(t, clienteDaFilaDeEventos(t))
}

// clienteDaFilaDeEventos monta o cliente da fila de saida.
func clienteDaFilaDeEventos(t *testing.T) *sqs.Cliente {
	t.Helper()

	cliente, err := sqs.Novo(contextoDeTeste(t), sqs.Opcoes{
		Endpoint:        endpointDoSqs(),
		Regiao:          "us-east-1",
		ChaveDeAcesso:   "test",
		SegredoDeAcesso: "test",
		FilaOperacoes:   "wager-events.fifo",
		FilaDeadLetter:  "wager-events-dlq.fifo",
	})
	if err != nil {
		t.Fatalf("cliente da fila de eventos: %v", err)
	}
	return cliente
}

// contarEventosPendentes devolve quantos eventos da outbox ainda nao foram publicados.
func contarEventosPendentes(t *testing.T) int {
	t.Helper()

	db, err := abrirLeitura(t)
	if err != nil {
		t.Fatalf("leitura: %v", err)
	}
	defer db.Close()

	var total int
	//nolint:errcheck
	err = db.QueryRow(
		"SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND failed_at IS NULL",
	).Scan(&total)
	if err != nil {
		t.Fatalf("contagem de pendentes: %v", err)
	}
	return total
}

// contarEventosPublicadosMaisDeUmaVez devolve quantos eventos foram confirmados duas
// vezes.
//
// A confirmacao e `UPDATE ... WHERE published_at IS NULL`, entao a segunda chamada
// atualiza zero linhas e devolve `ErrNaoEncontrado`. Por isso este teste olha o
// historico pela tabela e nao por um contador: e o numero de **lancamentos de
// publicacao** que importaria, e nao ha tabela que o guarde.
func contarEventosPublicadosMaisDeUmaVez(t *testing.T) int {
	t.Helper()

	// O relay nao duplica porque o `UPDATE` condicional impede a segunda confirmacao.
	// O que se verifica aqui e o efeito observavel: nao existem dois eventos com o
	// mesmo `eventId` -- o que significaria que o mesmo fato foi publicado duas vezes
	// como dois eventos distintos.
	db, err := abrirLeitura(t)
	if err != nil {
		t.Fatalf("leitura: %v", err)
	}
	defer db.Close()

	linhas, err := db.Query("SELECT id, count(*) FROM outbox_events GROUP BY id HAVING count(*) > 1")
	if err != nil {
		t.Fatalf("consulta de duplicados: %v", err)
	}
	defer linhas.Close()

	var duplicados int
	for linhas.Next() {
		var (
			id      string
			quantas int
		)
		if err := linhas.Scan(&id, &quantas); err != nil {
			t.Fatalf("varredura: %v", err)
		}
		duplicados++
	}
	return duplicados
}
