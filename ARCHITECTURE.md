# Arquitetura

Este arquivo registra as decisoes tecnicas com trade-off. Ele nao e um resumo do
codigo: e o lugar em que se explica **por que** cada escolha foi feita e o que ela
custa.

O enunciado original, que nao muda, esta em [`docs/challenge.md`](challenge.md).

As secoes sao acrescentadas na ordem em que a decisao e fechada, e nao na ordem
em que o codigo e lido. Uma decisao registrada tarde e uma decisao que ninguem
encontra.

---

## Dinheiro

**Decisao:** `Money` e um value object imutavel com `int64` em unidade minima e
escala fixa de duas casas, e a moeda ISO 4217 vem sempre junto.

**Por que `int64` e nao `NUMERIC`:** o enunciado proibe `float32` e `float64` em
qualquer etapa, e o erro classico do `NUMERIC` no PostgreSQL e o driver devolver
string. Com `BIGINT` em unidade minima, o valor gravado e o valor lido, sem
conversao, e a constraint de saldo nao negativo e um `CHECK` sobre inteiro.

**Escala fixa em duas casas, e nao por moeda:** o contrato externo manda
`"25.00"`, e uma escala por moeda permitiria JPY sem casas e USD com duas. Cada
combinacao seria um caso especial no schema, no hash de idempotencia e no
contrato JSON. O preco e um teto de 92233720368547758.07 por operacao, que nenhum
saldo real se aproxima.

**Parsing estrito:** a gramatica aceita apenas `[-] digitos+ . digitos{1,2}`.
Entrada fora dessa forma e recusada, nunca arredondada. Um arredondamento
silencioso muda dinheiro sem ninguem ver. `1.000` e classificado como escala
excedente e nao como separador de milhar: com ponto como separador decimal, sao
tres casas, e adivinhar que o autor quis dizer `1000` seria aceitar em silencio
uma precisao que ele nao enviou.

**Magnitude em unsigned no parsing:** o menor valor do sistema, `-92233720368547758.08`,
nao seria alcancavel se a magnitude fosse convertida direto em `int64`. Ele existe
porque e o valor que a negacao precisa recusar, e um valor que o sistema declara
aceitar e nao pode ler seria um limiteMentiroso.

**Overflow explicito:** `Add`, `Sub`, `Neg` e o proprio parsing devolvem
`ErrOverflow`. A soma sem guarda ja devolvia `-92233720368547758.08` sem erro, e um
saldo negativo silencioso e pior que operacao recusada: passa como numero valido
para o banco e para o log.

**Limite documentado:** `Money.Decimal()` monta a magnitude em `uint64` e e o
unico lugar do sistema inteiro que precisa conhecer `MinInt64`.

---

## Identificadores

**Decisao:** todo identificador e um UUID v7, com tipo proprio `Identificador`.

**Por que tipo proprio e nao `string`:** string crua aceita `transacao-123` e
UUID com espaco no fim, que so falham na constraint do banco -- e o erro aparece
longe da origem. Comparar com string vazia tambem nao pega o UUID zerado, que e um
valor valido que ninguem deveria usar.

**Por que v7 e nao v4:** v7 e ordenado por tempo. A carteira e a tabela com mais
escrita do sistema, e chave primaria de UUID v4 em tabela alta e um ponto de
contencao por linha. O custo e que o identificador revela a ordem de criacao, o
que e aceitavel para este sistema e nao seria para um identificador de sessao.

---

## Concorrencia

**Decisao:** lock pessimista por carteira, com `SELECT ... FOR UPDATE` na linha da
carteira, e atualizacao condicional por versao.

**Por que por carteira e nao global:** o enunciado proibe lock global. A
coordenacao por carteira e o grano que contem a dispute real -- duas operacoes na
mesma carteira -- e deixa carteiras diferentes em paralelo, que e o outro
requisito.

**Por que tambem versao:** o lock protege a leitura; a versao protege a escrita
condicional. Quem escreve com `WHERE version = $n` e recebe zero linhas atualizadas
sabe que alguem mexeu na carteira entre a leitura e a escrita, e pode repetir em
vez de sobrescrever. E a garantia contra lost update quando o lock falha ou quando
duas instancias operam em pools diferentes.

