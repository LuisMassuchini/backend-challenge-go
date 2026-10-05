-- Migration 00002: unicidade, nao negatividade e coerencia de estado.
--
-- Cada invariante que o dominio garante em Go aparece aqui como constraint. A
-- duplicacao e deliberada: o dominio protege a operacao, o banco protege o dado,
-- e o dado pode ser escrito por qualquer caminho, inclusive por um script de
-- operacao ou por uma versao futura do codigo que tenha esquecido a regra.
--
-- As colunas de identidade externa deixam de ser NOT NULL aqui. A migration 00001
-- as criou NOT NULL porque nao havia ainda a constraint que distingue origem
-- interna de origem externa; sem o DROP NOT NULL, um OPENING -- que nao tem
-- provedor -- seria impossivel de gravar.
--
-- +goose Up
-- +goose StatementBegin

-- Origem interna contra origem externa.
--
-- A regra tem dois lados e os dois sao constraint. O lado do dado: OPENING nao tem
-- identidade externa. O lado do provedor: tudo que nao e OPENING tem. Um
-- constraint de um lado so deixaria passar o outro, e um OPENING com provedor e
-- exatamente o que permitiria credito inicial duplicado.
ALTER TABLE wager_transactions
    ALTER COLUMN provider_id              DROP NOT NULL,
    ALTER COLUMN external_transaction_id  DROP NOT NULL,
    ALTER COLUMN idempotency_key          DROP NOT NULL,
    ALTER COLUMN content_hash             DROP NOT NULL,
    ALTER COLUMN round_id                 DROP NOT NULL,
    ALTER COLUMN game_id                  DROP NOT NULL;

ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_origem CHECK (
        (kind = 'OPENING'
            AND provider_id IS NULL
            AND external_transaction_id IS NULL
            AND idempotency_key IS NULL
            AND content_hash IS NULL
            AND round_id IS NULL
            AND game_id IS NULL
            AND reference_external_id IS NULL)
        OR
        (kind <> 'OPENING'
            AND provider_id IS NOT NULL
            AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL
            AND content_hash IS NOT NULL
            AND round_id IS NOT NULL
            AND game_id IS NOT NULL)
    );

-- Saldo nunca negativo. E a garantia final do sistema, e ela e do banco: lock no
-- cliente protege a operacao, CHECK protege o dado.
ALTER TABLE wallets
    ADD CONSTRAINT ck_wallets_saldo_nao_negativo CHECK (balance >= 0);

-- Versao comeca em 1. Com 0, um update condicional nao distingue "nenhuma linha
-- atualizada" de "atualizada de zero para zero", e um lost update passa por
-- sucesso.
ALTER TABLE wallets
    ADD CONSTRAINT ck_wallets_versao CHECK (version >= 1);

