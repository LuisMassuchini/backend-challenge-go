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

// O ledger e a prova de que o dinheiro se moveu. Estas tres garantias sao as que
// o enunciado exige do banco: append-only, coerencia do lancamento com o saldo, e
// coerencia entre o lancamento e a carteira.

// movimenta aplica um lancamento e a atualizacao da carteira na mesma transacao.
//
// Uma funcao que faz as duas coisas e o que permite testar a coerencia: um
// lancamento sem a atualizacao da carteira tem de ser recusado no commit, e so ha
// commit se as duas occurrem juntas.
func movimenta(t *testing.T, db *sql.DB, carteira, transacao, direcao string, valor, antes, depois int64) error {
	t.Helper()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback de teste ja encerrado e esperado

	if _, err := tx.ExecContext(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, money_amount, currency,
			balance_before, balance_after, created_at
		)
		VALUES ($1, $2, $3, $4, $5, 'BRL', $6, $7, $8)
	`, uuid.NewString(), carteira, transacao, direcao, valor, antes, depois, agora()); err != nil {
		return err
	}

	if _, err := tx.ExecContext(context.Background(), `
		UPDATE wallets SET balance = $2, version = version + 1, updated_at = $3 WHERE id = $1
	`, carteira, depois, agora()); err != nil {
		return err
	}

	return tx.Commit()
}

func TestLancamentoCoerenteAceito(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	if err := movimenta(t, conexao, carteiraA, uuid.NewString(), "DEBIT", 2500, 10000, 7500); err != nil {
		t.Fatalf("lancamento coerente recusado: %v", err)
	}
}

// balance_after diferente de balance_before +- money. Um CHECK nao pegaria este
// caso, porque ele precisa da aritmetica entre tres colunas com condicao -- que e
// exatamente o que o trigger faz.
func TestLancamentoComSaldoPosteriorErradoRecusado(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	casos := []struct {
		nome    string
		direcao string
		valor   int64
		antes   int64
		depois  int64
	}{
		{"credito com posterior menor", "CREDIT", 2500, 10000, 7500},
		{"credito com posterior a mais", "CREDIT", 2500, 10000, 12501},
		{"debito com posterior maior", "DEBIT", 2500, 10000, 12500},
		{"debito com posterior a menos", "DEBIT", 2500, 10000, 7499},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			tx, err := conexao.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer tx.Rollback() //nolint:errcheck // rollback de teste ja encerrado e esperado

			_, err = tx.ExecContext(context.Background(), `
				INSERT INTO wallet_ledger_entries (
					id, wallet_id, transaction_id, direction, money_amount, currency,
					balance_before, balance_after, created_at
				)
				VALUES ($1, $2, $3, $4, $5, 'BRL', $6, $7, $8)
			`, uuid.NewString(), carteiraA, uuid.NewString(), c.direcao, c.valor, c.antes, c.depois, agora())
			if err == nil {
				t.Fatal("banco aceitou lancamento incoerente")
			}
			if !dbtest.ErroDeConstraint(err) {
				t.Fatalf("devolveu %v, esperado violacao de constraint", err)
			}
		})
	}
}

// Append-only: nem corrigir nem apagar. Uma prova que pode ser reescrita nao e
// prova, e correcao financeira se faz com lancamento novo.
func TestLedgerAppendOnlyRecusaUpdateEDelete(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	transacao := uuid.NewString()
	if err := movimenta(t, conexao, carteiraA, transacao, "DEBIT", 2500, 10000, 7500); err != nil {
		t.Fatalf("movimentacao: %v", err)
	}

	// O id do lancamento e lido do banco, e nao do valor enviado: o que se testa
	// aqui e a recusa da alteracao, e nao a ausencia da linha.
	var idReal string
	if err := conexao.QueryRowContext(context.Background(),
		`SELECT id::text FROM wallet_ledger_entries WHERE transaction_id = $1`, transacao).Scan(&idReal); err != nil {
		t.Fatalf("leitura do lancamento: %v", err)
	}

	if _, err := conexao.ExecContext(context.Background(),
		`UPDATE wallet_ledger_entries SET money_amount = 1 WHERE id = $1`, idReal); err == nil {
		t.Error("banco aceitou UPDATE no ledger")
	} else if !dbtest.ErroDeConstraint(err) {
		t.Errorf("UPDATE devolveu %v, esperado violacao de constraint", err)
	}

	if _, err := conexao.ExecContext(context.Background(),
		`DELETE FROM wallet_ledger_entries WHERE id = $1`, idReal); err == nil {
		t.Error("banco aceitou DELETE no ledger")
	} else if !dbtest.ErroDeConstraint(err) {
		t.Errorf("DELETE devolveu %v, esperado violacao de constraint", err)
	}

	// A linha continua intacta depois das duas tentativas.
	var valor int64
	if err := conexao.QueryRowContext(context.Background(),
		`SELECT money_amount FROM wallet_ledger_entries WHERE id = $1`, idReal).Scan(&valor); err != nil {
		t.Fatalf("leitura pos-atualizacao: %v", err)
	}
	if valor != 2500 {
		t.Errorf("valor do lancamento e %d, esperado 2500: a alteracao nao pode ter pasado", valor)
	}
}

// Lancamento sem a atualizacao financeira correspondente. E o que o enunciado pede
// para o banco impedir: a movimentacao entra no ledger e o saldo nao acompanha.
//
// O trigger e DEFERRABLE, e por isso o erro aparece no commit e nao no INSERT -- a
// aplicacao tem liberdade de escrever nas duas ordens.
func TestLancamentoSemAtualizacaoDaCarteiraRecusadoNoCommit(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	tx, err := conexao.BeginTx(context.Background(), nil)
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
	`, uuid.NewString(), carteiraA, uuid.NewString(), agora()); err != nil {
		t.Fatalf("insert do lancamento: %v", err)
	}

	// Sem o UPDATE da carteira. O commit tem de falhar.
	if err := tx.Commit(); err == nil {
		t.Fatal("commit aceito com lancamento sem atualizacao da carteira")
	} else if !dbtest.ErroDeConstraint(err) {
		t.Fatalf("commit devolveu %v, esperado violacao de constraint", err)
	}

	// O saldo da carteira continua o de antes: a transacao inteira foi revertida.
	var saldo int64
	if err := conexao.QueryRowContext(context.Background(),
		`SELECT balance FROM wallets WHERE id = $1`, carteiraA).Scan(&saldo); err != nil {
		t.Fatalf("leitura do saldo: %v", err)
	}
	if saldo != 10000 {
		t.Errorf("saldo e %d, esperado 10000", saldo)
	}
}

