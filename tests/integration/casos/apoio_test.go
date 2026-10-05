//go:build integration

package casos

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// dsnRuntime e o DSN do papel de menor privilegio, o mesmo que a aplicacao usa.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// dsnDono e o DSN do dono do schema.
const dsnDono = "postgres://wager:wager@localhost:5432/wager?sslmode=disable"

// atorInterno e o cliente de servico, com os escopos internos.
func atorInterno() app.Ator {
	return app.Ator{
		Cliente: "wager-service",
		Escopos: []string{app.EscopoAberturaCarteira, app.EscopoReconciliacao},
	}
}

// atorProvedor e um provedor de jogos, com apenas o escopo de operacoes.
func atorProvedor(provedor string) app.Ator {
	return app.Ator{
		Cliente:  provedor,
		Provedor: provedor,
		Escopos:  []string{app.EscopoOperacoes},
	}
}

func contexto(t *testing.T) context.Context {
	t.Helper()
	ctx, cancelar := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancelar)
	return ctx
}

// relogioFixo e um relogio parado, para que o instante das operacoes seja
// deterministico e a ordem de insercao na outbox seja a ordem do teste.
type relogioFixo struct{ agora time.Time }

func (r relogioFixo) Agora() time.Time { return r.agora }

func servicosDeTeste(t *testing.T, relogio app.Relogio) app.Servicos {
	t.Helper()

	ctx := contexto(t)
	aberto, err := pg.AbrirPool(ctx, dsnRuntime, pg.Opcoes{MaxConexoes: 8})
	if err != nil {
		t.Fatalf("abertura do pool: %v", err)
	}
	t.Cleanup(aberto.Close)

	return app.Servicos{
		Unidade:    pg.NovaUnidade(aberto),
		Carteiras:  pg.NovaRepositorioCarteira(),
		Ledger:     pg.NovaRepositorioLedger(),
		Transacoes: pg.NovaRepositorioTransacoes(),
		Inbox:      pg.NovaRepositorioInbox(),
		Outbox:     pg.NovaRepositorioOutbox(),
		Relogio:    relogio,
		Correlacao: func() string { return uuid.NewString() },
	}
}

func agoraTeste() time.Time {
	return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
}

func dinheiro(t *testing.T, centavos int64) money.Money {
	t.Helper()
	m, err := money.Parse(formatar(centavos), money.CurrencyBRL)
	if err != nil {
		t.Fatalf("money.Parse: %v", err)
	}
	return m
}

func formatar(centavos int64) string {
	negativo := centavos < 0
	m := centavos
	if negativo {
		m = -m
	}
	base := itoa(m/100) + "." + doisDigitos(m%100)
	if negativo {
		return "-" + base
	}
	return base
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	digitos := ""
	for v > 0 {
		digitos = string(rune('0'+v%10)) + digitos
		v /= 10
	}
	return digitos
}

func doisDigitos(v int64) string {
	if v < 10 {
		return "0" + itoa(v)
	}
	return itoa(v)
}

func idValido(t *testing.T, texto string) wallet.Identificador {
	t.Helper()
	id, err := wallet.IdentificadorDe(texto)
	if err != nil {
		t.Fatalf("IdentificadorDe(%q): %v", texto, err)
	}
	return id
}

// contar devolve quantas linhas a consulta encontra, para as verificacoes de
// atomicidade.
func contar(t *testing.T, consulta string, args ...any) int {
	t.Helper()

	db, err := sql.Open("pgx", dsnDono)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	var total int
	if err := db.QueryRowContext(context.Background(), consulta, args...).Scan(&total); err != nil {
		t.Fatalf("contagem %q: %v", consulta, err)
	}
	return total
}
