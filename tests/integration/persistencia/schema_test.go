//go:build integration

// Package persistencia testa as constraints do schema contra o PostgreSQL real.
//
// Cada teste viola uma constraint de proposito e exige que o banco recuse. A
// inversao tambem e testada: o que o banco aceita tem de ser o que o dominio
// aceita, e o que o banco recusa tem de ser recusado com o codigo de constraint e
// nao com um erro qualquer.
package persistencia

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

const (
	carteiraA  = "0192f291-27dd-7d3f-8071-5f8685deef37"
	carteiraB  = "0192f291-27dd-7d3f-8071-5f8685deef38"
	jogadorA   = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	jogadorB   = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2"
	transacaoA = "0192f298-345e-7e38-af88-e43f851a819d"
	transacaoB = "0192f298-345e-7e38-af88-e43f851a819e"
)

func agora() time.Time { return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC) }

// criaCarteira insere uma carteira valida.
func criaCarteira(t *testing.T, db *sql.DB, id, jogador, moeda string, saldo int64) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, $5, $5)
	`, id, jogador, moeda, saldo, agora())
	if err != nil {
		t.Fatalf("criaCarteira %s: %v", id, err)
	}
}

// criaTransacao insere uma transacao externa valida. Os valores vazios viram NULL
// pelo proprio SQL, para que o teste possa montar um OPENING sem ter que escrever
// uma segunda instrucao.
func criaTransacao(t *testing.T, db *sql.DB, id, provedor, carteira, jogador, chave, externo, tipo string, valor int64) {
	t.Helper()
	nulo := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	estado := "PENDING"
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO wager_transactions (
			id, provider_id, external_transaction_id, idempotency_key, content_hash,
			wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
			state, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, 'hash', $5, $6, 'round-987', 'fortune-chimp', $7, $8, 'BRL',
			$9, $10, $10)
	`, id, provedor, externo, nulo(chave), carteira, jogador, tipo, valor, estado, agora())
	if err != nil {
		t.Fatalf("criaTransacao %s: %v", id, err)
	}
}

// TestSaldoNegativoRecusadoPeloBanco e a garantia final do sistema. O dominio ja
// recusa debito maior que o saldo; o banco recusa a linha, e e o banco que vale
// quando o dado vem de outro caminho.
func TestSaldoNegativoRecusadoPeloBanco(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)

	_, err := conexao.ExecContext(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', -1, 1, $3, $3)
	`, carteiraA, jogadorA, agora())
	if err == nil {
		t.Fatal("banco aceitou carteira com saldo negativo")
	}
	if !dbtest.ErroDeConstraint(err) {
		t.Fatalf("devolveu %v, esperado violacao de constraint", err)
	}
}

// (playerId, currency) identifica a carteira. Duas carteiras do mesmo jogador na
// mesma moeda sao conflito, e o enunciado trata a segunda abertura como conflito.
func TestUnicidadeDeJogadorEMoeda(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)

	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	_, err := conexao.ExecContext(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', 0, 1, $3, $3)
	`, carteiraB, jogadorA, agora())
	if err == nil {
		t.Fatal("banco aceitou segunda carteira do mesmo jogador na mesma moeda")
	}
	if !dbtest.ErroDeConstraint(err) {
		t.Fatalf("devolveu %v, esperado violacao de unicidade", err)
	}

	// A mesma jogadora em outra moeda e permitida: a chave do enunciado e o par.
	criaCarteira(t, conexao, carteiraB, jogadorA, "USD", 0)
}

// Duas operacoes com a mesma chave de idempotencia e o mesmo provedor. E a
// duplicidade que a idempotencia precisa impedir.
func TestUnicidadeDeChaveDeIdempotencia(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	criaTransacao(t, conexao, transacaoA, "provider-a", carteiraA, jogadorA, "provider-a:1", "1", "BET", 2500)

	_, err := conexao.ExecContext(context.Background(), `
		INSERT INTO wager_transactions (
			id, provider_id, external_transaction_id, idempotency_key, content_hash,
			wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
			state, created_at, updated_at
		)
		VALUES ($1, 'provider-a', '2', 'provider-a:1', 'hash', $2, $3, 'round-987',
			'fortune-chimp', 'BET', 2500, 'BRL', 'PENDING', $4, $4)
	`, transacaoB, carteiraA, jogadorA, agora())
	if err == nil {
		t.Fatal("banco aceitou a mesma chave de idempotencia duas vezes")
	}
	if !dbtest.ErroDeConstraint(err) {
		t.Fatalf("devolveu %v, esperado violacao de unicidade", err)
	}
}

