-- Migration 00008: nota de falha de retomada em transacao que deu certo.
--
-- A constraint da 00005 proibia nota de falha de retomada em `PROCESSED` e em
-- `REJECTED`, com o argumento de que uma operacao bem-sucedida nao deveria exibir um
-- erro de retomada. O argumento estava errado, e a constraint proibia um caminho
-- retrospectiva que existe.
--
-- **O cenario que quebrou:** um `ROLLBACK` chega antes da aposta que ele referencia. A
-- transacao fica em `PENDING_REFERENCE`, o worker registra a nota "referencia ausente"
-- e reagenda. A aposta chega, a proxima retentativa resolve a referencia e a
-- pendencia vira `PROCESSED` -- com a nota da tentativa anterior ainda na linha. A
-- constraint recusa o `UPDATE`, o worker tenta de novo, a constraint recusa de novo, e a
-- pendencia nunca sai de `PENDING_REFERENCE` mesmo com a referencia resolvida.
--
-- A pendencia foi resolvida e o sistema se recusava a registrar isso. O operador via um
-- estorno travado sem nenhum movimento de dinheiro errado: o pior tipo de bug, porque
-- a garantia financeira continua valendo e a operacao simplesmente nunca termina.
--
-- Nenhum teste anterior pegou porque `pendenciasteste` cobre a expiracao -- que grava
-- `FAILED`, estado que a constraint permite -- e nao a retomada bem-sucedida. O
-- primeiro teste de E2E com reversao em processo real foi o que encontrou.
--
-- +goose Up
-- +goose StatementBegin

-- A constraint e relaxada, e nao removida.
--
-- Relaxar e nao remover porque a intencao original tem uma parte que continua valendo:
-- uma nota de falha **isolada**, sem estado de espera nem de expiracao, e um dado que
-- se contradiz. O que deixou de valer foi tratar `PROCESSED` e `REJECTED` como
-- contradicao -- eles significam "a espera acabou em um desfecho", e a nota conta o
-- caminho ate la.
ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS ck_transacoes_nota_somente_em_espera;

ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_nota_requer_historia
    CHECK (
        -- Uma nota so pode existir se a transacao passou por espera: um
        -- `retry_count` maior que zero e o rastro de que houve espera.
        last_retry_error IS NULL
        OR retry_count > 0
        OR state IN ('PENDING', 'PENDING_REFERENCE', 'FAILED')
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE wager_transactions
    DROP CONSTRAINT IF EXISTS ck_transacoes_nota_requer_historia;

ALTER TABLE wager_transactions
    ADD CONSTRAINT ck_transacoes_nota_somente_em_espera
    CHECK (
        last_retry_error IS NULL
        OR state IN ('PENDING', 'PENDING_REFERENCE', 'FAILED')
    );

-- +goose StatementEnd
