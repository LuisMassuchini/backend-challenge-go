//go:build integration

// Package consumidorteste exercita o consumidor contra o LocalStack de verdade.
//
// Nao haBroker nem fila em memoria aqui. O que precisa ser provado -- FIFO com
// ordem por chave de particao, deduplicacao, visibility timeout, redrive e
// cartao morto -- existe no comportamento do SQS, e um substituto em memoria
// passaria justamente nos casos que importam.
package consumidorteste

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/consumidor"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/internal/sqs"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// consultar roda uma consulta com o papel de dono e devolve as colunas de texto.
func consultar(t *testing.T, consulta string, args ...any) []string {
	t.Helper()

	db, err := sql.Open("pgx", dsnDono)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	linhas, err := db.QueryContext(context.Background(), consulta, args...)
	if err != nil {
		t.Fatalf("consulta %q: %v", consulta, err)
	}
	defer linhas.Close()

	var saida []string
	for linhas.Next() {
		var valor string
		if err := linhas.Scan(&valor); err != nil {
			t.Fatalf("varredura: %v", err)
		}
		saida = append(saida, valor)
	}
	if err := linhas.Err(); err != nil {
		t.Fatalf("varredura: %v", err)
	}
	return saida
}

// dsnDono e o papel de dono do schema.
const dsnDono = "postgres://wager:wager@localhost:5432/wager?sslmode=disable"

// dsnRuntime e o papel de menor privilegio.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// ambiente e o consumidor com suas dependencias.
type ambiente struct {
	// fila e o cliente SQS.
	fila *sqs.Cliente

	// servicos sao os casos de uso.
	servicos app.Servicos

	// parar encerra o worker.
	parar func()
}

// novoAmbiente sobe o consumidor contra a fila real.
func novoAmbiente(t *testing.T) *ambiente {
	t.Helper()

	// Esvaziar a fila e o primeiro passo de cada teste. Sem isso, uma mensagem
	// deixada pelo teste anterior seria consumida aqui e o resultado passaria a
	// depender da ordem de execucao.
	limpar(t)

	pool, err := pg.AbrirPool(contexto(t), dsnRuntime, pg.Opcoes{MaxConexoes: 16})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	fila, err := sqs.Novo(contexto(t), sqs.Opcoes{
		Endpoint:        endpointDoSqs(),
		Regiao:          "us-east-1",
		ChaveDeAcesso:   "test",
		SegredoDeAcesso: "test",
		FilaOperacoes:   "wager-transactions.fifo",
		FilaDeadLetter:  "wager-transactions-dlq.fifo",
	})
	if err != nil {
		t.Fatalf("cliente da fila: %v", err)
	}

	servicos := app.Servicos{
		Unidade:    pg.NovaUnidade(pool),
		Carteiras:  pg.NovaRepositorioCarteira(),
		Ledger:     pg.NovaRepositorioLedger(),
		Transacoes: pg.NovaRepositorioTransacoes(),
		Inbox:      pg.NovaRepositorioInbox(),
		Outbox:     pg.NovaRepositorioOutbox(),
		Relogio:    relogio{},
		Correlacao: func() string { return uuid.NewString() },
	}

	worker := consumidor.Novo(consumidor.Dependencias{
		Fila:               fila,
		Servicos:           servicos,
		Lote:               10,
		Ocioso:             200 * time.Millisecond,
		RenovaVisibilidade: 5 * time.Second,
	})

	contexto, cancelar := context.WithCancel(context.Background())
	terminou := make(chan struct{})

	go func() {
		defer close(terminou)
		//nolint:errcheck
		worker.Rodar(contexto)
	}()

	a := &ambiente{fila: fila, servicos: servicos}
	a.parar = func() {
		cancelar()
		select {
		case <-terminou:
		case <-time.After(10 * time.Second):
			t.Error("o consumidor nao parou em dez segundos")
		}
	}
	t.Cleanup(a.parar)

	return a
}

