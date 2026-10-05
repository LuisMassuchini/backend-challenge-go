//go:build integration

package casos

import (
	"context"
	"database/sql"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// A reconciliacao precisa detectar divergencia de verdade. Um teste que so confirma
// carteira sadia passaria com um reconciliador que devolve sempre "converge", e esse
// e o pior defeito possivel numa ferramenta de diagnostico: ela passa a dar
// seguranca justamente quando nao deve.
//
// A corrupcao e feita no banco pelo dono do schema, porque nenhuma combinacao de
// chamadas da aplicacao consegue produzir divergencia. E o proprio fato de o
// caminho normal nao conseguir ja diz o quanto o schema protege o dado.
func TestReconciliacaoDetectaSaldoDivergenteDoLedger(t *testing.T) {
	dbtest.Limpa(t)
	s := servicosDeTeste(t, relogioFixo{agora: agoraTeste()})
	ctx := contexto(t)

	abertura := abrirComSaldo(t, s, 10000)
	apostar(t, s, abertura, "provider-a", "ext-bet-1", "chave-bet-1", "BET", 2500)

	executarComoDono(t, "UPDATE wallets SET balance = balance + 1000 WHERE id = $1",
		abertura.Carteira.ID().UUID())

	resposta, err := app.Reconciliar(ctx, s, atorInterno(), app.RequisicaoReconciliacao{
		Carteira: abertura.Carteira.ID(),
	})
	if err != nil {
		t.Fatalf("Reconciliar: %v", err)
	}

	if !resposta.Divergente {
		t.Fatal("reconciliacao nao detectou o saldo adulterado")
	}
	if resposta.SaldoGravado.Decimal() != "85.00" {
		t.Errorf("saldo gravado e %s, esperado 85.00", resposta.SaldoGravado)
	}
	if resposta.SaldoDoLedger.Decimal() != "75.00" {
		t.Errorf("saldo do ledger e %s, esperado 75.00", resposta.SaldoDoLedger)
	}
	if resposta.Diferenca.Decimal() != "10.00" {
		t.Errorf("diferenca e %s, esperado 10.00", resposta.Diferenca)
	}
}

// executarComoDono roda SQL com o papel de dono, que e o unico que altera o schema e
// desliga trigger.
func executarComoDono(t *testing.T, consulta string, args ...any) {
	t.Helper()

	db, err := sql.Open("pgx", dsnDono)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(context.Background(), consulta, args...); err != nil {
		t.Fatalf("exec %q: %v", consulta, err)
	}
}