**A versacao da carteira sobe em mudanca efetiva de saldo.** Uma operacao sem
efeito -- LOSS -- nao altera saldo, nao produz lancamento e nao sobe a versao. Se
subisse, a linha seria reescrita sem motivo e o update condicional acusaria
conflito sem que houvesse dinheiro em jogo.

**A garantia final e no banco:** `CHECK (balance >= 0)`, unicidade de
`(player_id, currency)`, unicidade de `(wallet_id, transaction_id)` no ledger e
trigger que proibe `UPDATE`/`DELETE` em lancamento. O banco e a autoridade; o lock
e otimizacao.

---

## Idempotencia

**Decisao:** dois indices unicos -- `UNIQUE (idempotency_key)` e
`UNIQUE (provider_id, external_transaction_id)` -- e um hash canonico dos campos de
negocio.

**Por que dois indices:** cada um cobre uma realidade diferente. A chave impede
reprocessar a mesma requisicao; o par provedor e id externo impede que a mesma
operacao financeira aparece com duas chaves diferentes, que e o jeito que uma
duplicidade entra quando o cliente muda o esquema da chave.

**Por que hash e nao apenas a chave:** mesma chave com conteudo diferente e
conflito, nao replay. O hash e calculado sobre JSON canonico com chaves ordenadas,
excluindo a chave de idempotencia e os metadados de transporte. Normalizacao
qualquer -- escala, ordem de campo, representacao de moeda -- acontece **antes** do
hash, e e documentada, para que HTTP e SQS produzam o mesmo hash da mesma
operacao.

**O resultado original e persistido.** Replay devolve o saldo observado no
reprocessamento original, e nao o saldo atual: recalcular devolveria a resposta de
hoje para uma operacao de ontem.

---

## Transacoes e maquina de estados

**Decisao:** cinco estados -- `PENDING`, `PENDING_REFERENCE`, `PROCESSED`,
`REJECTED`, `FAILED` -- com tres terminais, e toda transacao nasce em `PENDING`.

**Rejeicao e falha sao coisas diferentes.** Rejeicao e regra de negocio: o cliente
pode corrigir a entrada e reenviar. Falha permanente e infraestrutura: reenviar nao
muda o resultado e o cliente nao tem o que corrigir. Tratar os dois como erro unico
faz o provedor reenviar para sempre uma operacao que nunca vai passar.

**A regra de coerencia entre estado e dados e por estado, e nao por "terminal".**
`PROCESSED` carrega resultado e nao carrega codigo de falha; `REJECTED` e `FAILED`
carregam codigo e nao carregam resultado. Com a regra por "terminal", um
`PROCESSED` legitimo seria recusado por nao ter codigo.

**`OPENING` e de origem interna e nasce em `PROCESSED`.** Nao ha referencia a
esperar e nao ha dependencia alem da propria carteira. Criar em `PENDING` e
depender de um segundo passo abriria uma janela em que a carteira existe sem a
transacao que a originou, e a reconciliacao executada nesse intervalo veria saldo
sem lancamento. A invariante e verificada nos dois sentidos: `Registrar` recusa
`OPENING` externo, e a reidratacao recusa `OPENING` com qualquer identidade externa.

**A politica de valor e aplicada na entrada.** `Registrar` exige valor maior que
zero para `BET`, `WIN`, `REFUND` e `ROLLBACK`, e zero exato para `LOSS`. Adiar essa
conferencia faria operacao com valor invalido virar registro duravel em `PENDING`
que outra instancia teria de rejeitar depois -- e o provedor receberia o codigo de
falha tarde demais para distinguir entrada incorreta de indisponibilidade.

**Saldo inicial zero nao cria `OPENING`,** nem lancamento, nem evento. Criar a
transacao assim daria um `OPENING` por jogador sem nenhum movimento por tras.

---

## Reversoes: `REFUND` e `ROLLBACK`

Esta era a decisao que o plano mandava fechar antes de codar.

**Decisao:** uma `BET` e neutralizada **uma unica vez**, por `REFUND` **ou** por
`ROLLBACK`, nunca pelas duas.

- `REFUND` referencia apenas `BET`. Devolve o debito da aposta.
- `ROLLBACK` referencia `BET`, `WIN` ou `REFUND`, e faz o contrario do que a
  referencia fez.
- Depois de um `REFUND` bem-sucedido sobre uma `BET`, um `ROLLBACK` sobre a mesma
  `BET` e recusado com `REVERSAO_JA_APLICADA`.
