//go:build integration

package casos

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// BET debita o valor, grava o lancamento e publica os dois eventos. E o caminho
// feliz que fixa a ordem esperada: transacao, lancamento, mudanca de saldo,
// eventos.
func TestBetDebitaOLancaEeventua(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)

	resposta, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 2500, ""))
	if err != nil {
		t.Fatalf("ProcessarOperacao: %v", err)
	}

	if resposta.Estado != wagering.EstadoProcessado {
		t.Errorf("estado e %q, esperado %q", resposta.Estado, wagering.EstadoProcessado)
	}
	if resposta.Saldo.Decimal() != "75.00" {
		t.Errorf("saldo devolvido e %s, esperado 75.00", resposta.Saldo)
	}
	if resposta.Replay {
		t.Error("primeira operacao foi marcada como replay")
	}

	// A abertura e o BET: dois lancamentos.
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 2 {
		t.Errorf("lancamentos e %d, esperado 2", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionProcessed'`); got != 2 {
		t.Errorf("WagerTransactionProcessed e %d, esperado 2: abertura e BET", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WalletBalanceChanged'`); got != 2 {
		t.Errorf("WalletBalanceChanged e %d, esperado 2: abertura e BET", got)
	}
	if !resposta.TransacaoID.Valida() {
		t.Error("resposta sem identificador de transacao")
	}
}

// BET sem saldo e recusa de regra, e nao erro. A transacao fica REJECTED com o
// codigo, o evento de rejeicao e gravado e o commit acontece: e o que o provedor
// consulta depois.
func TestBetSemSaldoERecusaComCodigoEConfirmaOCommit(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)

	resposta, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 999999, ""))
	if err != nil {
		t.Fatalf("a recusa de regra nao pode virar erro: %v", err)
	}

	if resposta.Estado != wagering.EstadoRejeitado {
		t.Errorf("estado e %q, esperado %q", resposta.Estado, wagering.EstadoRejeitado)
	}
	if resposta.CodigoFalha != wagering.CodigoFalhaSemSaldo {
		t.Errorf("codigo e %q, esperado %q", resposta.CodigoFalha, wagering.CodigoFalhaSemSaldo)
	}
	if resposta.Saldo.Decimal() != "100.00" {
		t.Errorf("saldo devolvido e %s, esperado 100.00: recusa nao move dinheiro", resposta.Saldo)
	}

	// Nenhum lancamento novo, e o evento de rejeicao existe.
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1: so a abertura", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WalletBalanceChanged'`); got != 1 {
		t.Errorf("WalletBalanceChanged e %d, esperado 1: so a abertura", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionRejected'`); got != 1 {
		t.Errorf("WagerTransactionRejected e %d, esperado 1", got)
	}
	if got := contar(t, `SELECT count(*) FROM wager_transactions WHERE state = 'REJECTED'`); got != 1 {
		t.Errorf("transacoes REJECTED e %d, esperado 1", got)
	}
}

// LOSS e processada sem movimentar saldo e sem gerar WalletBalanceChanged. O
// enunciado e explicito sobre isso.
func TestLossNaoMoveSaldoNemPublicaMudancaDeSaldo(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)

	resposta, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-loss-1", "chave-loss-1", carteira, "LOSS", 0, ""))
	if err != nil {
		t.Fatalf("ProcessarOperacao: %v", err)
	}

	if resposta.Estado != wagering.EstadoProcessado {
		t.Errorf("estado e %q, esperado %q", resposta.Estado, wagering.EstadoProcessado)
	}
	if resposta.Saldo.Decimal() != "100.00" {
		t.Errorf("saldo e %s, esperado 100.00", resposta.Saldo)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1: LOSS nao lanca", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionProcessed'`); got != 2 {
		t.Errorf("WagerTransactionProcessed e %d, esperado 2", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WalletBalanceChanged'`); got != 1 {
		t.Errorf("WalletBalanceChanged e %d, esperado 1: so a abertura", got)
	}
}

// WIN credita o valor apostado.
func TestWinCreditaOValorApostado(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)
	if _, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 4000, "")); err != nil {
		t.Fatalf("BET: %v", err)
	}

	resposta, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-win-1", "chave-win-1", carteira, "WIN", 4000, ""))
	if err != nil {
		t.Fatalf("WIN: %v", err)
	}
	if resposta.Saldo.Decimal() != "100.00" {
		t.Errorf("saldo e %s, esperado 100.00", resposta.Saldo)
	}
}

// ROLLBACK de um WIN devolve o dinheiro, e e a operacao que exige a referencia
// resolvida: e o caminho que quebra se a referencia nao chegar ao dominio.
func TestRollbackDeWinDesfazOCredito(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)
	if _, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 2500, "")); err != nil {
		t.Fatalf("BET: %v", err)
	}
	if _, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-win-1", "chave-win-1", carteira, "WIN", 2500, "")); err != nil {
		t.Fatalf("WIN: %v", err)
	}

	resposta, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-roll-1", "chave-roll-1", carteira, "ROLLBACK", 2500, "ext-win-1"))
	if err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
	if resposta.Estado != wagering.EstadoProcessado {
		t.Errorf("estado e %q, esperado %q", resposta.Estado, wagering.EstadoProcessado)
	}
	if resposta.Saldo.Decimal() != "75.00" {
		t.Errorf("saldo e %s, esperado 75.00: o credito do WIN foi desfeito", resposta.Saldo)
	}
}