// A mesma operacao financeira com outra chave e o caminho mais comum de
// duplicidade quando o cliente muda o esquema da chave. O segundo indice e o que
// pega.
func TestUnicidadeDeProvedorEIdExterno(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	criaTransacao(t, conexao, transacaoA, "provider-a", carteiraA, jogadorA, "chave-1", "transaction-123", "BET", 2500)

	_, err := conexao.ExecContext(context.Background(), `
		INSERT INTO wager_transactions (
			id, provider_id, external_transaction_id, idempotency_key, content_hash,
			wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
			state, created_at, updated_at
		)
		VALUES ($1, 'provider-a', 'transaction-123', 'chave-2', 'hash', $2, $3, 'round-987',
			'fortune-chimp', 'BET', 2500, 'BRL', 'PENDING', $4, $4)
	`, transacaoB, carteiraA, jogadorA, agora())
	if err == nil {
		t.Fatal("banco aceitou a mesma operacao externa com outra chave")
	}
	if !dbtest.ErroDeConstraint(err) {
		t.Fatalf("devolveu %v, esperado violacao de unicidade", err)
	}
}

// Uma carteira tem no maximo uma abertura. Sem isso, dois INSERT de OPENING
// criariam dois creditos iniciais para a mesma carteira, e a unicidade de
// (player_id, currency) nao pegaria porque a carteira ja existe.
func TestCreditoInicialDuplicadoRecusado(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 0)

	abertura := `
		INSERT INTO wager_transactions (
			id, provider_id, external_transaction_id, idempotency_key, content_hash,
			wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
			state, result_amount, result_currency, created_at, updated_at
		)
		VALUES ($1, NULL, NULL, NULL, NULL, $2, $3, NULL, NULL, 'OPENING', 10000, 'BRL',
			'PROCESSED', 10000, 'BRL', $4, $4)`

	if _, err := conexao.ExecContext(context.Background(), abertura, transacaoA, carteiraA, jogadorA, agora()); err != nil {
		t.Fatalf("primeira abertura: %v", err)
	}
	if _, err := conexao.ExecContext(context.Background(), abertura, transacaoB, carteiraA, jogadorA, agora()); err == nil {
		t.Fatal("banco aceitou segunda abertura para a mesma carteira")
	}
}

// OPENING nao tem identidade externa. O CHECK tem os dois lados: abertura nao tem
// provedor, e o que nao e abertura tem.
func TestAberturaComIdentidadeExternaRecusada(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 0)

	casos := map[string]func(*string){
		"provedor":   func(p *string) { *p = "provider-a" },
		"id externo": func(p *string) { *p = "transaction-1" },
		"chave":      func(p *string) { *p = "chave" },
		"hash":       func(p *string) { *p = "hash" },
		"rodada":     func(p *string) { *p = "round-987" },
		"jogo":       func(p *string) { *p = "fortune-chimp" },
		"referencia": func(p *string) { *p = "transaction-1" },
	}

	for nome, preencher := range casos {
		t.Run(nome, func(t *testing.T) {
			provedor := any(nil)
			externo := any(nil)
			chave := any(nil)
			hash := any(nil)
			rodada := any(nil)
			jogo := any(nil)
			referencia := any(nil)

			switch nome {
			case "provedor":
				provedor = "provider-a"
			case "id externo":
				externo = "transaction-1"
			case "chave":
				chave = "chave"
			case "hash":
				hash = "hash"
			case "rodada":
				rodada = "round-987"
			case "jogo":
				jogo = "fortune-chimp"
			case "referencia":
				referencia = "transaction-1"
			}
			_ = preencher

			_, err := conexao.ExecContext(context.Background(), `
				INSERT INTO wager_transactions (
					id, provider_id, external_transaction_id, idempotency_key, content_hash,
					wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
					reference_external_id, state, result_amount, result_currency, created_at, updated_at
				)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'OPENING', 10000, 'BRL',
					$10, 'PROCESSED', 10000, 'BRL', $11, $11)
			`, transacaoB, provedor, externo, chave, hash, carteiraA, jogadorA, rodada, jogo, referencia, agora())
			if err == nil {
				t.Fatalf("banco aceitou OPENING com %s", nome)
			}
			if !dbtest.ErroDeConstraint(err) {
				t.Fatalf("devolveu %v, esperado violacao de constraint", err)
			}
		})
	}
}

