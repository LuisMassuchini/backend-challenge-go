# Convencoes do repositorio

Este arquivo registra as convencoes de commit, de branch e o checklist de criteria de
avaliacao do desafio. Ele existe porque o historico Git e parte da apresentacao tecnica:
quem avalia a solucao le o `git log` antes de ler o codigo.

O enunciado original, que nao muda, esta em [`docs/challenge.md`](challenge.md).

## Idioma

- **Mensagens de commit, documentacao e comentarios**: portugues do Brasil.
- **Sem acentos e sem cedilha** em mensagem de commit, nome de arquivo, identificador e
  documento versionado. O historico precisa ser legivel em terminal, diff e log sem
  depender de fonte, e o padrao adotado aqui evita que a diferenca de charset vire ruido
  em revisao.
- **Excecoes, que permanecem como o padrao da plataforma**: codigo, identificadores,
  mensagens de erro de framework, `commit`/`push`/`build`, nomes de campos de JSON e
  nomes proprios de biblioteca (`Money`, `Wallet`, `SQS`, `Fx`).
- Termo de negocio cujo termo ingles seja mais claro no dominio fica em ingles; nao ha
  traducao forcada.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/), com o **tipo em ingles** e a
**descricao em portugues**.

```text
<tipo>(<escopo>): <descricao no imperativo>

<corpo: por que, nao o que. Corpo opcional, mas obrigatorio quando
a decisao teve trade-off.>
```

- **Um commit por comportamento real**, com uma unica intencao tecnica. Um commit que
  introduz erro e outro que o corrige, sem estado intermediario observavel, sao dois
  commits sem valor.
- **Teste e implementacao no mesmo commit.** O ciclo TDD e vermelho, verde, refatoracao, mas
  nunca se commita codigo vermelho: o commit representa o comportamento, nao o momento do
  ciclo. O teste e escrito e executado primeiro, para falhar pela razao esperada.
- **O nome descreve o comportamento, nao o metodo:**
  `feat(dominio): Money com parsing decimal estrito e rejeicao de formato invalido`.
- **Commit de correcao explica o defeito.** Por isso nao existe commit `fix:` planejado:
  um `fix:` so e escrito quando o defeito existe, e o corpo diz por que ele estava la.
- **Sem `--amend`, sem squash do historico, sem commit vazio.**

### Tipos

| Tipo | Quando |
|---|---|
| `feat` | comportamento novo |
| `fix` | defeito corrigido |
| `refactor` | estrutura mudada sem mudar comportamento |
| `perf` | ganho medido |
| `test` | suite de caracterizacao, correcao de teste, ou cenarios de integracao, concorrencia e recuperacao |
| `docs` | documentacao |
| `build` | Makefile, Dockerfile, `go.mod`, toolchain |
| `chore` | higiene de repositorio que nao entra no build |
| `ci` | integracao continua |

O escopo `test(...)` e reservado. Nao e TDD: ele cobre suite que outliva a mudanca,
correcao ou reforco de teste existente, e os cenarios de integracao, concorrencia e
recuperacao, que so existem depois que a infrastrutura real esta montada.

### Escopos

Escopo por camada, nao por pacote:

```text
dominio  app  db  pg  eventos  outbox  sqs  wagering
fx  http  auth  obs  docker  idp  build  ci  perf  docs  chore
```

`repo` e aceito para alteracao de higiene que nao pertence a nenhuma camada.

### Portao por commit

Nenhum commit entra sem que os quatro comandos abaixo passem:

```sh
make verify
```

que expande para `gofmt -l` vazio, `go vet ./...`, `go build ./...` e `go test ./...`.

## Branches

- `main` e o trunk e nao recebe trabalho direto. O que entrega e verificado mora no
  `main`.
- Branch curta, uma por etapa do plano, nomeada `<tipo>/<etapa>-<descricao>`:

  ```text
  feat/e10-oidc-autenticacao
  feat/e13-consumidor-sqs
  chore/e0-bootstrap
  ```

  A sigla da etapa no nome e o que torna as 22 etapas legiveis no historico.
- A branch entra no `main` por merge `--no-ff`, nunca por squash nem por rebase. O commit
  de merge marca a fronteira da etapa, e um historia linear apaga justamente essa
  informacao.
- Branch nao vive mais que a etapa. Aberta no inicio da etapa, fechada no portao dela.

## Checklist de criterios de avaliacao

Oito criterios,cem pontos. A coluna da direita diz onde a evidencia nasce; o portao real de
cada etapa esta no `state.md` da tarefa.

| Criterio | Pontos | Evidencia | Etapa |
|---|---:|---|---|
| Integridade financeira | 20 | `Money` exato, dominio encapsulado, constraints no banco, transacao unica, reconciliacao | E3, E4, E7, E9, E17 |
| Concorrencia | 20 | lock por carteira, banco como autoridade, sem lock global | E7, E8, E17 |
| Idempotencia | 15 | dois indices, fingerprint canonico, resultado persistido, inbox | E7, E9, E13, E17 |
| Mensageria e recuperacao | 15 | FIFO, inbox, outbox, retry, DLQ, shutdown seguro | E13, E15, E17 |
| Modelagem e arquitetura | 10 | dominio isolado, Fx por modulos, `Actor` na borda | E1, E12, E18 |
| Testes | 10 | infra real, `-race`, multiplos processos, auth real | E17 |
| Observabilidade | 5 | logs JSON com correlacao, metricas, health checks | E16 |
| Documentacao | 5 | README, ARCHITECTURE, relatorio de carga | E19, E20, E21 |

### Eliminatorios

Estes nao sao pontos: reprovam. Cada um e um portao que vale mais que a soma dos criterios
na margem.

- [ ] Autenticacao efetiva nos endpoints de negocio.
- [ ] Nenhum acesso nao autorizado a operacoes ou transacoes.
- [ ] Nenhum calculo monetario em ponto flutuante.
- [ ] Nenhum saldo negativo por concorrencia.
- [ ] Nenhuma movimentacao duplicada.
- [ ] Idempotencia nunca restrita a memoria.
- [ ] Nenhuma dependencia de instancia unica para funcionar corretamente.
- [ ] Nenhuma publicacao de evento anterior ao commit.
- [ ] Ledger auditavel sempre presente.
- [ ] PostgreSQL, SQS e IdP nunca substituidos integralmente por mock nos testes.

### Diferenciais opcionais

Partidas dobradas e tracing ficam fora do escopo. O diferencial assumido e o **relatorio de
carga** (E20), que precisa de comando reproduzivel, ambiente, metodologia, throughput,
p50/p95/p99, erros, conflitos de concorrencia e atraso da outbox.