// A ordem das duas escritas dentro da transacao e livre. O trigger e deferido
// justamente para isso: exigir uma ordem especifica transformaria a ordem de duas
// linhas em uma constraint de arquitetura.
func TestOrdemDeEscritaDentroDaTransacaoEhLivre(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	// Carteira primeiro, lancamento depois.
	tx, err := conexao.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE wallets SET balance = 7500, version = version + 1, updated_at = $2 WHERE id = $1`,
		carteiraA, agora()); err != nil {
		t.Fatalf("update da carteira: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, money_amount, currency,
			balance_before, balance_after, created_at
		)
		VALUES ($1, $2, $3, 'DEBIT', 2500, 'BRL', 10000, 7500, $4)
	`, uuid.NewString(), carteiraA, uuid.NewString(), agora()); err != nil {
		t.Fatalf("insert do lancamento: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("carteira antes do lancamento recusado: %v", err)
	}
}

// O lancamento tem de encadear com o anterior. Um lancamento com saldo anterior
// inventado faria a soma do ledger divergir do saldo sem que nenhuma linha
// parecesse errada: e o tipo de bug que so a reconciliacao pega, e a reconciliacao
// roda depois.
func TestLancamentoQueNaoEncadeiaComOAnteriorRecusado(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	if err := movimenta(t, conexao, carteiraA, uuid.NewString(), "DEBIT", 2500, 10000, 7500); err != nil {
		t.Fatalf("primeira movimentacao: %v", err)
	}

	// Segunda movimentacao com saldo anterior que nao e o posterior da primeira.
	tx, err := conexao.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback de teste ja encerrado e esperado

	if _, err := tx.ExecContext(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, money_amount, currency,
			balance_before, balance_after, created_at
		)
		VALUES ($1, $2, $3, 'DEBIT', 1000, 'BRL', 8000, 7000, $4)
	`, uuid.NewString(), carteiraA, uuid.NewString(), agora().Add(time.Minute)); err != nil {
		t.Fatalf("insert do segundo lancamento: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE wallets SET balance = 7000, version = version + 1, updated_at = $2 WHERE id = $1`,
		carteiraA, agora()); err != nil {
		t.Fatalf("update da carteira: %v", err)
	}

	if err := tx.Commit(); err == nil {
		t.Fatal("commit aceitou lancamento que nao encadeia com o anterior")
	} else if !dbtest.ErroDeConstraint(err) {
		t.Fatalf("commit devolveu %v, esperado violacao de constraint", err)
	}
}