// Politica de valor por tipo, verificada no banco.
func TestPoliticaDeValorPorTipo(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	casos := []struct {
		nome   string
		tipo   string
		valor  int64
		recusa bool
	}{
		{"BET com valor zero", "BET", 0, true},
		{"BET com valor negativo", "BET", -2500, true},
		{"WIN com valor zero", "WIN", 0, true},
		{"LOSS com valor positivo", "LOSS", 1, true},
		{"LOSS com valor negativo", "LOSS", -1, true},
		{"BET com valor positivo", "BET", 2500, false},
		{"LOSS com valor zero", "LOSS", 0, false},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			id := uuid.NewString()
			_, err := conexao.ExecContext(context.Background(), `
				INSERT INTO wager_transactions (
					id, provider_id, external_transaction_id, idempotency_key, content_hash,
					wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
					state, created_at, updated_at
				)
				VALUES ($1, 'provider-a', $2, $3, 'hash', $4, $5, 'round-987', 'fortune-chimp',
					$6, $7, 'BRL', 'PENDING', $8, $8)
			`, id, "ext-"+id, "chave-"+id, carteiraA, jogadorA, c.tipo, c.valor, agora())

			if c.recusa && err == nil {
				t.Fatalf("banco aceitou %s", c.nome)
			}
			if !c.recusa && err != nil {
				t.Fatalf("banco recusou caso valido %s: %v", c.nome, err)
			}
		})
	}
}

// Coerencia entre estado e dados. Um PROCESSED sem resultado deixa a resposta do
// replay sem o saldo observado; um REJECTED sem codigo deixa a auditoria sem
// explicacao.
func TestCoerenciaEntreEstadoEDados(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	casos := []struct {
		nome   string
		estado string
		result any
		moeda  any
		falha  any
		recusa bool
	}{
		{"processada com resultado", "PROCESSED", 7500, "BRL", nil, false},
		{"processada sem resultado", "PROCESSED", nil, nil, nil, true},
		{"processada com codigo de falha", "PROCESSED", 7500, "BRL", "BET_SEM_SALDO", true},
		{"rejeitada com codigo", "REJECTED", nil, nil, "BET_SEM_SALDO", false},
		{"rejeitada sem codigo", "REJECTED", nil, nil, nil, true},
		{"rejeitada com resultado", "REJECTED", 7500, "BRL", "BET_SEM_SALDO", true},
		{"pendente limpa", "PENDING", nil, nil, nil, false},
		{"pendente com resultado", "PENDING", 7500, "BRL", nil, true},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			id := uuid.NewString()
			_, err := conexao.ExecContext(context.Background(), `
				INSERT INTO wager_transactions (
					id, provider_id, external_transaction_id, idempotency_key, content_hash,
					wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
					state, result_amount, result_currency, failure_code, created_at, updated_at
				)
				VALUES ($1, 'provider-a', $2, $3, 'hash', $4, $5, 'round-987', 'fortune-chimp',
					'BET', 2500, 'BRL', $6, $7, $8, $9, $10, $10)
			`, id, "ext-"+id, "chave-"+id, carteiraA, jogadorA, c.estado, c.result, c.moeda, c.falha, agora())

			if c.recusa && err == nil {
				t.Fatalf("banco aceitou %s", c.nome)
			}
			if !c.recusa && err != nil {
				t.Fatalf("banco recusou caso valido %s: %v", c.nome, err)
			}
		})
	}
}

// Uma movimentacao por transacao por carteira. E o que impede movimentacao
// duplicada mesmo que a idempotencia da transacao falhe.
func TestUnicidadeDoLancamentoPorTransacao(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	lancamento := `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, money_amount, currency,
			balance_before, balance_after, created_at
		)
		VALUES ($1, $2, $3, 'DEBIT', 2500, 'BRL', 10000, 7500, $4)`

	if _, err := conexao.ExecContext(context.Background(), lancamento, "0192f299-1111-7e38-af88-e43f851a819d", carteiraA, transacaoA, agora()); err != nil {
		t.Fatalf("primeiro lancamento: %v", err)
	}
	if _, err := conexao.ExecContext(context.Background(), lancamento, "0192f299-2222-7e38-af88-e43f851a819d", carteiraA, transacaoA, agora()); err == nil {
		t.Fatal("banco aceitou segundo lancamento da mesma transacao na mesma carteira")
	}
}

// Direcao e valor do lancamento, no conjunto fechado.
func TestDirecaoEValorDoLancamento(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	casos := []struct {
		nome   string
		id     string
		dir    string
		valor  int64
		recusa bool
	}{
		{"credito valido", "0192f299-1111-7e38-af88-e43f851a8191", "CREDIT", 2500, false},
		{"debito valido", "0192f299-1111-7e38-af88-e43f851a8192", "DEBIT", 2500, false},
		{"direcao desconhecida", "0192f299-1111-7e38-af88-e43f851a8193", "TRANSFER", 2500, true},
		{"valor zero", "0192f299-1111-7e38-af88-e43f851a8194", "CREDIT", 0, true},
		{"valor negativo", "0192f299-1111-7e38-af88-e43f851a8195", "CREDIT", -2500, true},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := conexao.ExecContext(context.Background(), `
				INSERT INTO wallet_ledger_entries (
					id, wallet_id, transaction_id, direction, money_amount, currency,
					balance_before, balance_after, created_at
				)
				VALUES ($1, $2, $3, $4, $5, 'BRL', 10000, 7500, $6)
			`, c.id, carteiraA, uuid.NewString(), c.dir, c.valor, agora())

			if c.recusa && err == nil {
				t.Fatalf("banco aceitou %s", c.nome)
			}
			if !c.recusa && err != nil {
				t.Fatalf("banco recusou caso valido %s: %v", c.nome, err)
			}
		})
	}
}

