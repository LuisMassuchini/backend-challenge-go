-- Migration 00003: ledger append-only e coerencia de saldo no banco.
--
-- O CHECK de coerencia entre valor e saldo poderia ser uma constraint do tipo
-- CHECK, mas a coerencia completa nao cabe em um CHECK: ela precisa da linha
-- anterior da mesma carteira e do saldo atual da carteira. Isso exige trigger.
--
-- +goose Up
-- +goose StatementBegin

-- Validacao por linha, no instante do INSERT.
--
-- E a mesma regra que o dominio aplica em NovoLancamento, reescrita para o
-- banco. A duplicacao e o ponto: o dominio protege a operacao que passa pelo
-- agregado, e o trigger protege a linha de qualquer outro caminho de escrita.
CREATE OR REPLACE FUNCTION wager_valida_lancamento() RETURNS trigger AS $$
DECLARE
    esperado bigint;
BEGIN
    IF NEW.direction = 'CREDIT' THEN
        esperado := NEW.balance_before + NEW.money_amount;
    ELSE
        esperado := NEW.balance_before - NEW.money_amount;
    END IF;

    IF NEW.balance_after <> esperado THEN
        RAISE EXCEPTION
            'lancamento incoerente: % de % sobre % resulta em %, e nao em %',
            NEW.direction, NEW.money_amount, NEW.balance_before, esperado, NEW.balance_after
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_lancamento_coerente
    BEFORE INSERT ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wager_valida_lancamento();

-- Append-only.
--
-- O ledger e a prova de que o dinheiro se moveu, e uma prova que pode ser
-- reescrita nao e prova. Correcao financeira e lancamento novo: por isso UPDATE e
-- DELETE sao recusados aqui e nao apenas desaconselhados.
CREATE OR REPLACE FUNCTION wager_proibe_alteracao_lancamento() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'ledger e append-only: % em % e %', TG_OP, TG_TABLE_NAME, OLD.id
        USING ERRCODE = '23514';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_lancamento_append_only
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wager_proibe_alteracao_lancamento();

-- Coerencia entre ledger e carteira, conferida no commit.
--
-- DEFERRABLE INITIALLY DEFERRED porque a ordem de escrita dentro da transacao e
-- livre: o codigo pode atualizar a carteira e depois inserir o lancamento, ou o
-- contrario. Um trigger imediato exigiria uma ordem especifica e transformaria a
-- ordem de duas linhas em uma constraint de arquitetura.
--
-- Sao duas conferencias:
--
-- 1. O saldo da carteira tem de ser igual ao saldo posterior do ultimo lancamento
--    dela. E o que pega o lancamento sem atualizacao financeira correspondente --
--    a movimentacao entrou no ledger e o saldo nao acompanhou.
--
-- 2. O saldo anterior do lancamento tem de ser igual ao saldo posterior do
--    lancamento anterior da mesma carteira. E o que impede um lancamento com saldo
--    anterior inventado, que faria a soma do ledger divergir do saldo sem que
--    nenhuma linha parecesse errada.
CREATE OR REPLACE FUNCTION wager_confere_coerencia_ledger() RETURNS trigger AS $$
DECLARE
    ultimo_balance_after bigint;
    ultimo_id            uuid;
    saldo_da_carteira    bigint;
    anterior_balance_after bigint;
BEGIN
    SELECT balance_after, id
      INTO ultimo_balance_after, ultimo_id
      FROM wallet_ledger_entries
     WHERE wallet_id = NEW.wallet_id
       AND (created_at, id) < (NEW.created_at, NEW.id)
     ORDER BY created_at DESC, id DESC
     LIMIT 1;

    IF ultimo_id IS NOT NULL AND NEW.balance_before <> ultimo_balance_after THEN
        RAISE EXCEPTION
            'lancamento nao encadeia com o anterior: saldo anterior %, anterior %',
            NEW.balance_before, ultimo_balance_after
            USING ERRCODE = '23514';
    END IF;

    SELECT balance INTO saldo_da_carteira FROM wallets WHERE id = NEW.wallet_id;

    SELECT balance_after
      INTO ultimo_balance_after
      FROM wallet_ledger_entries
     WHERE wallet_id = NEW.wallet_id
     ORDER BY created_at DESC, id DESC
     LIMIT 1;

    IF ultimo_balance_after IS NULL OR ultimo_balance_after <> saldo_da_carteira THEN
        RAISE EXCEPTION
            'saldo da carteira % divergente do ledger: carteira %, ultimo lancamento %',
            NEW.wallet_id, saldo_da_carteira, ultimo_balance_after
            USING ERRCODE = '23514';
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_ledger_coerente_na_carteira
    AFTER INSERT ON wallet_ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wager_confere_coerencia_ledger();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_ledger_coerente_na_carteira ON wallet_ledger_entries;
DROP TRIGGER IF EXISTS trg_lancamento_append_only ON wallet_ledger_entries;
DROP TRIGGER IF EXISTS trg_lancamento_coerente ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS wager_confere_coerencia_ledger();
DROP FUNCTION IF EXISTS wager_proibe_alteracao_lancamento();
DROP FUNCTION IF EXISTS wager_valida_lancamento();
-- +goose StatementEnd