// Tres movimentacoes em sequencia, cada uma encadeando na anterior. E o caminho
// feliz que a reconciliacao vai reconstruir, e por isso ele precisa passar.
func TestSequenciaDeLancamentosEncadeia(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	// A carteira nasce zerada e a abertura e um lancamento de credito. E assim que o
	// enunciado modela: a reconstrucao do ledger inclui a abertura, e por isso a
	// soma de creditos menos debitos tem de igualar o saldo armazenado.
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 0)
	if err := movimentaEm(t, conexao, carteiraA, uuid.NewString(), "CREDIT", 10000, 0, 10000, agora()); err != nil {
		t.Fatalf("abertura: %v", err)
	}

	passos := []struct {
		direcao string
		valor   int64
		antes   int64
		depois  int64
	}{
		{"DEBIT", 2500, 10000, 7500},
		{"CREDIT", 2500, 7500, 10000},
		{"DEBIT", 1000, 10000, 9000},
	}

	for i, p := range passos {
		// Os instantes comecam depois da abertura: com o mesmo created_at, a ordem do
		// trigger cai no id, que e um UUID aleatorio, e a ultima linha do ledger
		// deixaria de ser a ultima movimentacao.
		instante := agora().Add(time.Duration(i+1) * time.Minute)
		if err := movimentaEm(t, conexao, carteiraA, uuid.NewString(), p.direcao, p.valor, p.antes, p.depois, instante); err != nil {
			t.Fatalf("passo %d: %v", i, err)
		}
	}

	var (
		saldo       int64
		lancamentos int
		soma        int64
	)
	if err := conexao.QueryRowContext(context.Background(),
		`SELECT balance FROM wallets WHERE id = $1`, carteiraA).Scan(&saldo); err != nil {
		t.Fatalf("leitura do saldo: %v", err)
	}
	if err := conexao.QueryRowContext(context.Background(),
		`SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, carteiraA).Scan(&lancamentos); err != nil {
		t.Fatalf("contagem de lancamentos: %v", err)
	}
	// A reconciliacao do enunciado e esta conta: soma de creditos menos debitos
	// tem de reproduzir o saldo armazenado.
	if err := conexao.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN money_amount ELSE -money_amount END), 0)
		   FROM wallet_ledger_entries WHERE wallet_id = $1`, carteiraA).Scan(&soma); err != nil {
		t.Fatalf("soma do ledger: %v", err)
	}

	if lancamentos != 4 {
		t.Errorf("lancamentos e %d, esperado 4 (abertura mais tres)", lancamentos)
	}
	if soma != saldo {
		t.Errorf("soma do ledger %d difere do saldo %d", soma, saldo)
	}
	if saldo != 9000 {
		t.Errorf("saldo e %d, esperado 9000", saldo)
	}
}

// movimentaEm e movimenta com instante controlado, para que a ordem do ledger
// possa ser fixada no teste.
func movimentaEm(t *testing.T, db *sql.DB, carteira, transacao, direcao string, valor, antes, depois int64, instante time.Time) error {
	t.Helper()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback de teste ja encerrado e esperado

	if _, err := tx.ExecContext(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, money_amount, currency,
			balance_before, balance_after, created_at
		)
		VALUES ($1, $2, $3, $4, $5, 'BRL', $6, $7, $8)
	`, uuid.NewString(), carteira, transacao, direcao, valor, antes, depois, instante); err != nil {
		return err
	}
	if _, err := tx.ExecContext(context.Background(),
		`UPDATE wallets SET balance = $2, version = version + 1, updated_at = $3 WHERE id = $1`,
		carteira, depois, instante); err != nil {
		return err
	}
	return tx.Commit()
}
