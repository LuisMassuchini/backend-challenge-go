//go:build integration

// Package repositorios testa a persistencia contra o PostgreSQL real.
//
// Nenhum teste aqui usa mock. O que precisa ser provado -- lock por carteira,
// atualizacao condicional por versao, reversao de unidade transacional -- existe
// no comportamento do banco, e um teste com mock provaria que o mock obedece.
package repositorios

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// dsnRuntime e o DSN do papel de menor privilegio: e o mesmo que a aplicacao usa
// em execucao, e por isso que os testes rodam com os limites de sessao reais.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// dsnDono e o DSN do dono do schema, que e quem pode rodar migration.
const dsnDono = "postgres://wager:wager@localhost:5432/wager?sslmode=disable"

// contexto devolve um contexto com prazo.
//
// Todo acesso ao banco nos testes tem prazo. Sem ele, uma transacao esquecida por
// um teste que falhou antes de fechar trava os testes seguintes, e o sintoma
// aparece longe da causa.
func contexto(t *testing.T) context.Context {
	t.Helper()
	ctx, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancelar)
	return ctx
}

// abrirPool abre um pool para o DSN informado.
func abrirPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()

	aberto, err := pg.AbrirPool(contexto(t), dsn, pg.Opcoes{MaxConexoes: 8})
	if err != nil {
		t.Fatalf("abertura do pool: %v", err)
	}
	t.Cleanup(aberto.Close)

	return aberto
}

// abrirUnidade abre um pool e devolve a unidade de trabalho sobre ele.
func abrirUnidade(t *testing.T, dsn string) (*pgxpool.Pool, *pg.Unidade) {
	t.Helper()
	aberto := abrirPool(t, dsn)
	return aberto, pg.NovaUnidade(aberto)
}

// agoraTeste e um instante fixo.
//
// time.Now em teste de persistencia produz valor diferente a cada execucao, e um
// teste que so falha as vezes nao e teste.
func agoraTeste() time.Time {
	return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
}

// dinheiro devolve um Money a partir de centavos, que e a unidade em que o teste
// razona.
func dinheiro(t *testing.T, centavos int64, moeda money.Currency) money.Money {
	t.Helper()

	texto := fmt.Sprintf("%d.%02d", centavos/100, abs(centavos%100))
	if centavos < 0 {
		texto = fmt.Sprintf("-%d.%02d", abs(centavos/100), abs(centavos%100))
	}
	m, err := money.Parse(texto, moeda)
	if err != nil {
		t.Fatalf("money.Parse(%q, %q): %v", texto, moeda, err)
	}
	return m
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// idValido converte um texto em identificador, falhando o teste se nao for.
func idValido(t *testing.T, texto string) wallet.Identificador {
	t.Helper()

	id, err := wallet.IdentificadorDe(texto)
	if err != nil {
		t.Fatalf("IdentificadorDe(%q): %v", texto, err)
	}
	return id
}

// inserirCarteiraDireto grava a carteira com SQL, para partir de um estado
// conhecido sem passar pelo repositorio.
func inserirCarteiraDireto(t *testing.T, dsn string, c wallet.Carteira) {
	t.Helper()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.ID().String(), c.Jogador().String(), string(c.Saldo().Currency()),
		c.Saldo().Amount(), c.Versao(), c.CriadaEm(), c.AtualizadaEm(),
	); err != nil {
		t.Fatalf("insercao da carteira: %v", err)
	}
}