// relogio devolve o instante real.
type relogio struct{}

func (relogio) Agora() time.Time { return time.Now().UTC() }

// endpointDoSqs devolve o endereco do LocalStack.
func endpointDoSqs() string {
	if valor := os.Getenv("WAGER_TEST_SQS_ENDPOINT"); valor != "" {
		return valor
	}
	return "http://localhost:4566"
}

// limpar esvazia as duas filas.
//
// A fila de operacoes e esvaziada com o consumidor parado: purgar com um consumidor
// vivo tiraria as mensagens da mao dele, e ele voltaria a receber depois.
func limpar(t *testing.T) {
	t.Helper()

	fila, err := sqs.Novo(contexto(t), sqs.Opcoes{
		Endpoint:        endpointDoSqs(),
		Regiao:          "us-east-1",
		ChaveDeAcesso:   "test",
		SegredoDeAcesso: "test",
		FilaOperacoes:   "wager-transactions.fifo",
		FilaDeadLetter:  "wager-transactions-dlq.fifo",
	})
	if err != nil {
		t.Fatalf("cliente da fila: %v", err)
	}

	for i := 0; i < 40; i++ {
		mensagens, err := fila.Receber(contexto(t), 10)
		if err != nil {
			t.Fatalf("recebimento na limpeza: %v", err)
		}
		if len(mensagens) == 0 {
			return
		}
		for _, mensagem := range mensagens {
			if err := fila.Concluir(contexto(t), mensagem.ReceiptHandle); err != nil {
				t.Fatalf("limpeza: %v", err)
			}
		}
	}
}

// abrirCarteira abre uma carteira pelo caso de uso.
func abrirCarteira(t *testing.T, servicos app.Servicos, centavos int64) wallet.Carteira {
	t.Helper()

	valor, err := money.Parse(fmt.Sprintf("%d.%02d", centavos/100, centavos%100), money.CurrencyBRL)
	if err != nil {
		t.Fatalf("money.Parse: %v", err)
	}

	resposta, err := app.AbrirCarteira(contexto(t), servicos, atorInterno(), app.RequisicaoAbertura{
		Jogador:      idDe(t, uuid.NewString()),
		SaldoInicial: valor,
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}
	return resposta.Carteira
}

// atorInterno e o cliente de servico.
func atorInterno() app.Ator {
	return app.Ator{
		Cliente: "wager-service",
		Escopos: []string{app.EscopoAberturaCarteira, app.EscopoReconciliacao},
	}
}

// idDe converte texto em identificador.
func idDe(t *testing.T, texto string) wallet.Identificador {
	t.Helper()

	id, err := wallet.IdentificadorDe(texto)
	if err != nil {
		t.Fatalf("IdentificadorDe(%q): %v", texto, err)
	}
	return id
}

// mensagemDeOperacao monta o corpo de uma mensagem.
func mensagemDeOperacao(carteira wallet.Carteira, jogador wallet.Identificador,
	externa, chave, tipo string, centavos int64, referencia string) string {

	corpo := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": externa,
		"playerId":              jogador.String(),
		"walletId":              carteira.ID().String(),
		"roundId":               "round-987",
		"gameId":                "fortune-chimp",
		"kind":                  tipo,
		"money": map[string]any{
			"amount":   fmt.Sprintf("%d.%02d", centavos/100, abs(centavos%100)),
			"currency": "BRL",
		},
		"idempotencyKey": chave,
	}
	if referencia != "" {
		corpo["referenceExternalTransactionId"] = referencia
	}

	bruto, err := json.Marshal(corpo)
	if err != nil {
		panic(err)
	}
	return string(bruto)
}