-- Conjunto fechado de tipos e de estados. Um valor fora do conjunto gravado
-- apareceria em consulta de auditoria sem que ninguem soubesse como trata-lo.
ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_tipo CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    ADD CONSTRAINT ck_transacoes_estado CHECK (state IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED'));

-- Politica de zero por tipo, verificada no banco.
--
-- LOSS exige zero exato e nao produz movimentacao; os demais exigem valor maior
-- que zero. O CHECK e o que impede um LOSS com valor de-database e um BET de valor
-- zero, que entrariam no ledger como lancamento de valor zero.
ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_valor_por_tipo CHECK (
        (kind = 'LOSS' AND money_amount = 0)
        OR
        (kind <> 'LOSS' AND money_amount > 0)
    );

-- Coerencia entre estado e dados, por estado e nao por "terminal".
--
-- PROCESSED e terminal de sucesso: tem resultado e nao tem codigo de falha.
-- REJECTED e FAILED sao terminais de falha: tem codigo e nao tem resultado.
-- PENDING e PENDING_REFERENCE nao tem nenhum dos dois. Uma regra escrita por
-- "terminal" recusaria um PROCESSED legitimo por nao ter codigo de falha.
ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_estado_e_dados CHECK (
        (state = 'PROCESSED' AND result_amount IS NOT NULL AND result_currency IS NOT NULL AND failure_code IS NULL)
        OR
        (state IN ('REJECTED', 'FAILED') AND failure_code IS NOT NULL AND result_amount IS NULL)
        OR
        (state IN ('PENDING', 'PENDING_REFERENCE') AND failure_code IS NULL AND result_amount IS NULL)
    );

-- Referencia externa e obrigatoria nas duas operacoes que dependem de outra.
ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_referencia CHECK (
        (kind IN ('REFUND', 'ROLLBACK') AND reference_external_id IS NOT NULL)
        OR
        (kind NOT IN ('REFUND', 'ROLLBACK'))
    );

-- Moeda e valor tem de concordar, e o codigo de moeda e do catalogo.
ALTER TABLE wallets
    ADD CONSTRAINT ck_wallets_moeda CHECK (currency IN ('BRL', 'USD', 'EUR'));

ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_moeda CHECK (currency IN ('BRL', 'USD', 'EUR'));

-- Direcao do lancamento tem exatamente dois valores. Um terceiro valor nao tem
-- soma associada, e a reconciliacao acabaria descobrindo a soma a partir do texto.
ALTER TABLE wallet_ledger_entries
    ADD CONSTRAINT ck_ledger_direcao CHECK (direction IN ('DEBIT', 'CREDIT')),
    ADD CONSTRAINT ck_ledger_valor CHECK (money_amount > 0),
    ADD CONSTRAINT ck_ledger_saldos_nao_negativos CHECK (balance_before >= 0 AND balance_after >= 0),
    ADD CONSTRAINT ck_ledger_moeda CHECK (currency IN ('BRL', 'USD', 'EUR'));

-- Unicidade de (player_id, currency): o par identifica a carteira.
ALTER TABLE wallets
    ADD CONSTRAINT uq_wallets_jogador_moeda UNIQUE (player_id, currency);

-- Unicidade de idempotencia, em dois indices com papeis diferentes.
--
-- A chave impede reprocessar a mesma requisicao. O par (provedor, id externo)
-- impede que a mesma operacao financeira apareca com duas chaves diferentes, que
-- e o jeito mais comum de uma duplicidade entrar quando o cliente muda o esquema
-- da chave.
ALTER TABLE wager_transactions
    ADD CONSTRAINT uq_transacoes_chave UNIQUE (idempotency_key),
    ADD CONSTRAINT uq_transacoes_provedor_externo UNIQUE (provider_id, external_transaction_id);

-- Unicidade do ledger por (wallet_id, transaction_id): uma movimentacao por
-- transacao por carteira. E o que impede movimentacao duplicada mesmo que a
-- idempotencia da transacao falhe.
ALTER TABLE wallet_ledger_entries
    ADD CONSTRAINT uq_ledger_carteira_transacao UNIQUE (wallet_id, transaction_id);

-- Credito inicial duplicado: uma carteira tem no maximo uma abertura. Sem isso,
-- dois INSERT de OPENING para a mesma carteira criariam dois creditos, e a
-- unicidade de (player_id, currency) nao pegaria porque a carteira ja existe.
CREATE UNIQUE INDEX uq_transacoes_abertura_por_carteira
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

-- Inbox por consumidor: a mesma mensagem pode ser entregue a consumidores
-- diferentes, e cada um decide o que fazer com ela.
ALTER TABLE inbox_messages
    ADD CONSTRAINT uq_inbox_consumidor_mensagem PRIMARY KEY (consumer_name, message_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS uq_transacoes_abertura_por_carteira;
ALTER TABLE inbox_messages DROP CONSTRAINT IF EXISTS uq_inbox_consumidor_mensagem;
ALTER TABLE wallet_ledger_entries DROP CONSTRAINT IF EXISTS uq_ledger_carteira_transacao;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS uq_transacoes_provedor_externo;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS uq_transacoes_chave;
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS uq_wallets_jogador_moeda;
ALTER TABLE wallet_ledger_entries DROP CONSTRAINT IF EXISTS ck_ledger_moeda;
ALTER TABLE wallet_ledger_entries DROP CONSTRAINT IF EXISTS ck_ledger_saldos_nao_negativos;
ALTER TABLE wallet_ledger_entries DROP CONSTRAINT IF EXISTS ck_ledger_valor;
ALTER TABLE wallet_ledger_entries DROP CONSTRAINT IF EXISTS ck_ledger_direcao;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS ck_transacoes_moeda;
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS ck_wallets_moeda;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS ck_transacoes_referencia;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS ck_transacoes_estado_e_dados;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS ck_transacoes_valor_por_tipo;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS ck_transacoes_estado;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS ck_transacoes_tipo;
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS ck_wallets_versao;
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS ck_wallets_saldo_nao_negativo;
ALTER TABLE wager_transactions DROP CONSTRAINT IF EXISTS ck_transacoes_origem;
ALTER TABLE wager_transactions
    ALTER COLUMN provider_id             SET NOT NULL,
    ALTER COLUMN external_transaction_id SET NOT NULL,
    ALTER COLUMN idempotency_key         SET NOT NULL,
    ALTER COLUMN content_hash            SET NOT NULL,
    ALTER COLUMN round_id                SET NOT NULL,
    ALTER COLUMN game_id                 SET NOT NULL;
-- +goose StatementEnd
