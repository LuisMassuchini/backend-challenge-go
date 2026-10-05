//go:build integration

package casos

import (
	"errors"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

const jogadorA = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"

// A abertura com saldo positivo cria a carteira, o OPENING, o lancamento de
// credito e os dois eventos. E o que o enunciado pede: tudo no mesmo commit da
// carteira.
func TestAberturaComSaldoPositivoCriaTudoNoMesmoCommit(t *testing.T) {
	dbtest.Limpa(t)
	servicos := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	resposta, err := app.AbrirCarteira(ctx, servicos, atorInterno(), app.RequisicaoAbertura{
		Jogador:      idValido(t, jogadorA),
		SaldoInicial: dinheiro(t, 100000),
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}

	if !resposta.Carteira.ID().Valida() {
		t.Fatal("carteira sem id")
	}
	if resposta.Carteira.Saldo().Decimal() != "1000.00" {
		t.Errorf("saldo e %s, esperado 1000.00", resposta.Carteira.Saldo())
	}
	if resposta.Carteira.Versao() != 1 {
		t.Errorf("versao e %d, esperado 1: a carteira nasce com o saldo, e a criacao nao e mudanca", resposta.Carteira.Versao())
	}

	// A transacao de abertura existe, sem identidade externa.
	if got := contar(t, `SELECT count(*) FROM wager_transactions WHERE kind = 'OPENING'`); got != 1 {
		t.Errorf("aberturas e %d, esperado 1", got)
	}
	// O lancamento de credito existe.
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1", got)
	}
	// Os dois eventos de origem interna estao na outbox.
	if got := contar(t, `SELECT count(*) FROM outbox_events`); got != 2 {
		t.Errorf("eventos e %d, esperado 2", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionProcessed'`); got != 1 {
		t.Errorf("WagerTransactionProcessed e %d, esperado 1", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WalletBalanceChanged'`); got != 1 {
		t.Errorf("WalletBalanceChanged e %d, esperado 1", got)
	}
}

// Saldo zero cria a carteira e nada mais. O enunciado e explicito: zero nao cria
// OPENING, nem lancamento, nem evento.
func TestAberturaComSaldoZeroNaoCriaNemMovimentacaoNemEvento(t *testing.T) {
	dbtest.Limpa(t)
	servicos := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	resposta, err := app.AbrirCarteira(ctx, servicos, atorInterno(), app.RequisicaoAbertura{
		Jogador:      idValido(t, jogadorA),
		SaldoInicial: dinheiro(t, 0),
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}

	if !resposta.Carteira.Saldo().IsZero() {
		t.Errorf("saldo e %s, esperado zero", resposta.Carteira.Saldo())
	}
	if resposta.Carteira.Versao() != 1 {
		t.Errorf("versao e %d, esperado 1: sem mudanca de saldo nao ha versionamento", resposta.Carteira.Versao())
	}

	if got := contar(t, `SELECT count(*) FROM wager_transactions`); got != 0 {
		t.Errorf("transacoes e %d, esperado 0", got)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 0 {
		t.Errorf("lancamentos e %d, esperado 0", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events`); got != 0 {
		t.Errorf("eventos e %d, esperado 0", got)
	}
}

// A segunda carteira do mesmo jogador na mesma moeda e conflito. O enunciado trata
// como conflito, e o erro classificado chega do indice unico.
func TestSegundaAberturaDoMesmoJogadorDaConflito(t *testing.T) {
	dbtest.Limpa(t)
	servicos := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	requisicao := app.RequisicaoAbertura{
		Jogador:      idValido(t, jogadorA),
		SaldoInicial: dinheiro(t, 100000),
	}
	if _, err := app.AbrirCarteira(ctx, servicos, atorInterno(), requisicao); err != nil {
		t.Fatalf("primeira abertura: %v", err)
	}

	_, err := app.AbrirCarteira(ctx, servicos, atorInterno(), requisicao)
	if !errors.Is(err, pg.ErrConflitoDeChave) {
		t.Fatalf("devolveu %v, esperado ErrConflitoDeChave", err)
	}

	// Nenhum estado parcial sobrou da tentativa que falhou.
	if got := contar(t, `SELECT count(*) FROM wallets`); got != 1 {
		t.Errorf("carteiras e %d, esperado 1", got)
	}
	if got := contar(t, `SELECT count(*) FROM outbox_events`); got != 2 {
		t.Errorf("eventos e %d, esperado 2: a tentativa falhada nao deixou evento", got)
	}
}

// A atomicidade e o que importa: se a transacao de abertura falhasse no meio, a
// carteira nao poderia existir sem o lancamento que a justifica.
func TestFalhaNoMeioNaoDeixaCarteiraSemLancamento(t *testing.T) {
	dbtest.Limpa(t)
	servicos := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	// A carteira ja existe: a abertura vai falhar no indice unico de
	// (jogador, moeda), e nada do restante deve sobrar.
	if _, err := app.AbrirCarteira(ctx, servicos, atorInterno(), app.RequisicaoAbertura{
		Jogador:      idValido(t, jogadorA),
		SaldoInicial: dinheiro(t, 100000),
	}); err != nil {
		t.Fatalf("abertura inicial: %v", err)
	}

	_, err := app.AbrirCarteira(ctx, servicos, atorInterno(), app.RequisicaoAbertura{
		Jogador:      idValido(t, jogadorA),
		SaldoInicial: dinheiro(t, 50000),
	})
	if !errors.Is(err, pg.ErrConflitoDeChave) {
		t.Fatalf("devolveu %v, esperado ErrConflitoDeChave", err)
	}

	if got := contar(t, `SELECT count(*) FROM wallets WHERE balance = 50000`); got != 0 {
		t.Errorf("a tentativa falhou e deixou %d carteiras com o saldo novo", got)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1", got)
	}
}

// A abertura e operacao interna. Um provedor com escopo de operacoes nao abre
// carteira, e nem o escopo equivado salva: a distincao esta no ator.
func TestAberturaExigeClienteInterno(t *testing.T) {
	dbtest.Limpa(t)
	servicos := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	_, err := app.AbrirCarteira(ctx, servicos, atorProvedor("provider-a"), app.RequisicaoAbertura{
		Jogador:      idValido(t, jogadorA),
		SaldoInicial: dinheiro(t, 100000),
	})
	if !errors.Is(err, app.ErrNaoAutorizado) {
		t.Fatalf("devolveu %v, esperado ErrNaoAutorizado", err)
	}

	// Nem um ator interno sem o escopo abre carteira.
	semEscopo := app.Ator{Cliente: "wager-service"}
	if _, err := app.AbrirCarteira(ctx, servicos, semEscopo, app.RequisicaoAbertura{
		Jogador:      idValido(t, jogadorA),
		SaldoInicial: dinheiro(t, 100000),
	}); !errors.Is(err, app.ErrNaoAutorizado) {
		t.Fatalf("devolveu %v, esperado ErrNaoAutorizado", err)
	}
}

func TestAberturaRecusaSaldoInvalido(t *testing.T) {
	dbtest.Limpa(t)
	servicos := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	casos := map[string]app.RequisicaoAbertura{
		"jogador ausente": {SaldoInicial: dinheiro(t, 1000)},
		"saldo nao inicializado": {
			Jogador: idValido(t, jogadorA),
		},
	}

	for nome, requisicao := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := app.AbrirCarteira(ctx, servicos, atorInterno(), requisicao); !errors.Is(err, app.ErrRequisicaoInvalida) {
				t.Errorf("devolveu %v, esperado ErrRequisicaoInvalida", err)
			}
		})
	}
}

// A resposta traz a carteira com a versao que foi gravada, e nao a de entrada. E o
// que permite ao chamador saber o que esta no banco sem ler de novo.
func TestRespostaCarregaACarteiraGravada(t *testing.T) {
	dbtest.Limpa(t)
	servicos := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	abertura := agoraTeste()
	resposta, err := app.AbrirCarteira(ctx, servicos, atorInterno(), app.RequisicaoAbertura{
		Jogador:      idValido(t, jogadorA),
		SaldoInicial: dinheiro(t, 25000),
		Correlacao:   "corr-abertura",
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}

	if !resposta.Carteira.CriadaEm().Equal(abertura) {
		t.Errorf("criada em %v, esperado %v", resposta.Carteira.CriadaEm(), abertura)
	}
	if resposta.Replay {
		t.Error("a primeira abertura se diz replay")
	}

	// A correlacao informada chega nos eventos, que e o que amarra o fluxo.
	if got := contar(t, `SELECT count(*) FROM outbox_events WHERE correlation_id = 'corr-abertura'`); got != 2 {
		t.Errorf("eventos com a correlacao informada e %d, esperado 2", got)
	}
}
