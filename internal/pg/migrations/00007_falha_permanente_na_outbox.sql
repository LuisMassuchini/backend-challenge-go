-- Migration 00007: falha permanente na publicacao de evento.
--
-- Um evento pode falhar ao ser publicado por um defeito que nao passa: um envelope
-- que nao serializa, um broker que recusa este conteudo. Nesses casos a retentativa
-- com backoff so gasta recurso: a decima segunda tentativa republica exatamente o
-- mesmo conteudo invalido da primeira.
--
-- Onde esse desfecho fica gravado e uma decisao. Descartar o registro resolveria o
-- laco, mas apagar evento e perder a evidencia de que ele existiu e nao foi
-- publicado -- e o papel de runtime nao tem DELETE, o que ja e uma garantia de que
-- nada no sistema apaga evento. O desfecho aqui e o mesmo da transacao com
-- referencia que nunca chega: marcar como falha permanente, com codigo e motivo
-- gravados, e deixar de retomar.
--
-- A razao de existir e de nao ficar em silencio. Um relay que desiste sem deixar
-- rastro e indistinguivel de um relay que nunca recebeu o evento.
--
-- +goose Up
-- +goose StatementBegin

-- Motivo da ultima falha de publicacao, ou a ultima falha permanente.
--
-- E nullable de proposito: evento publicado ou ainda nao tentado nao tem motivo.
ALTER TABLE outbox_events
    ADD COLUMN failure_reason text;

-- Instante da falha permanente.
--
-- Separado do motivo porque sao dados de auditoria diferentes: o motivo responde o
-- que aconteceu e o instante responde quando. Quem responde "o relay esta atrasado"
-- precisa do instante; quem responde "por que este evento nao saiu" precisa do texto.
ALTER TABLE outbox_events
    ADD COLUMN failed_at timestamptz;

-- Um evento com motivo de falha nao pode estar publicado.
--
-- A constraint liga os dois desfechos: `published_at` preenchido com `failure_reason`
-- preenchido e um registro que diz ter saido e nao ter saido ao mesmo tempo. Sem a
-- constraint, o bug apareceria como um evento publicado que o operador nao
-- consegue explicar.
--
-- O mesmo vale para o instante: a falha tem hora, e um evento sem hora de falha nao
-- tem desfecho.
ALTER TABLE outbox_events
    ADD CONSTRAINT ck_outbox_falha_ou_publicado
    CHECK (
        (failure_reason IS NULL AND failed_at IS NULL AND published_at IS NULL)
        OR (failure_reason IS NOT NULL AND failed_at IS NOT NULL AND published_at IS NULL)
        OR (failure_reason IS NULL AND failed_at IS NULL AND published_at IS NOT NULL)
    );

-- O relay precisa achar os registros que ele pode publicar sem varrer os que ja
-- desistiram.
--
-- O indice parcial sobre `published_at IS NULL` ja existia, mas ele tambem trazia os
-- registros com falha permanente: eles nao voltam para a fila e nunca serao lidos, e
-- ficavam ocupando paginas do indice para sempre. Este os exclui, e o relay passa a
-- varredura so pelo trabalho que ainda existe.
--
-- O indice anterior nao e removido: ele responde a "o que esta elegivel agora", com
-- `next_attempt_at`, e este responde a "o que publicar, em que ordem".
CREATE INDEX idx_outbox_publicaveis
    ON outbox_events (occurred_at, id)
    WHERE published_at IS NULL AND failed_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_outbox_publicaveis;

ALTER TABLE outbox_events
    DROP CONSTRAINT IF EXISTS ck_outbox_falha_ou_publicado;

ALTER TABLE outbox_events
    DROP COLUMN IF EXISTS failed_at,
    DROP COLUMN IF EXISTS failure_reason;

-- +goose StatementEnd