// Referencia externa obrigatoria em REFUND e ROLLBACK, e proibida nos demais.
func TestReferenciaObrigatoriaNasReversoes(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)
	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 10000)

	insere := func(id, tipo string, referencia any) error {
		_, err := conexao.ExecContext(context.Background(), `
			INSERT INTO wager_transactions (
				id, provider_id, external_transaction_id, idempotency_key, content_hash,
				wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
				reference_external_id, state, created_at, updated_at
			)
			VALUES ($1, 'provider-a', $2, $3, 'hash', $4, $5, 'round-987', 'fortune-chimp',
				$6, 2500, 'BRL', $7, 'PENDING', $8, $8)
		`, id, "ext-"+id, "chave-"+id, carteiraA, jogadorA, tipo, referencia, agora())
		return err
	}

	if err := insere(transacaoA, "REFUND", nil); err == nil {
		t.Error("banco aceitou REFUND sem referencia externa")
	}
	if err := insere(transacaoA, "ROLLBACK", nil); err == nil {
		t.Error("banco aceitou ROLLBACK sem referencia externa")
	}
	if err := insere(transacaoA, "REFUND", "transaction-123"); err != nil {
		t.Errorf("banco recusou REFUND com referencia: %v", err)
	}
	if err := insere(transacaoB, "BET", nil); err != nil {
		t.Errorf("banco recusou BET sem referencia: %v", err)
	}
}

// Inbox por consumidor: a mesma mensagem pode ser entregue a consumidores
// diferentes, e cada um decide o que fazer com ela.
func TestInboxPorConsumidorEMensagem(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)

	insere := func(consumidor, mensagem string) error {
		_, err := conexao.ExecContext(context.Background(), `
			INSERT INTO inbox_messages (consumer_name, message_id, message_hash, received_at)
			VALUES ($1, $2, 'hash', $3)
		`, consumidor, mensagem, agora())
		return err
	}

	if err := insere("wager-consumer", "msg-1"); err != nil {
		t.Fatalf("primeira insercao: %v", err)
	}
	if err := insere("wager-consumer", "msg-1"); err == nil {
		t.Error("banco aceitou a mesma mensagem duas vezes para o mesmo consumidor")
	}
	if err := insere("outro-consumidor", "msg-1"); err != nil {
		t.Errorf("banco recusou a mesma mensagem para outro consumidor: %v", err)
	}
}

// O que o banco aceita tem de ser exatamente o que o dominio aceita. Se o banco
// recusar um caso valido, o codigo passa a carregar um|workaround; se aceitar um
// invalido, a garantia e do codigo e nao do dado.
func TestCasosValidosSaoAceitos(t *testing.T) {
	dbtest.Limpa(t)
	conexao := dbtest.NovaConexao(t)

	criaCarteira(t, conexao, carteiraA, jogadorA, "BRL", 0)
	criaCarteira(t, conexao, carteiraB, jogadorB, "USD", 0)

	criaTransacao(t, conexao, transacaoA, "provider-a", carteiraA, jogadorA, "provider-a:1", "1", "BET", 2500)
	criaTransacao(t, conexao, transacaoB, "provider-b", carteiraB, jogadorB, "provider-b:1", "1", "WIN", 2500)

	if _, err := conexao.ExecContext(context.Background(), `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, money_amount, currency,
			balance_before, balance_after, created_at
		)
		VALUES ('0192f299-1111-7e38-af88-e43f851a819d', $1, $2, 'DEBIT', 2500, 'BRL', 10000, 7500, $3)
	`, carteiraA, transacaoA, agora()); err != nil {
		t.Fatalf("lancamento valido recusado: %v", err)
	}

	if _, err := conexao.ExecContext(context.Background(), `
		INSERT INTO outbox_events (
			id, aggregate_id, event_type, payload, correlation_id, version, occurred_at, next_attempt_at
		)
		VALUES ('0192f29a-1111-7e38-af88-e43f851a819d', $1, 'WagerTransactionProcessed',
			'{"transactionId":"x"}'::jsonb, 'corr-1', 1, $2, $2)
	`, transacaoA, agora()); err != nil {
		t.Fatalf("evento de outbox valido recusado: %v", err)
	}
}
