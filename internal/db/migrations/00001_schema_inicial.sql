-- Migration 00001: schema inicial.
--
-- As tabelas nascem sem as garantias que a etapa seguinte acrescenta. Isso e
-- deliberado e e o que torna cada migration reversivel por conta propria: uma
-- migration que cria tabela e ja cria trigger fica com o trigger preso nela, e a
-- reversao precisa derrubar as duas.
--
-- +goose Up
-- +goose StatementBegin

-- wallets e a raiz do agregado financeiro. O par (player_id, currency)
-- identifica a carteira, e a unicidade desse par e o que impede duas carteiras do
-- mesmo jogador na mesma moeda.
CREATE TABLE wallets (
    id             uuid        PRIMARY KEY,
    player_id      uuid        NOT NULL,
    currency       char(3)     NOT NULL,
    -- balance e int64 em unidade minima, com escala 2. BIGINT e nao NUMERIC
    -- porque o valor gravado tem de ser exatamente o valor lido, e NUMERIC no
    -- driver PostgreSQL volta como string.
    balance        bigint      NOT NULL,
    -- version comeca em 1 e sobe so em mudanca efetiva de saldo. Comecar em 0
    -- faria um update condicional nao distinguir "nenhuma linha atualizada" de
    -- "atualizada de zero para zero", e um lost update passaria por sucesso.
    version        bigint      NOT NULL DEFAULT 1,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);

-- wager_transactions e a operacao externa, e tambem a abertura interna. A
-- distincao esta no tipo: OPENING nao tem identidade externa, e o CHECK da etapa
-- seguinte impede que tenha.
CREATE TABLE wager_transactions (
    id                        uuid        PRIMARY KEY,
    provider_id               text        NOT NULL,
    external_transaction_id   text        NOT NULL,
    idempotency_key           text        NOT NULL,
    content_hash              text        NOT NULL,
    wallet_id                 uuid        NOT NULL REFERENCES wallets (id),
    player_id                 uuid        NOT NULL,
    round_id                  text        NOT NULL,
    game_id                   text        NOT NULL,
    kind                      text        NOT NULL,
    money_amount              bigint      NOT NULL,
    currency                  char(3)     NOT NULL,
    reference_external_id     text,
    reference_internal_id     uuid,
    state                     text        NOT NULL,
    failure_code              text,
    -- result_amount e o saldo devolvido ao provedor no processamento original.
    -- Persistir e o que faz o replay devolver o saldo que o provedor viu, e nao
    -- o saldo de hoje.
    result_amount             bigint,
    result_currency           char(3),
    created_at                timestamptz NOT NULL,
    updated_at                timestamptz NOT NULL
);

-- wallet_ledger_entries e o ledger append-only da carteira. Os dois saldos sao
-- gravados na propria linha porque a reconciliacao reconstroi o saldo a partir
-- deles e compara com o saldo armazenado.
CREATE TABLE wallet_ledger_entries (
    id             uuid        PRIMARY KEY,
    wallet_id      uuid        NOT NULL REFERENCES wallets (id),
    transaction_id uuid        NOT NULL,
    direction      text        NOT NULL,
    money_amount   bigint      NOT NULL,
    currency       char(3)     NOT NULL,
    balance_before bigint      NOT NULL,
    balance_after  bigint      NOT NULL,
    created_at     timestamptz NOT NULL
);

-- inbox_messages marca o que cada consumidor ja processou. A chave primaria e
-- (consumer_name, message_id) porque a mesma mensagem pode ser entregue a
-- consumidores diferentes, e cada um decide o que fazer com ela.
CREATE TABLE inbox_messages (
    consumer_name text        NOT NULL,
    message_id    text        NOT NULL,
    message_hash  text        NOT NULL,
    received_at   timestamptz NOT NULL,
    completed_at  timestamptz,
    -- attempts conta as reentregas. A entrada da inbox e a conclusao duravel do
    -- tratamento compartilham a transacao SQL do dominio, entao completed_at
    -- preenchido significa "esta mensagem ja produziu efeito".
    attempts      integer     NOT NULL DEFAULT 1
);

-- outbox_events e a fila de publicacao. O registro e escrito na mesma transacao
-- que a alteracao financeira, e o relay o publica depois do commit.
CREATE TABLE outbox_events (
    id             uuid        PRIMARY KEY,
    aggregate_id   uuid        NOT NULL,
    event_type     text        NOT NULL,
    -- payload e o snapshot imutavel do evento. Guardar o snapshot e o que faz a
    -- republicacao publicar exatamente o que foi gravado.
    payload        jsonb       NOT NULL,
    correlation_id text        NOT NULL,
    causation_id   uuid,
    version        integer     NOT NULL,
    occurred_at    timestamptz NOT NULL,
    attempts       integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    published_at   timestamptz,
    -- leased_until e a janela em que um publisher detem o registro. E o que
    -- permite que outro publisher assuma o trabalho de um publisher que morreu.
    leased_until   timestamptz,
    leased_by      text
);

-- indices de leitura. Nenhum deles e unico: unicidade e coisa da etapa seguinte, e
-- misturar as duas responsabilidades em uma migration impossibilita reverter uma
-- sem reverter a outra.
CREATE INDEX idx_wallets_player ON wallets (player_id, currency);
CREATE INDEX idx_transactions_wallet ON wager_transactions (wallet_id, created_at);
CREATE INDEX idx_transactions_state ON wager_transactions (state);
CREATE INDEX idx_transactions_reference ON wager_transactions (provider_id, reference_external_id);
CREATE INDEX idx_ledger_wallet ON wallet_ledger_entries (wallet_id, created_at, id);
CREATE INDEX idx_outbox_pending ON outbox_events (next_attempt_at) WHERE published_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS inbox_messages;
DROP TABLE IF EXISTS wallet_ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;
-- +goose StatementEnd