- Depois de um `ROLLBACK` bem-sucedido sobre uma `BET`, um `REFUND` sobre a mesma
  `BET` e recusado pelo mesmo codigo.
- Um `WIN` e desfeito uma unica vez, por `ROLLBACK`. Um `REFUND` tambem e desfeito
  uma unica vez, por `ROLLBACK` -- e o caminho para anular uma devolucao.
- Nao existe caminho para desfazer um `ROLLBACK`. Desfazer o contrario de uma
  operacao e refazer a operacao original, que o provedor deve enviar de novo com a
  chave dele.

**Por que as duas nao podem ser somadas:** `REFUND` de uma `BET` credita o valor
do debito, e `ROLLBACK` da mesma `BET` credita o mesmo valor. Aceitar as duas em
sequencia creditaria duas vezes o mesmo dinheiro, e o saldo da carteira deixaria de
corresponder ao ledger. Como o enunciado exige que a soma do ledger reproduza o
saldo, e o ledger e append-only, a duplicidade nao seria corrigivel depois: seria um
lancamento novo que contradiz dois anteriores.

**Por que o inverso e decidido pelo efeito e nao pelo tipo:** `BET` e debito, e o
contrario de um debito e `WIN`; `WIN` e `REFUND` sao credito, e o contrario de um
credito e `BET`. Decidir pelo tipo erra no `REFUND`, que e credito: um
`ROLLBACK` de `REFUND` que creditasse em vez de debitar devolveria o dinheiro duas
vezes.

**Conferencia de identidade:** operacao e referencia precisam concordar em
provedor, jogador, carteira, moeda e rodada. A rodada e a mais relevante, porque
uma reversao que atravessa rodada devolve dinheiro de uma aposta para outra e o
provedor nao tem como conferir.

**Valor:** o valor da reversao tem de ser igual ao valor referenciado. Reversao
parcial esta fora do desafio, e aceita-la exigiria uma segunda regra que ninguem
pediu: o que fazer com a parte nao devolvida.

**`ROLLBACK` sem saldo tem codigo proprio,** `ROLLBACK_SEM_SALDO`, diferente de
`BET_SEM_SALDO`. No primeiro o jogador nao tem saldo para o movimento inverso; no
segundo nao tinha saldo para a aposta. O erro carrega as duas informacoes: a causa
`wallet.ErrSaldoInsuficiente` para o caso de uso tratar saldo insuficiente como
saldo insuficiente, e o codigo estavel para o provedor tratar os dois problemas
como diferentes.

---

## Ledger

**Decisao:** `WalletLedgerEntry` imutavel, com `balanceBefore` e `balanceAfter`
gravados na propria linha, e validacao no construtor de que
`balanceAfter = balanceBefore +- money` conforme a direcao.

**Por que os dois saldos na linha:** a reconciliacao reconstrui o saldo a partir do
ledger e compara com o saldo armazenado. Com o saldo anterior em cada lancamento, a
reconstrucao e uma soma com deteccao de salto, e nao uma confianca no saldo de
hoje.

**Lancamento de valor zero nao existe.** Ele nao muda o saldo, mas entraria na soma
do ledger e na contagem de lancamentos da reconciliacao como se fosse movimentacao.
`LOSS` e a operacao sem efeito: existe, e confirmada e auditada, sem lancamento.

**Imutabilidade e no banco:** trigger que proibe `UPDATE` e `DELETE` em
`wallet_ledger_entry`, mais `UNIQUE (wallet_id, transaction_id)`. Correcao
financeira e lancamento novo, nunca edicao.

---

## Eventos

**Decisao:** o envelope leva `eventId`, `eventType`, `aggregateId`, `correlationId`,
`causationId` opcional, `occurredAt` em UTC, `version` e `data` tipado. Tipo e
versao sao definidos pelo construtor do evento.

**`occurredAt` e sempre UTC em RFC 3339** porque o envelope e snapshot imutavel
guardado na outbox e pode ser lido em outra regiao, com outro fuso.

**`eventId` e estavel entre republicacoes.** O relay pode publicar o mesmo registro
da outbox mais de uma vez -- depois de publicar e antes de confirmar -- e o
consumidor precisa poder reconhecer que e o mesmo evento. Por isso o `eventId` e
gerado na escrita da outbox e nao na publicacao.

