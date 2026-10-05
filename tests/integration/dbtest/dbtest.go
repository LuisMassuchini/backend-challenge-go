//go:build integration

// Package dbtest e o apoio comum aos testes de integracao de persistencia.
//
// Os testes rodam contra o PostgreSQL do Compose, com o schema migrado de verdade.
// Nao ha mock e nao ha SQLite: o que precisa ser provado aqui -- lock, constraint,
// trigger, unicidade parcial -- existe no comportamento do PostgreSQL, e um teste
// contra um banco diferente estaria provando o banco diferente.
package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	pg "github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// dsnPadrao aponta para o Compose local. A variavel de ambiente existe para que o
// teste possa rodar contra um banco efemero em outro ambiente, e para que a URL
// nunca fique escrita em varios lugares do repositorio.
const dsnPadrao = "postgres://wager:wager@localhost:5432/wager?sslmode=disable"

var (
	conexao *sql.DB
	umaVez  sync.Once
	erro    error
)

// Compartilhada devolve a conexao com o schema migrado, aplicando as migrations na
// primeira chamada.
//
// A migracao acontece aqui e nao em um TestMain porque este arquivo e uma
// biblioteca de apoio: TestMain so vale no pacote de teste. Migrar na primeira
// chamada tambem e o que permite que o teste que precisa do schema vazio rode sem
// depender da ordem de execucao.
func Compartilhada(t *testing.T) *sql.DB {
	t.Helper()

	umaVez.Do(func() {
		dsn := dsnDeTeste()

		aberta, err := sql.Open("pgx", dsn)
		if err != nil {
			erro = fmt.Errorf("conexao: %w", err)
			return
		}

		ctx, cancelar := context.WithTimeout(context.Background(), time.Minute)
		defer cancelar()

		if err := aberta.PingContext(ctx); err != nil {
			aberta.Close()
			erro = fmt.Errorf("postgres indisponivel em %s: %w", dsn, err)
			return
		}
		if err := pg.NovoMigrator(aberta).Up(ctx); err != nil {
			aberta.Close()
			erro = fmt.Errorf("migrations: %w", err)
			return
		}

		conexao = aberta
	})

	if erro != nil {
		t.Fatalf("banco de teste indisponivel: %v", erro)
	}
	return conexao
}

// NovaConexao devolve uma conexao nova, exclusiva do teste.
//
// Uma conexao por teste e o que reproduz a condicao real: o enunciado exige tres
// processos independentes, cada um com as proprias conexoes e memoria. Um pool
// compartilhado entre testes esconderia disputa de lock.
func NovaConexao(t *testing.T) *sql.DB {
	t.Helper()

	nova, err := sql.Open("pgx", dsnDeTeste())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { nova.Close() })

	return nova
}

// Limpa remove todas as linhas das tabelas de negocio.
//
// TRUNCATE e nao DELETE porque o teste nao cares do que cada teste deixou, e um
// DELETE em cascata na ordem errada custa mais codigo sem ganhar nada. O CASCADE e
// explicito justamente para que a ordem de truncamento nao vire mais um motivo de
// o teste passar ou falhar por acaso.
func Limpa(t *testing.T) {
	t.Helper()

	_, err := Compartilhada(t).ExecContext(context.Background(), `
		TRUNCATE TABLE
			wallet_ledger_entries,
			wager_transactions,
			outbox_events,
			inbox_messages,
			wallets
		CASCADE
	`)
	if err != nil {
		t.Fatalf("Limpa: %v", err)
	}
}

// ErroDeConstraint informa se o erro do PostgreSQL e o de uma constraint.
//
// A verificacao e por codigo SQLSTATE e nao por substring de mensagem: a mensagem
// do PostgreSQL muda entre versoes, e um teste que casa com texto quebra na
// proxima versao do banco sem que nada tenha mudado no sistema.
func ErroDeConstraint(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "23505", // unique_violation
		"23514", // check_violation
		"23503", // foreign_key_violation
		"23502", // not_null_violation
		"40001", // serialization_failure
		"40P01": // deadlock_detected
		return true
	}
	return false
}

// dsnDeTeste devolve o DSN do banco de teste.
func dsnDeTeste() string {
	if dsn := os.Getenv("WAGER_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	return dsnPadrao
}
