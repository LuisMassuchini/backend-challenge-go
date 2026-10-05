-- Migration 00005: agendamento de retomada das pendencias de referencia.
--
-- Uma transacao em PENDING_REFERENCE esta esperando uma operacao que pode nunca
-- chegar. Onde o agendamento mora e uma decisao, e ela e: no banco e nao no processo.
--
-- O worker de referencias pendentes pode morrer entre a leitura da pendencia e a
-- tentativa. Sem a data da proxima tentativa gravada junto da linha, a unica
-- recuperacao seria varrer tudo a cada ciclo, o que custa uma varredura completa da
-- tabela por intervalo. Com a data gravada, o trabalho vira de de ser o que esta pronto
-- para agora, e o indice faz isso.
--
-- Alem disso, tres instancias independentes precisam parar de disputar a mesma
-- pendencia. O `FOR UPDATE SKIP LOCKED` resolve isso sem coordenacao, e a coluna de
-- tentativas e o que impede que uma pendencia impoderavel seja retomada para sempre.
--
-- +goose Up
-- +goose StatementBegin

-- Quantas vezes a retomada foi tentada.
--
-- Comeca em zero e e incrementada pelo worker. Nao e um contador de entrega: conta
-- tentativas de retomada, que e o que decide a expiracao.
ALTER TABLE wager_transactions
    ADD COLUMN retry_count integer NOT NULL DEFAULT 0;

-- Quando a proxima tentativa pode acontecer.
--
-- O padrao e o instante da criacao, e nao now(), porque uma transacao ja inserida
-- precisa ficar elegivel na hora. A constraint garante que so quem espera tem
-- agendamento: uma transacao concluida com data de retentativa e um dado que
-- contradiz o estado.
ALTER TABLE wager_transactions
    ADD COLUMN next_retry_at timestamptz NOT NULL DEFAULT now();

-- O motivo da ultima falha de retomada, para o operador ver por que a pendencia
-- travou.
--
-- E nullable de proposito: pendencia nao vencida nao tem falha.
ALTER TABLE wager_transactions
    ADD COLUMN last_retry_error text;

-- Uma nota de falha so faz sentido em quem espera ou em quem expirou.
--
-- A constraint liga o texto da ultima falha ao estado: uma transacao PROCESSED com
-- nota de falha e um dado que se contradiz, e o operador veria um erro de retomada
-- em uma operacao que deu certo. Sem a constraint, o bug apareceria como uma nota
-- obsoleta que ninguem sabe explicar.
ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_nota_somente_em_espera
    CHECK (
        last_retry_error IS NULL
        OR state IN ('PENDING', 'PENDING_REFERENCE', 'FAILED')
    );

-- O indice e o que faz o worker barato.
--
-- E um indice parcial porque a consulta do worker sempre filtra por estado: um
-- indice sobre a tabela inteira guardaria paginas de transacoes ja concluidas que
-- nunca serao lidas, e o indice ficaria grande sem aproveitamento.
CREATE INDEX idx_transacoes_pendentes
    ON wager_transactions (next_retry_at, id)
    WHERE state = 'PENDING_REFERENCE';

-- O runtime continua sem poder UPDATE em tabela do dominio? Nao: o worker precisa
-- agendar a proxima tentativa e concluir a transacao. O que ele nao pode e DELETE,
-- e o papel de runtime nao tem esse privilegio -- ja garantido em 00004.

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_transacoes_pendentes;

ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS ck_transacoes_retentativa_somente_pendente;

ALTER TABLE wager_transactions
    DROP COLUMN IF EXISTS last_retry_error,
    DROP COLUMN IF EXISTS next_retry_at,
    DROP COLUMN IF EXISTS retry_count;

-- +goose StatementEnd
