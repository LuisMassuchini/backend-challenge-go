-- Migration 00004: runtime role de menor privilegio e limites de sessao.
--
-- O schema e do dono; a aplicacao roda com um papel que nao e o dono. A diferenca
-- nao e de arquitetura: se a aplicacao usa o mesmo papel que criou as tabelas, um
-- `DELETE` acidental em `wallet_ledger_entries` passa e o append-only vira apenas
-- recomendacao.
--
-- O papel de runtime nao tem DELETE em lugar nenhum, e nao por esquecimento: nada no
-- sistema apaga lancamento, transacao ou evento publicado. A unica coisa que se
-- remove e dado de entrada da inbox, e a limpeza e feita pelo dono.
--
-- A senha e de ambiente local e esta aqui porque o papel precisa existir sem passo
-- manual. Nenhuma delas e segredo real, e o `.env.example` do projeto traz as
-- mesmas.
--
-- +goose Up
-- +goose StatementBegin

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'wager_app') THEN
        CREATE ROLE wager_app LOGIN PASSWORD 'wager_app';
    END IF;
END
$$;

-- Limites de sessao do papel de runtime.
--
-- Esta e a decisao que o plano mandava fechar antes de escrever o pool:
-- `statement_timeout` e `lock_timeout` nao sao vao: com varias instancias
-- disputando a mesma carteira, uma transacao que espera lock sem limite segura o
-- lock e transforma contencao em indisponibilidade. O pedido falha rapido, o
-- caso de uso devolve erro transitorio e o provedor reenvia.
--
-- O valor e 3s de statement e 1s de lock porque a transacao financeira faz poucas
-- operacoes -- um SELECT FOR UPDATE, um INSERT, um UPDATE condicional e os indices
-- -- e nao ha justificativa para segundos dentro dela. Uma transacao que precisa de
-- mais que isso e uma transacao que esta fazendo trabalho demais dentro do lock.
--
-- `idle_in_transaction_session_timeout` fecha a sessao que abriu transacao e a
-- esqueceu. Sem ela, uma conexao esquecida segura o lock que esqueceu, e o sintoma
-- aparece como lentidao geral em vez de como conexao pendurada.
ALTER ROLE wager_app SET statement_timeout = '3s';
ALTER ROLE wager_app SET lock_timeout = '1s';
ALTER ROLE wager_app SET idle_in_transaction_session_timeout = '15s';

GRANT CONNECT ON DATABASE wager TO wager_app;
GRANT USAGE ON SCHEMA public TO wager_app;

-- DML no necessario e nada alem disso.
GRANT SELECT, INSERT, UPDATE ON
    wallets,
    wager_transactions,
    wallet_ledger_entries,
    inbox_messages,
    outbox_events
TO wager_app;

-- Sem DELETE, sem DDL, sem CREATE no schema: o papel de runtime nao cria objeto e
-- nao apaga linha. E o que faz do append-only do ledger uma garantia do banco e
-- nao apenas do codigo.
REVOKE DELETE, TRUNCATE ON
    wallets,
    wager_transactions,
    wallet_ledger_entries,
    inbox_messages,
    outbox_events
FROM wager_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- A revogacao do DML vem antes da queda do papel: um GRANT para um papel que nao
-- existe mais e um erro, e a migration de reversao nao pode falhar por causa da
-- ordem das linhas.
REVOKE ALL ON
    wallets,
    wager_transactions,
    wallet_ledger_entries,
    inbox_messages,
    outbox_events
FROM wager_app;
REVOKE ALL ON SCHEMA public FROM wager_app;
REVOKE CONNECT ON DATABASE wager FROM wager_app;
DROP ROLE IF EXISTS wager_app;
-- +goose StatementEnd