// Reversao entregue antes da aposta e espera, nao recusa: a transacao fica
// PENDING_REFERENCE e o worker assume depois.
func TestReversaoAntesDaApostaFicaPendente(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)

	resposta, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-refund-1", "chave-refund-1", carteira, "REFUND", 2500, "ext-bet-inexistente"))
	if err != nil {
		t.Fatalf("ProcessarOperacao: %v", err)
	}

	if resposta.Estado != wagering.EstadoPendenteReferencia {
		t.Errorf("estado e %q, esperado %q", resposta.Estado, wagering.EstadoPendenteReferencia)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1: pendente nao move dinheiro", got)
	}
	if got := contar(t, `SELECT count(*) FROM wager_transactions WHERE state = 'PENDING_REFERENCE'`); got != 1 {
		t.Errorf("transacoes PENDING_REFERENCE e %d, esperado 1", got)
	}
}

// ROLLBACK que precisa debitar sem saldo tem codigo proprio. Confundir com
// BET_SEM_SALDO seria erro de resposta ao provedor.
func TestRollbackSemSaldoRecusaComCodigoProprio(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)
	if _, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 9000, "")); err != nil {
		t.Fatalf("BET: %v", err)
	}
	if _, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-win-1", "chave-win-1", carteira, "WIN", 9000, "")); err != nil {
		t.Fatalf("WIN: %v", err)
	}
	// Outra aposta consome o saldo. Sem ela o ROLLBACK teria fundo: o WIN creditou
	// 90.00 e o debito do ROLLBACK seria de 90.00 sobre 100.00.
	if _, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-bet-2", "chave-bet-2", carteira, "BET", 8000, "")); err != nil {
		t.Fatalf("segundo BET: %v", err)
	}

	resposta, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-roll-1", "chave-roll-1", carteira, "ROLLBACK", 9000, "ext-win-1"))
	if err != nil {
		t.Fatalf("a recusa de regra nao pode virar erro: %v", err)
	}
	if resposta.CodigoFalha != wagering.CodigoFalhaReverSaoSemSaldo {
		t.Errorf("codigo e %q, esperado %q", resposta.CodigoFalha, wagering.CodigoFalhaReverSaoSemSaldo)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 4 {
		t.Errorf("lancamentos e %d, esperado 4: abertura, dois BET e WIN", got)
	}
}

// A mesma chave recebida duas vezes devolve o resultado persistido e nao move
// dinheiro de novo. E a garantia eliminatoria do enunciado.
func TestReenvioDaMesmaChaveNaoMoveDinheiro(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)
	requisicao := requisicaoDe(t, "provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 2500, "")

	primeira, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicao)
	if err != nil {
		t.Fatalf("primeira: %v", err)
	}

	segunda, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicao)
	if err != nil {
		t.Fatalf("o reenvio nao pode ser erro: %v", err)
	}

	if !segunda.Replay {
		t.Error("o reenvio nao foi marcado como replay")
	}
	if segunda.TransacaoID != primeira.TransacaoID {
		t.Errorf("o reenvio criou outra transacao: %s e %s", segunda.TransacaoID, primeira.TransacaoID)
	}
	if segunda.Saldo.Decimal() != "75.00" {
		t.Errorf("saldo do replay e %s, esperado 75.00: o resultado persistido, nao o saldo atual", segunda.Saldo)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 2 {
		t.Errorf("lancamentos e %d, esperado 2: o reenvio nao lanca", got)
	}
	if got := contar(t, `SELECT count(*) FROM wager_transactions`); got != 2 {
		t.Errorf("transacoes e %d, esperado 2: abertura e BET", got)
	}
}

// Mesma chave com conteudo diferente e conflito, nao replay. Sem isso, um cliente
// que muda o valor e reenvia com a chave antiga receberia o resultado antigo e
// acharia que a operacao nova passou.
func TestMesmaChaveComConteudoDiferenteEConflito(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)
	if _, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 2500, "")); err != nil {
		t.Fatalf("primeira: %v", err)
	}

	// Mesma chave, mesmo identificador externo, valor diferente.
	conflito := requisicaoDe(t, "provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 9900, "")
	_, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), conflito)
	if !errors.Is(err, app.ErrConflitoDeChave) {
		t.Fatalf("erro e %v, esperado conflito de chave", err)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 2 {
		t.Errorf("lancamentos e %d, esperado 2: o conflito nao move dinheiro", got)
	}
}