**Os quatro eventos:** `WagerTransactionProcessed` (inclui `LOSS`),
`WagerTransactionRejected`, `WalletBalanceChanged` (so quando o saldo muda de
verdade) e `WagerTransactionPendingReference`.

---

## Autenticacao e autorizacao

**Decisao:** Keycloak com `client_credentials`. A identidade do provedor vem do
`azp` do token, que e o proprio `client_id`, e a autorizacao e por escopo atribuido
por cliente.

- O token de um provedor carrega `wager:operacoes` e nada mais.
- O token interno `wager-service` carrega `wager:carteira:abertura` e
  `wager:reconciliacao`, e nunca `wager:operacoes`.

**Por que `azp` e nao um claim de role:** o `client_id` do provedor ja e a
identidade dele, e o realm de testeprovisiona um cliente por provedor. Um claim de
role exigiria que o `client_credentials` carregasse role, e o token de
`client_credentials` nao tem usuario: nao ha quem carregue a role. Introduzir um
claim customizado seria uma segunda fonte de identidade, e as duas divergem.

**Por que escopo e nao so `azp`:** sem escopo, "provedor tem tudo" seria o
resultado, e abertura de carteira e reconciliacao ficariam acessiveis a qualquer
provedor. O escopo separa o que e do provedor do que e do servico interno, e a
separacao esta no IdP, e nao em codigo da aplicacao.

**Por que o segredo do cliente esta no realm versionado:** segredo gerado no import
muda a cada recriacao de volume, e o teste de integracao passa a falhar por causa
do ambiente. O segredo de ambiente local de teste e publico por desenho; o que nao
entra no repositorio e chave de ambiente que aponte para servico real.

---

## Persistencia

**Decisao:** `pgx` com SQL explicito, sem ORM e sem `sqlc`, e `goose` nas
migrations.

**Por que sem ORM:** transacao, lock e constraint precisam permanecer explicitos e
verificaveis, que e o que o enunciado pede. Um ORM esconde a transacao que segura o
`SELECT ... FOR UPDATE` junto com o `INSERT` do lancamento, e e exatamente essa
atomicidade que precisa ser lida no codigo.

**Por que sem `sqlc`:** `sqlc` ajuda a reduzir repeticao de query, mas a consulta
financial deste sistema nao repete: cada uma tem lock, condicao de versao ou
`INSERT ... ON CONFLICT` proprio, e o que se quer e ler isso inteiro, nao
economizar linha.

**A unidade transacional e da aplicacao, nao do repositorio.** O caso de uso abre a
transacao SQL e passa o handle para os repositorios. Um repositorio que abre a
propria transacao impediria o `FOR UPDATE` e o lancamento de compartilharem o mesmo
commit -- que e o que torna a operacao atomica.

---

## Inbox e outbox

**Decisao:** na entrada por SQS, o registro da inbox e a conclusao duravel do
tratamento compartilham a transacao SQL das alteracoes de dominio, do ledger e dos
eventos. A publicacao nunca acontece dentro da transacao financeira.

**Fluxo da saida:** alteracao financeira, transacao, ledger e inbox/outbox no mesmo
commit; o relay le a outbox, publica e confirma a publicacao. Republicacao
preserva o `eventId`.

**A mensagem so e removida da fila depois do commit do tratamento.** Uma falha
entre o commit e o `delete` resulta em reentrega, e a inbox transforma a reentrega
em replay do resultado persistido.

**Disputa por registros da outbox:** `FOR UPDATE SKIP LOCKED`, com lease e
`next_attempt_at` para backoff. `SKIP LOCKED` permite varios publishers sem que um
espero o outro, e e o que permite recuperar trabalho abandonado depois de uma
interrupcao.

---

## Pendencias registradas

As decisoes abaixo ainda nao foram fechadas e precisam ser registradas aqui no
momento em que forem:

- `statement_timeout` e `lock_timeout` do pool: com contencao, a transacao deve
  falhar rapido em vez de segurar a requisicao.
- `MessageGroupId`, `MessageDeduplicationId`, visibility timeout, receive count e
  limite de tentativas: a serializacao e a deduplicacao no broker nao podem ficar
  implicitas.
- Chave de particionamento e ordenacao da outbox: e o que decide se `SKIP LOCKED`
  publica em ordem.
