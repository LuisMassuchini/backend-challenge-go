-- Migration 00006: indice de varredura do relay da outbox.
--
-- O relay le a outbox o tempo todo, e a leitura e `WHERE published_at IS NULL AND
-- next_attempt_at <= agora ORDER BY occurred_at, id LIMIT n FOR UPDATE SKIP LOCKED`.
--
-- O indice que existe ate aqui e por `next_attempt_at`, que e a ordem em que o
-- registro fica elegivel. Ele nao serve para a varredura do relay: o relay nao
-- pergunta "o que esta pronto agora" em ordem de elegibilidade, e sim "o mais antigo
-- primeiro", e ordenar por `occurred_at` sobre um indice de `next_attempt_at` obriga
-- o PostgreSQL a ordenar em memoria a tabela inteira de evento. Em uma tabela que so
-- cresce, o relay passaria a pagar o custo da tabela a cada ciclo.
--
-- Por isso um segundo indice, parcial e na ordem de leitura. Ele nao substitui o
-- primeiro: o `next_attempt_at` responde a "o que esta elegivel", e o
-- `occurred_at, id` responde a "em que ordem publicar".
--
-- E parcial porque o relay nunca le evento ja publicado. Um indice sobre a tabela
-- inteira guardaria paginas de evento publicado que nunca serao lidas, e ficaria
-- grande sem aproveitamento.
--
-- Este indice e criado aqui e substituido pela 00007, que acrescenta a coluna de
-- falha permanente e um indice mais estreito. Ele ja e criado separado porque cada
-- migration e reversivel por conta propria: a 00007 tem de reverter sem deixar para
-- tras um indice que so ela sabe que e obsoleto.
--
-- +goose Up
-- +goose StatementBegin

CREATE INDEX idx_outbox_pendentes_por_ordem
    ON outbox_events (occurred_at, id)
    WHERE published_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_outbox_pendentes_por_ordem;

-- +goose StatementEnd