// Um provedor nao escreve em nome de outro. E a checagem que impede que o token
// valido do provider-a registre operacao com providerId = provider-b.
func TestProvedorDivergenteDoTokenERecusado(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)

	_, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoDe(t,
		"provider-b", "ext-bet-1", "chave-bet-1", carteira, "BET", 2500, ""))
	if !errors.Is(err, app.ErrProvedorDivergente) {
		t.Fatalf("erro e %v, esperado divergencia de provedor", err)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1: nada foi movimentado", got)
	}
}

// Cliente interno nao opera apostas: os escopos sao separados por construcao.
func TestOperacaoExigeProvedorComEscopoDeOperacoes(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	carteira := abrirComSaldo(t, s, 10000)

	if _, err := app.ProcessarOperacao(ctx, s, atorInterno(), requisicaoDe(t,
		"provider-a", "ext-bet-1", "chave-bet-1", carteira, "BET", 2500, "")); !errors.Is(err, app.ErrNaoAutorizado) {
		t.Errorf("erro e %v, esperado nao autorizado para o cliente interno", err)
	}

	semEscopo := atorProvedor("provider-a")
	semEscopo.Escopos = nil
	if _, err := app.ProcessarOperacao(ctx, s, semEscopo, requisicaoDe(t,
		"provider-a", "ext-bet-2", "chave-bet-2", carteira, "BET", 2500, "")); !errors.Is(err, app.ErrNaoAutorizado) {
		t.Errorf("erro e %v, esperado nao autorizado sem escopo", err)
	}
}

// Atomicidade: se a operacao falha, nao sobra transacao nem lancamento. E o que
// distingue uma operacao de um efeito parcial.
func TestOperacaoQueFalhaNaoDeixaNemTransacaoNemLancamento(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	// A chave estrangeira da carteira e verificada na insercao da transacao, antes da
	// leitura com lock. O que importa aqui e o efeito: a operacao falhou e nada
	// sobrou.
	inexistente := idValido(t, uuid.NewString())
	if _, err := app.ProcessarOperacao(ctx, s, atorProvedor("provider-a"), requisicaoBruta(t,
		"provider-a", "ext-bet-1", "chave-bet-1", inexistente, idValido(t, uuid.NewString()),
		"BET", 2500, "")); err == nil {
		t.Fatal("operacao em carteira inexistente foi aceita")
	}

	if got := contar(t, `SELECT count(*) FROM wager_transactions WHERE kind = 'BET'`); got != 0 {
		t.Errorf("transacoes BET e %d, esperado 0: a unidade desfez o registro", got)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 0 {
		t.Errorf("lancamentos e %d, esperado 0", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events`); got != 0 {
		t.Errorf("eventos e %d, esperado 0", got)
	}
}

// abrirComSaldo abre uma carteira nova com o saldo pedido e devolve o
// identificador, para que cada teste comece do zero sem depender do jogador fixo.
func abrirComSaldo(t *testing.T, s app.Servicos, centavos int64) app.RespostaAbertura {
	t.Helper()

	ctx := contexto(t)
	resposta, err := app.AbrirCarteira(ctx, s, atorInterno(), app.RequisicaoAbertura{
		Jogador:      idValido(t, uuid.NewString()),
		SaldoInicial: dinheiro(t, centavos),
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}
	return resposta
}

// requisicaoDe monta um comando de operacao com o fingerprint derivado do
// conteudo, para que o teste reproduza o que o transporte faz.
func requisicaoDe(
	t *testing.T,
	provedor, externo, chave string,
	carteira app.RespostaAbertura,
	tipo string,
	centavos int64,
	referencia string,
) app.RequisicaoOperacao {
	t.Helper()

	return requisicaoBruta(t, provedor, externo, chave,
		carteira.Carteira.ID(), carteira.Carteira.Jogador(), tipo, centavos, referencia)
}

// requisicaoBruta monta o comando a partir dos identificadores, para o teste que
// precisa de uma carteira que nao existe.
func requisicaoBruta(
	t *testing.T,
	provedor, externo, chave string,
	carteira, jogador wallet.Identificador,
	tipo string,
	centavos int64,
	referencia string,
) app.RequisicaoOperacao {
	t.Helper()

	conteudo := tipo + "|" + externo + "|" + itoa(centavos) + "|" + referencia
	return app.RequisicaoOperacao{
		Provedor:         wagering.Provedor(provedor),
		TransacaoExterna: wagering.Externo(externo),
		Chave:            wagering.Chave(chave),
		Fingerprint:      wagering.Hash("sha256:" + conteudo),
		Carteira:         carteira,
		Jogador:          jogador,
		Rodada:           wagering.Rodada("rodada-2026-09-08"),
		Jogo:             wagering.Jogo("jogo-1"),
		Tipo:             wagering.Tipo(tipo),
		Valor:            dinheiro(t, centavos),
		Referencia:       wagering.Referencia{Externa: wagering.Externo(referencia)},
		Correlacao:       uuid.NewString(),
	}
}
