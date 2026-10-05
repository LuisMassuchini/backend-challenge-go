//go:build integration

package casos

import (
	"errors"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// A reconciliacao de uma carteira sadia concorda com o ledger.
func TestReconciliacaoDeCarteiraSadiaNaoAchaDivergencia(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	abertura := abrirComSaldo(t, s, 10000)
	apostar(t, s, abertura, "provider-a", "ext-bet-1", "chave-bet-1", "BET", 2500)

	resposta, err := app.Reconciliar(ctx, s, atorInterno(), app.RequisicaoReconciliacao{
		Carteira: abertura.Carteira.ID(),
	})
	if err != nil {
		t.Fatalf("Reconciliar: %v", err)
	}

	if resposta.Divergente {
		t.Errorf("reconciliacao acusou divergencia em carteira sadia: diferenca %s", resposta.Diferenca)
	}
	if resposta.SaldoGravado.Decimal() != "75.00" {
		t.Errorf("saldo gravado e %s, esperado 75.00", resposta.SaldoGravado)
	}
	if resposta.SaldoDoLedger.Decimal() != "75.00" {
		t.Errorf("saldo do ledger e %s, esperado 75.00", resposta.SaldoDoLedger)
	}
	if resposta.Lancamentos != 2 {
		t.Errorf("lancamentos e %d, esperado 2: abertura e BET", resposta.Lancamentos)
	}
}

// Carteira sem lancamento nao e divergencia. Acusar erro aqui transformaria uma
// carteira recem-criada em falso positivo.
func TestReconciliacaoSemLancamentoNaoAcusaDivergencia(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	abertura := abrirComSaldo(t, s, 0)

	resposta, err := app.Reconciliar(ctx, s, atorInterno(), app.RequisicaoReconciliacao{
		Carteira: abertura.Carteira.ID(),
	})
	if err != nil {
		t.Fatalf("Reconciliar: %v", err)
	}
	if resposta.Divergente {
		t.Error("carteira sem lancamento foi marcada como divergente")
	}
	if resposta.Lancamentos != 0 {
		t.Errorf("lancamentos e %d, esperado 0", resposta.Lancamentos)
	}
}

// A reconciliacao e do cliente interno. Um provedor com escopo de operacoes nao
// alcanca o diagnostico.
func TestReconciliacaoExigeClienteInternoComEscopo(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	abertura := abrirComSaldo(t, s, 10000)
	requisicao := app.RequisicaoReconciliacao{Carteira: abertura.Carteira.ID()}

	if _, err := app.Reconciliar(ctx, s, atorProvedor("provider-a"), requisicao); !errors.Is(err, app.ErrNaoAutorizado) {
		t.Errorf("erro e %v, esperado nao autorizado para provedor", err)
	}

	semEscopo := atorInterno()
	semEscopo.Escopos = nil
	if _, err := app.Reconciliar(ctx, s, semEscopo, requisicao); !errors.Is(err, app.ErrNaoAutorizado) {
		t.Errorf("erro e %v, esperado nao autorizado sem escopo", err)
	}
}

// Apostar e um atalho para os caminhos felizes dos testes de reconciliacao.
func apostar(
	t *testing.T,
	s app.Servicos,
	abertura app.RespostaAbertura,
	provedor, externo, chave, tipo string,
	centavos int64,
) {
	t.Helper()

	if _, err := app.ProcessarOperacao(contexto(t), s, atorProvedor(provedor), requisicaoDe(t,
		provedor, externo, chave, abertura, tipo, centavos, "")); err != nil {
		t.Fatalf("ProcessarOperacao %s: %v", tipo, err)
	}
}
