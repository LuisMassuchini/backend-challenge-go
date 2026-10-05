//go:build integration

package persistencia

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// O papel de runtime e a ultima linha de defesa. Estas verificacoes rodam como
// `wager_app`, e nao como o dono do schema: uma garantia de privilegio testada
// com o superusuario nao prova nada.

// dsnRuntime e o DSN do papel de menor privilegio.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// comoRuntime abre uma conexao com o papel de runtime.
func comoRuntime(t *testing.T) *sql.DB {
	t.Helper()

	conexao, err := sql.Open("pgx", dsnRuntime)
	if err != nil {
		t.Fatalf("conexao como runtime: %v", err)
	}
	t.Cleanup(func() { conexao.Close() })

	if err := conexao.PingContext(context.Background()); err != nil {
		t.Fatalf("papel de runtime nao conecta: %v", err)
	}
	return conexao
}

// O papel de runtime nao apaga. E o que faz do append-only do ledger uma garantia
// do banco: nao ha caminho de escrita, nem por bug, que remova um lancamento.
func TestPapelDeRuntimeNaoApagaLancamento(t *testing.T) {
	dbtest.Limpa(t)
	dono := dbtest.NovaConexao(t)
	criaCarteira(t, dono, carteiraA, jogadorA, "BRL", 10000)

	transacao := uuid.NewString()
	if err := movimenta(t, dono, carteiraA, transacao, "DEBIT", 2500, 10000, 7500); err != nil {
		t.Fatalf("movimentacao: %v", err)
	}

	runtime := comoRuntime(t)

	var id string
	if err := dono.QueryRowContext(context.Background(),
		`SELECT id::text FROM wallet_ledger_entries WHERE transaction_id = $1`, transacao).Scan(&id); err != nil {
		t.Fatalf("leitura do lancamento: %v", err)
	}

	if _, err := runtime.ExecContext(context.Background(),
		`DELETE FROM wallet_ledger_entries WHERE id = $1`, id); err == nil {
		t.Fatal("papel de runtime conseguiu apagar lancamento do ledger")
	}

	// A linha continua la.
	var restante int
	if err := dono.QueryRowContext(context.Background(),
		`SELECT count(*) FROM wallet_ledger_entries WHERE id = $1`, id).Scan(&restante); err != nil {
		t.Fatalf("leitura pos-tentativa: %v", err)
	}
	if restante != 1 {
		t.Error("lancamento sumiu do ledger")
	}
}

// O papel de runtime nao altera o ledger. O trigger recusaria, e o privilegio
// recusa antes: as duas barreiras sao independentes e nenhuma delas depende da
// outra.
func TestPapelDeRuntimeNaoAlteraLancamento(t *testing.T) {
	dbtest.Limpa(t)
	dono := dbtest.NovaConexao(t)
	criaCarteira(t, dono, carteiraA, jogadorA, "BRL", 10000)

	transacao := uuid.NewString()
	if err := movimenta(t, dono, carteiraA, transacao, "DEBIT", 2500, 10000, 7500); err != nil {
		t.Fatalf("movimentacao: %v", err)
	}

	runtime := comoRuntime(t)

	var id string
	if err := dono.QueryRowContext(context.Background(),
		`SELECT id::text FROM wallet_ledger_entries WHERE transaction_id = $1`, transacao).Scan(&id); err != nil {
		t.Fatalf("leitura do lancamento: %v", err)
	}

	if _, err := runtime.ExecContext(context.Background(),
		`UPDATE wallet_ledger_entries SET money_amount = 1 WHERE id = $1`, id); err == nil {
		t.Fatal("papel de runtime conseguiu alterar lancamento do ledger")
	}
}

// O papel de runtime nao cria objeto. Sem CREATE no schema, um `DROP TABLE` de
// teste ou um `CREATE INDEX` improvisado nao entra.
func TestPapelDeRuntimeNaoCriaObjeto(t *testing.T) {
	runtime := comoRuntime(t)

	if _, err := runtime.ExecContext(context.Background(),
		`CREATE TABLE tentativa (id int)`); err == nil {
		t.Fatal("papel de runtime conseguiu criar tabela")
	}

	if _, err := runtime.ExecContext(context.Background(),
		`ALTER TABLE wallets ADD COLUMN tentativa int`); err == nil {
		t.Fatal("papel de runtime conseguiu alterar o schema")
	}

	if _, err := runtime.ExecContext(context.Background(),
		`DROP TABLE wallets`); err == nil {
		t.Fatal("papel de runtime conseguiu derrubar tabela")
	}
}

// O papel de runtime le e escreve o necessario. Se ele nao conseguisse abrir uma
// transacao nem inserir, as garantias acima seriam verdadeiras por vacuuo.
func TestPapelDeRuntimeLeEEscreve(t *testing.T) {
	dbtest.Limpa(t)
	dono := dbtest.NovaConexao(t)
	criaCarteira(t, dono, carteiraA, jogadorA, "BRL", 10000)
	runtime := comoRuntime(t)

	instante := agora()
	tx, err := runtime.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback de teste ja encerrado e esperado

	if _, err := tx.ExecContext(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, money_amount, currency,
			balance_before, balance_after, created_at
		)
		VALUES ($1, $2, $3, 'DEBIT', 2500, 'BRL', 10000, 7500, $4)
	`, uuid.NewString(), carteiraA, uuid.NewString(), instante); err != nil {
		t.Fatalf("insert como runtime: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE wallets SET balance = 7500, version = version + 1, updated_at = $2 WHERE id = $1`,
		carteiraA, instante); err != nil {
		t.Fatalf("update como runtime: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit como runtime: %v", err)
	}

	var saldo int64
	if err := runtime.QueryRowContext(context.Background(),
		`SELECT balance FROM wallets WHERE id = $1`, carteiraA).Scan(&saldo); err != nil {
		t.Fatalf("leitura como runtime: %v", err)
	}
	if saldo != 7500 {
		t.Errorf("saldo e %d, esperado 7500", saldo)
	}
}

// O limite de sessao do papel de runtime existe para que contencao vire erro e nao
// espera. O teste segura um lock e verifica que a outra sessao falha em vez de
// esperar por mais que o limite.
func TestLockTimeoutDoPapelDeRuntimeFalhaRapido(t *testing.T) {
	dbtest.Limpa(t)
	dono := dbtest.NovaConexao(t)
	criaCarteira(t, dono, carteiraA, jogadorA, "BRL", 10000)

	// O dono segura o lock da carteira numa transacao que nao fecha.
	segurador, err := dono.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin do dono: %v", err)
	}
	defer segurador.Rollback() //nolint:errcheck // rollback de teste ja encerrado e esperado

	if _, err := segurador.ExecContext(context.Background(),
		`SELECT id FROM wallets WHERE id = $1 FOR UPDATE`, carteiraA); err != nil {
		t.Fatalf("lock do dono: %v", err)
	}

	runtime := comoRuntime(t)
	ctx, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelar()

	inicio := time.Now()
	_, err = runtime.ExecContext(ctx,
		`UPDATE wallets SET balance = 7500 WHERE id = $1`, carteiraA)
	decorrido := time.Since(inicio)

	if err == nil {
		t.Fatal("atualizacao conflitada passou: o lock nao segurou")
	}
	// O limite do papel e de 1s. Com folga de 5s, um lock_timeout ausente seria
	// lido como travamento.
	if decorrido > 5*time.Second {
		t.Errorf("a espera levou %v: o lock_timeout do papel nao foi aplicado", decorrido)
	}
	if !dbtest.ErroDeConstraint(err) {
		t.Logf("erro devolvido: %v", err)
	}
}