// publicar envia uma mensagem para a fila.
//
// O grupo de particao e a carteira: e o que garante que duas operacoes na mesma
// carteira sejam aplicadas em ordem, e o que impede duas instancias de trabalharem na
// mesma carteira ao mesmo tempo.
func publicar(t *testing.T, a *ambiente, corpo, grupo string) {
	t.Helper()

	chave := fmt.Sprintf("publicada-%s", uuid.NewString())
	if err := a.fila.Publicar(contexto(t), grupo, chave, 0, corpo); err != nil {
		t.Fatalf("publicar: %v", err)
	}
}

// esperarTransacao espera a transacao com o identificador externo chegar a algum
// estado terminal ou pendente.
func esperarTransacao(t *testing.T, servicos app.Servicos, externa string, em []wagering.Estado) {
	t.Helper()

	limite := time.Now().Add(30 * time.Second)
	for time.Now().Before(limite) {
		transacao, err := app.LerTransacaoDoProvedor(contexto(t), servicos, atorInterno(),
			wagering.Provedor("provider-a"), wagering.Externo(externa))
		if err == nil {
			for _, esperado := range em {
				if transacao.Estado() == esperado {
					return
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("a transacao %s nao chegou a um dos estados %v em 30s", externa, em)
}

// saldoDa devolve o saldo da carteira.
func saldoDa(t *testing.T, servicos app.Servicos, carteira wallet.Carteira) string {
	t.Helper()

	lida, err := app.LerCarteira(contexto(t), servicos, atorInterno(), carteira.ID())
	if err != nil {
		t.Fatalf("LerCarteira: %v", err)
	}
	return lida.Saldo().Decimal()
}

// ---------------------------------------------------------------------------
// O caminho feliz
// ---------------------------------------------------------------------------

// Uma aposta publicada na fila e aplicada, e o saldo muda.
func TestOperacaoNaFilaDesbitaOSaldo(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := abrirCarteira(t, a.servicos, 10000)
	publicar(t, a, mensagemDeOperacao(carteira, carteira.Jogador(),
		"transaction-1", "chave-1", "BET", 2500, ""), carteira.ID().String())

	esperarTransacao(t, a.servicos, "transaction-1",
		[]wagering.Estado{wagering.EstadoProcessado, wagering.EstadoRejeitado})

	if got := saldoDa(t, a.servicos, carteira); got != "75.00" {
		t.Errorf("saldo e %s, esperado 75.00", got)
	}
}

// Uma LOSS e processada sem mexer no saldo.
func TestLossNaFilaNaoMexeNoSaldo(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := abrirCarteira(t, a.servicos, 10000)
	publicar(t, a, mensagemDeOperacao(carteira, carteira.Jogador(),
		"transaction-loss", "chave-loss", "LOSS", 0, ""), carteira.ID().String())

	esperarTransacao(t, a.servicos, "transaction-loss",
		[]wagering.Estado{wagering.EstadoProcessado})

	if got := saldoDa(t, a.servicos, carteira); got != "100.00" {
		t.Errorf("saldo e %s, esperado 100.00", got)
	}
}

// ---------------------------------------------------------------------------
// Idempotencia pela fila
// ---------------------------------------------------------------------------

// A mesma chave publicada duas vezes move dinheiro uma vez.
//
// E a garantia eliminatoria: o produtor reenvia porque nao recebeu a resposta, e o
// consumidor nao pode aplicar de novo.
func TestChaveRepetidaMoveDinheiroUmaVez(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := abrirCarteira(t, a.servicos, 10000)
	corpo := mensagemDeOperacao(carteira, carteira.Jogador(),
		"transaction-dup", "chave-dup", "BET", 2500, "")

	// Deduplicacao por conteudo esta desligada na fila, entao as duas mensagens
	// chegam. A deduplicacao da fila e uma janela de cinco minutos e a chave pode
	// ser reenviada muito depois; o que impede a segunda aplicacao e a chave de
	// idempotencia do caso de uso.
	publicar(t, a, corpo, carteira.ID().String())
	publicar(t, a, corpo, carteira.ID().String())

	esperarTransacao(t, a.servicos, "transaction-dup",
		[]wagering.Estado{wagering.EstadoProcessado})

	// Espera suficiente para a segunda mensagem ser consumida.
	esperarFilaVazia(t, a)

	if got := saldoDa(t, a.servicos, carteira); got != "75.00" {
		t.Errorf("saldo e %s, esperado 75.00: a repeticao moveu dinheiro duas vezes", got)
	}
	if got := contarLancamentos(t); got != 2 {
		t.Errorf("lancamentos e %d, esperado 2: abertura e uma aposta", got)
	}
}

// Uma reversao publicada antes da aposta fica pendente, e a pendencia sobrevive a
// chegada da referencia.
//
// O que este teste fixa e a metade que cabe a E13: a mensagem e aceita, a transacao
// fica em PENDING_REFERENCE, o dinheiro nao se mexe e nada se perde. A retomada -- a
// busca pela referencia e a aplicacao do estorno -- e o worker de referencias
// pendentes, testado na etapa dele. Aqui a pendencia continua de pe depois de a
// aposta existir, e e isso que a durabilidade significa.
func TestReversaoAntesDaApostaFicaPendenteEContinuaAposAReferenciaChegar(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := abrirCarteira(t, a.servicos, 10000)

	// A reversao chega primeiro, e a referencia ainda nao existe.
	publicar(t, a, mensagemDeOperacao(carteira, carteira.Jogador(),
		"transaction-refund", "chave-refund", "REFUND", 2500, "transaction-bet"),
		carteira.ID().String())

	esperarTransacao(t, a.servicos, "transaction-refund",
		[]wagering.Estado{wagering.EstadoPendenteReferencia})

	if got := saldoDa(t, a.servicos, carteira); got != "100.00" {
		t.Errorf("saldo e %s, esperado 100.00: pendente nao move dinheiro", got)
	}

	// A aposta chega depois. Ela e aplicada normalmente, e a reversao continua
	// esperando: o consumidor nao adivinha que a referencia agora existe.
	publicar(t, a, mensagemDeOperacao(carteira, carteira.Jogador(),
		"transaction-bet", "chave-bet", "BET", 2500, ""),
		carteira.ID().String())

	esperarFilaVazia(t, a)
	esperarSaldo(t, a.servicos, carteira, "75.00")

	// A reversao continua pendente e sem codigo de recusa: "ainda nao chegou" nao e
	// "nao pode", e o provedor trata as duas situacoes de formas diferentes.
	pendente, err := app.LerTransacaoDoProvedor(contexto(t), a.servicos, atorInterno(),
		wagering.Provedor("provider-a"), wagering.Externo("transaction-refund"))
	if err != nil {
		t.Fatalf("ler a reversao: %v", err)
	}
	if pendente.Estado() != wagering.EstadoPendenteReferencia {
		t.Errorf("estado e %q, esperado %q", pendente.Estado(), wagering.EstadoPendenteReferencia)
	}
	if pendente.CodigoFalha().Presente() {
		t.Errorf("a pendencia tem codigo de falha %q: espera nao e recusa", pendente.CodigoFalha())
	}
}

// ---------------------------------------------------------------------------
// Mensagem invalida
// ---------------------------------------------------------------------------

// Uma mensagem sem chave de idempotencia e descartada, e nao fica voltando.
//
// Mensagem malformada nao melhora com repeticao: o produtor mandou algo que este
// servico nao entende, e reentregar seria repetir o erro ate o cartao morto sem
// chance de sucesso.
func TestMensagemSemChaveEDescartada(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := abrirCarteira(t, a.servicos, 10000)

	corpo := fmt.Sprintf(`{"providerId":"provider-a","externalTransactionId":"sem-chave",`+
		`"playerId":%q,"walletId":%q,"roundId":"r","gameId":"g","kind":"BET",`+
		`"money":{"amount":"25.00","currency":"BRL"}}`,
		carteira.Jogador().String(), carteira.ID().String())

	publicar(t, a, corpo, carteira.ID().String())

	// A fila esvazia: a mensagem foi apagada pelo consumidor, nao devolvida.
	esperarFilaVazia(t, a)

	if got := contarLancamentos(t); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1: so a abertura", got)
	}
}

// Uma mensagem que nao e JSON e descartada.
func TestMensagemNaoJsonEDescartada(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := abrirCarteira(t, a.servicos, 10000)
	publicar(t, a, "isto nao e json", carteira.ID().String())

	esperarFilaVazia(t, a)

	if got := contarLancamentos(t); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1", got)
	}
}

// ---------------------------------------------------------------------------
// Ordem e concorrencia
// ---------------------------------------------------------------------------

// Varias operacoes na mesma carteira sao aplicadas em ordem.
//
// A FIFO garante ordem por chave de particao, e a chave de particao e a carteira.
// Duas operacoes na mesma carteira sao relacionadas -- uma reversao depende da
// aposta, e aplica-las fora de ordem produz resultado que depende da corrida.
func TestOperacoesDaMesmaCarteiraSaoAplicadasEmOrdem(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := abrirCarteira(t, a.servicos, 100000)

	// A aposta, a recompra e a reversao, nesta ordem.
	publicar(t, a, mensagemDeOperacao(carteira, carteira.Jogador(),
		"ordem-bet", "chave-ordem-bet", "BET", 5000, ""), carteira.ID().String())
	publicar(t, a, mensagemDeOperacao(carteira, carteira.Jogador(),
		"ordem-win", "chave-ordem-win", "WIN", 5000, ""), carteira.ID().String())
	publicar(t, a, mensagemDeOperacao(carteira, carteira.Jogador(),
		"ordem-roll", "chave-ordem-roll", "ROLLBACK", 5000, "ordem-win"),
		carteira.ID().String())

	esperarFilaVazia(t, a)

	// 1000.00 menos a aposta, mais o premio, menos o premio desfeito.
	esperarSaldo(t, a.servicos, carteira, "950.00")

	// A ordem de insercao no banco prova a ordem de aplicacao.
	ordem := ordenacaoDasTransacoes(t)
	esperada := []string{"ordem-bet", "ordem-win", "ordem-roll"}
	if len(ordem) != len(esperada) {
		t.Fatalf("transacoes em ordem %v, esperado %v", ordem, esperada)
	}
	for i := range esperada {
		if ordem[i] != esperada[i] {
			t.Fatalf("ordem das transacoes e %v, esperado %v", ordem, esperada)
		}
	}
}

// Carteiras diferentes sao processadas em paralelo.
//
// A chave de particao e a carteira, e nao o provedor nem a fila inteira: com a fila
// inteira como grupo, uma unica mensagem por vez seria processada e o sistema
// perderia o paralelismo que o enunciado exige.
func TestCarteirasDiferentesSaoProcessadasEmParalelo(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	const carteiras = 5
	var espera sync.WaitGroup

	for i := 0; i < carteiras; i++ {
		nova := abrirCarteira(t, a.servicos, 10000)

		espera.Add(1)
		go func(c wallet.Carteira, indice int) {
			defer espera.Done()

			publicar(t, a, mensagemDeOperacao(c, c.Jogador(),
				fmt.Sprintf("paralelo-%d", indice), fmt.Sprintf("chave-paralelo-%d", indice),
				"BET", 2500, ""), c.ID().String())
		}(nova, i)
	}
	espera.Wait()

	for i := 0; i < carteiras; i++ {
		esperarTransacao(t, a.servicos, fmt.Sprintf("paralelo-%d", i),
			[]wagering.Estado{wagering.EstadoProcessado})
	}
}

// ---------------------------------------------------------------------------
// Deduplicacao da fila
// ---------------------------------------------------------------------------

// A deduplicacao da fila impede a mesma chave de deduplicacao entrar duas vezes em
// cinco minutos.
//
// E uma janela curta e que nao substitui a idempotencia do caso de uso: a operacao
// pode ser reenviada com a mesma chave muito depois, quando a janela ja passou.
func TestDeduplicacaoDaFilaBloqueiaAMesmaChaveDeDeduplicacao(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := abrirCarteira(t, a.servicos, 100000)
	corpo := mensagemDeOperacao(carteira, carteira.Jogador(),
		"dedup-1", "chave-dedup", "BET", 1000, "")

	deduplicacao := "dedup-" + uuid.NewString()

	if err := a.fila.Publicar(contexto(t), carteira.ID().String(), deduplicacao, 0, corpo); err != nil {
		t.Fatalf("primeira: %v", err)
	}
	if err := a.fila.Publicar(contexto(t), carteira.ID().String(), deduplicacao, 0, corpo); err != nil {
		t.Fatalf("segunda: %v", err)
	}

	// Uma unica aposta chega ao banco, mesmo que a deduplicacao da fila falhasse: a
	// chave de idempotencia do caso de uso e a garantia real.
	esperarTransacao(t, a.servicos, "dedup-1", []wagering.Estado{wagering.EstadoProcessado})
	esperarFilaVazia(t, a)

	if got := contarTransacoesBET(t); got != 1 {
		t.Errorf("transacoes BET e %d, esperado 1", got)
	}
}

// ---------------------------------------------------------------------------
// Metodos auxiliares de verificacao
// ---------------------------------------------------------------------------

// esperarFilaVazia espera a fila ficar sem mensagem visivel.
func esperarFilaVazia(t *testing.T, a *ambiente) {
	t.Helper()

	limite := time.Now().Add(30 * time.Second)
	for time.Now().Before(limite) {
		mensagens, err := a.fila.Receber(contexto(t), 10)
		if err != nil {
			t.Fatalf("recebimento: %v", err)
		}
		if len(mensagens) == 0 {
			return
		}
		for _, mensagem := range mensagens {
			//nolint:errcheck
			a.fila.Concluir(contexto(t), mensagem.ReceiptHandle)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("a fila nao esvaziou em 30s")
}

// esperarSaldo espera a carteira chegar ao saldo esperado.
func esperarSaldo(t *testing.T, servicos app.Servicos, carteira wallet.Carteira, esperado string) {
	t.Helper()

	limite := time.Now().Add(30 * time.Second)
	var atual string
	for time.Now().Before(limite) {
		atual = saldoDa(t, servicos, carteira)
		if atual == esperado {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("saldo ficou em %s, esperado %s", atual, esperado)
}

// ordenacaoDasTransacoes devolve os identificadores externos na ordem de criacao.
func ordenacaoDasTransacoes(t *testing.T) []string {
	t.Helper()

	linhas := consultar(t,
		`SELECT external_transaction_id FROM wager_transactions
		 WHERE external_transaction_id LIKE 'ordem-%' ORDER BY created_at, id`)

	externas := make([]string, 0, len(linhas))
	for _, linha := range linhas {
		externas = append(externas, linha)
	}
	return externas
}

// contarLancamentos devolve quantos lancamentos existem.
func contarLancamentos(t *testing.T) int {
	t.Helper()

	linhas := consultar(t, `SELECT id FROM wallet_ledger_entries`)
	return len(linhas)
}

// contarTransacoesBET devolve quantas transacoes BET existem.
func contarTransacoesBET(t *testing.T) int {
	t.Helper()

	linhas := consultar(t, `SELECT id FROM wager_transactions WHERE kind = 'BET'`)
	return len(linhas)
}

func contexto(t *testing.T) context.Context {
	t.Helper()

	ctx, cancelar := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancelar)
	return ctx
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
