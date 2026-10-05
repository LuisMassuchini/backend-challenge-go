//go:build integration

package pendenciasteste

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
)

// contexto devolve um contexto com prazo para o teste.
func contexto(t *testing.T) context.Context {
	t.Helper()

	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelar)
	return ctx
}

// agendamentoDe devolve a proxima tentativa e o numero de tentativas de uma pendencia.
func agendamentoDe(t *testing.T, externa string) (time.Time, int) {
	t.Helper()

	linhas := consultar(t, `
		SELECT next_retry_at, retry_count
		  FROM wager_transactions
		 WHERE external_transaction_id = $1`, externa)
	if len(linhas) != 1 {
		t.Fatalf("a pendencia %s nao foi encontrada: %d linhas", externa, len(linhas))
	}

	return linhas[0].instante, linhas[0].contagem
}

// politicaImediata e uma politica que agenda para ja.
//
// Existe para que o teste nao precise mover o relogio: o worker e o que se esta
// testando, e a espera nao faz parte disso.
func politicaImediata() app.Politica {
	return app.Politica{
		IntervaloInicial:   time.Nanosecond,
		IntervaloMaximo:    time.Nanosecond,
		MaximoDeTentativas: 10,
	}
}

// dsnDono e o papel de dono do schema.
const dsnDono = "postgres://wager:wager@localhost:5432/wager?sslmode=disable"

// consultar roda a consulta e devolve a primeira coluna de cada linha como tempo e
// inteiro.
//
// A assinatura e especifica porque as duas verificacoes que precisam dela -- o
// agendamento da pendencia e o numero de tentativas -- tem os mesmos dois tipos, e
// uma funcao generica devolveria linhas heterogeneas sem seguranca de tipo.
func consultar(t *testing.T, consulta string, args ...any) []resultado {
	t.Helper()

	db, err := sql.Open("pgx", dsnDono)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	linhas, err := db.QueryContext(context.Background(), consulta, args...)
	if err != nil {
		t.Fatalf("consulta: %v", err)
	}
	defer linhas.Close()

	var saida []resultado
	for linhas.Next() {
		var (
			instante time.Time
			contagem int
		)
		if err := linhas.Scan(&instante, &contagem); err != nil {
			t.Fatalf("varredura: %v", err)
		}
		saida = append(saida, resultado{instante: instante, contagem: contagem})
	}
	if err := linhas.Err(); err != nil {
		t.Fatalf("varredura: %v", err)
	}
	return saida
}

// resultado e uma linha do agendamento.
type resultado struct {
	// instante e a proxima tentativa agendada.
	instante time.Time

	// contagem e o numero de tentativas feitas.
	contagem int
}

// contarLancamentos devolve quantos lancamentos existem no ledger.
func contarLancamentos(t *testing.T) int {
	t.Helper()

	db, err := sql.Open("pgx", dsnDono)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	var total int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM wallet_ledger_entries`).Scan(&total); err != nil {
		t.Fatalf("contagem de lancamentos: %v", err)
	}
	return total
}
