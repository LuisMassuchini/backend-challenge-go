# Rastreabilidade: requisito do enunciado e teste que o executa

Este arquivo liga **cada exigencia do enunciado** ao **teste que a executa**. Nao e
indice de arquivos nem resumo do que existe: cada linha nomeia um requisito do
`README.md` (que e o enunciado, intacto) e o teste que roda e falha se o requisito
quebrar.

O que a auditoria em `docs/auditoria.md` organiza por **categoria** -- eliminatorio,
criterio, cenario -- este arquivo organiza por **requisito**. As duas visoes se
complementam: a auditoria diz o que a nota cobre, este diz onde cada frase do enunciado
ganhou sua prova.

**Como ler uma linha.** `Teste` e o que voce executa. `Cobertura` e o que o teste prova de
verdade -- e as vezes e menos do que o nome sugere, que e o ponto.

**453 funcoes de teste**: 281 de unidade e 172 de integracao, mais os subtestes de
tabela. As de unidade nao usam container; as de integracao precisam de `docker compose
up -d` e rodam com a tag `integration`.

---

## Secao 2 -- Autenticacao e autorizacao

| Requisito | Teste | Cobertura |
|---|---|---|
| Integracao com IdP externo, sem emitir token proprio | `oidc.TestTokenRealDeProvedorVirarAtor` | token do Keycloak real virando ator |
| Validacao de token contra o IdP real | `oidc.TestTokenAdulteradoERecusadoPeloIdPReal` | adulteracao recusada pelo IdP de verdade |
| Chave errada recusada | `oidc.TestTokenAssinadoPorChaveEstranhaERecusadoPeloIdPReal` | assinatura de chave que nao e do realm |
| Rejeicao de token invalido ou expirado | `auth.TestTokenExpiradoERecusado`, `auth.TestTokenAssinadoPorOutraChaveERecusado`, `auth.TestTokenSemAssinaturaERecusado` | os tres caminhos que o enunciado nomeia |
| Emissor e audiencia conferidos | `auth.TestEmissorEAudienciaErradosSaoRecusados` | token de outro realm e token de outro cliente |
| Rotacao de chave entra em vigor sem reiniciar | `auth.TestRotacaoDeChaveEntraEmVigor` | e o que o cache de 5 minutos promete |
| Identidade determina o `providerId` autorizado | `auth.TestTokenDeProvedorViraAtor`, `oidc.TestProvedoresDoRealmSaoAtoresDiferentes` | o `azp` decide o papel |
| Provedor acessa apenas as proprias transacoes | `e2e.TestProvedorNaoLeTransacaoDeOutro`, `apiteste.TestProvedorNaoLeTransacaoDeOutro` | leitura negada, com o token de verdade |
| Provedor nao escreve em nome de outro | `e2e.TestProvedorNaoEscreveEmNomeDeOutro`, `apiteste.TestProvedorNaoOperaEmNomeDeOutro` | escrita negada |
| Provedor nao abre carteira nem reconcilia | `e2e.TestProvedorNaoAbreNemReconcilia`, `apiteste.TestProvedorNaoAbreCarteira`, `apiteste.TestProvedorNaoReconcilia` | operacoes internas restritas ao servico |
| Servico interno nao opera apostas | `apiteste.TestClienteInternoNaoOperaApostas` | o inverso do anterior |
| Recusa de credencial ausente | `httpapi.TestRotaDeNegocioSemTokenE401`, `oidc.TestSemCabecalhoDeAutorizacaoDaErroProprio` | 401 e nao 404 |
| Ausencia de efeito financeiro em acesso nao autorizado | `casos.TestOperacaoQueFalhaNaoDeixaNemTransacaoNemLancamento` | a operacao recusada nao deixa rastro |
| Escolha do IdP e do modelo de permissoes justificada | `ARCHITECTURE.md`, secao "Autenticacao e autorizacao" | decisao documentada |

## Secao 3 -- Ambientes e falhas

Os seis cenarios de entrega do enunciado. A coluna da direita diz **onde** cada um roda,
porque a escolha de nivel de teste aqui e a parte dificil.

| Cenario | Teste | Onde |
|---|---|---|
| Recebimento repetido, inclusive por HTTP e SQS | `e2e.TestMesmaOperacaoPorHTTPESQSAplicaUmaVez` e o inverso | processo real, 3 instancias |
| Chegada de reversao antes da transacao que referencia | `e2e.TestReversaoAntesDaApostaAssumidaPorOutraInstancia` | processo real, outra instancia retoma |
| Processamento simultaneo da mesma carteira | `e2e.TestDisputaDeDuasApostasDeOitentaSobreSaldoCemEmTresInstancias` | 3 processos |
| Encerramento antes ou depois de um commit | `e2e.TestReencontroDevolveOResultadoPersistidoAposReinicio` (depois), `consumidorteste.TestMensagemComApagamentoFalhoEReentregadaSemMoverDinheiroDuasVezes` (entre commit e remocao) | processo real |
| Publicacao repetida de evento de integracao | `e2e.TestDoisRelaysDisputandoNaoPublicamODuplicado`, `relayteste.TestRepublicacaoPreservaOEventID` | 3 instancias e 2 publishers |
| Indisponibilidade temporaria de PostgreSQL ou SQS | `e2e.TestDisputaDistingueRecusaDeIndisponibilidade`, `persistencia.TestLockTimeoutDoPapelDeRuntimeFalhaRapido` | lock timeout real do PostgreSQL |

## Secao 4 -- Stack e composicao

| Requisito | Teste | Cobertura |
|---|---|---|
| Uber Fx com injecao por construtores | `app.TestConfiguracaoValidaMontaAplicacao` | o grafo monta |
| `fx.Lifecycle` gerencia servidor e workers | `ciclo.TestServicoCompletoSobeEResponde`, `app.TestEncerrarSemSubirNaoRegistraEncerramento` | subida e o registro das transicoes |
| Validacao de configuracao na subida | `app.TestSemBancoASubidaERecusadaComAVariavel`, `app.TestConfiguracaoInvalidaBarraAMontagem`, `app.TestDsnDoDonoERecusadoNaConfiguracao` | o erro nomeia a variavel a arrumar |
| Shutdown interrompe entradas e conclui trabalho | `ciclo.TestShutdownEsperaRequisicaoEmAndamento` | mede o `Stop` e confere que a conexao passa a ser recusada |
| Fechamento das dependencias apos os componentes | `ciclo.TestShutdownEsperaRequisicaoEmAndamento` | ordem de `OnStop` invertida a de registro |
| Dominio independente de Fx, HTTP, SQS e banco | `dominio.TestDominioNaoImportaInfraestrutura` | percorre os fontes do dominio e recusa `fx`, `net/http`, `database/sql`, `pgx`, a SDK da AWS, os casos de uso e qualquer dependencia externa fora da lista |
| Versao do Go declarada no `go.mod` e no `Dockerfile` | `buildinfo.TestModuleCoincideComOModuloDeclaradoEmGoMod` | os tres declaram 1.27.0 |
| `go vet` limpo | `make vet` | portao do Makefile |

**Uma decisao registrada em vez de cumprida:** `fx.Module` nao foi usado, e o argumento
esta em `ARCHITECTURE.md`, secao "Composicao com Fx", e em `docs/auditoria.md` secao 5.3.

---

## Secao 5 -- Garantias obrigatorias

Oito garantias, e sao eliminatorias.

| # | Garantia | Teste | Cobertura |
|---|---|---|---|
| 1 | Dinheiro nunca passa por ponto flutuante | `money.TestParseAceitaValorNormalComDuasCasas`, mais os 20 testes de `internal/dominio/money` | `Money` e `int64` em unidades minimas. Ressalva registrada em `docs/auditoria.md` secao 1: `float64` aparece em `metrica.go`, e o arquivo explica por que |
| 2 | Idempotencia persistente, sobrevive a reinicio | `e2e.TestReencontroDevolveOResultadoPersistidoAposReinicio`, `e2e.TestApostaUnicaSobCinquentaEnviosEmTresInstancias` | 50 envios, um debito; e o resultado persistido volta apos reiniciar o processo |
| 3 | Invariantes financeiras no banco | `persistencia.TestSaldoNegativoRecusadoPeloBanco`, `persistencia.TestLancamentoQueNaoEncadeiaComOAnteriorRecusado` | `CHECK (balance >= 0)` e a trigger de encadeamento |
| 4 | Eventos publicados depois do commit | `relayteste.TestRelayNaoPublicaAntesDoCommit` | o relay le a outbox, e a reserva so existe depois do commit |
| 5 | Ledger append-only | `persistencia.TestLedgerAppendOnlyRecusaUpdateEDelete`, `persistencia.TestPapelDeRuntimeNaoAlteraLancamento` | trigger e privilegio do papel de runtime |
| 6 | Carteiras independentes em paralelo, sem lock global | `e2e.TestCarteirasDistintasProcessamEmParalelo`, `consumidorteste.TestCarteirasDiferentesSaoProcessadasEmParalelo` | o lock e por carteira |
| 7 | Sem lost update de saldo | `repositorios.TestAtualizarSaldoEcondicionalPorVersao` (no repositorio), `repositorios.TestAtualizacaoComVersaoDesatualizadaDaConflito` | a versao e condicional no `UPDATE` |
| 8 | Unicidade, nao negatividade e imutabilidade no schema | `persistencia.TestUnicidadeDeJogadorEMoeda`, `TestUnicidadeDeChaveDeIdempotencia`, `TestUnicidadeDeProvedorEIdExterno`, `TestUnicidadeDoLancamentoPorTransacao`, `TestSaldoNegativoRecusadoPeloBanco` | cada invariante com o constraint que a garante |

## Secao 6.1 -- Money

| Requisito | Teste |
|---|---|
| Value object imutavel com valor e moeda | `money.TestSerializaComDuasCasas`, `money.TestIgualdadeEZero` |
| Criacao a partir de string decimal | `money.TestParseAceitaValorNormalComDuasCasas` |
| Escala fixa de duas casas | `money.TestParseRejeitaEscalaExcedente` |
| Codigo ISO 4217 | `money.TestMoedaValidaAceitaCodigoConhecido`, `money.TestParseRejeitaMoedaDesconhecida` |
| Rejeita vazio, `NaN`, `Infinity`, notacao cientifica, escala excedente, negativo nas entradas financeiras | `money.TestParseRejeitaFormatoInvalido`, `money.TestParseRejeitaValorZeroNaoInicializado`; o `Parse` rejeita `NaN` e `Inf` por nao serem numericos |
| Zero por moeda | `money.TestZeroPorMoeda` |
| Soma, subtracao, negacao, comparacao | `money.TestSomaValoresDaMesmaMoeda`, `TestSubtracao`, `TestNegacao`, `TestComparacao` |
| Aritmetica exige moedas compativeis | `money.TestOperacoesEntreMoedasDiferentesRecusam` |
| Overflow no parsing, soma, subtracao e negacao | `money.TestParseRecusaOverflow`, `TestSomaRecusaOverflow`, `TestSubtracaoRecusaOverflow`, `TestNegacaoDoMenorValorRecusa` |
| Negativo permitido em diferencas internas, nao no saldo | `money.TestParseAceitaValorNegativo` e `wallet.TestDebitoInsuficienteRecusaSemAlterarSaldo` |
| Persistencia preserva valor e moeda | `repositorios.TestInserirELerLancamento` |
| Normalizacao antes do hash de idempotencia documentada | `fingerprint.TestDocumentacaoCobreOsCampos`, `fingerprint.TestValorCanonicoProduzOMesmoResumo` |

## Secao 6.2 -- Wallet

| Requisito | Teste |
|---|---|
| Identidade, jogador, moeda, saldo, versao e instantes | `wallet.TestNovaCarteiraComSaldoInicialPositivo` |
| Criacao e reidratacao separadas; reidratacao nao reaplica movimentacao | `wallet.TestReidratacaoPreservaEstadoLidoDoBanco`, `wallet.TestReidratacaoNaoVersiona` |
| `(playerId, currency)` identifica uma unica carteira | `persistencia.TestUnicidadeDeJogadorEMoeda`, `casos.TestSegundaAberturaDoMesmoJogadorDaConflito` |
| Debito preserva saldo maior ou igual a zero | `wallet.TestDebitoInsuficienteRecusaSemAlterarSaldo`, `persistencia.TestSaldoNegativoRecusadoPeloBanco` |
| Moeda da movimentacao coincide com a da carteira | `wallet.TestOperacaoEmMoedaIncompativelRecusa`, `money.TestOperacoesEntreMoedasDiferentesRecusam` |
| Cada mudanca exige lancamento confirmado junto com o saldo | `casos.TestAberturaComSaldoPositivoCriaTudoNoMesmoCommit`, `persistencia.TestLancamentoSemAtualizacaoDaCarteiraRecusadoNoCommit` |
| Versao inicial `1`, incrementa so quando o saldo muda | `wallet.TestVersaoSobeACadaMudanca`, `wallet.TestOperacaoSemEfeitoNaoIncrementaVersao` |
| Disputas nao descartam atualizacao confirmada | `repositorios.TestAtualizacaoComVersaoDesatualizadaDaConflito`, `e2e.TestDisputaDeDuasApostasDeOitentaSobreSaldoCemEmTresInstancias` |
| Estrategia de concorrencia documentada | `ARCHITECTURE.md`, secao "Concorrencia" |

## Secao 6.3 -- WagerTransaction

| Requisito | Teste |
|---|---|
| Tipos `OPENING`, `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK` | `wagering.TestTiposExternosLegiveis`, `wagering.TestEfeitoFinanceiroPorTipo` |
| Origem externa com todos os identificadores; `OPENING` sem identidade externa | `wagering.TestAberturaNaoCarregaIdentidadeExterna`, `persistencia.TestAberturaComIdentidadeExternaRecusada` |
| Schema distingue origem interna e externa | `persistencia.TestAberturaComIdentidadeExternaRecusada` |
| Impede credito inicial duplicado | `persistencia.TestCreditoInicialDuplicadoRecusado` |
| Nasce em `PENDING` | `wagering.TestTransacaoNascePendente` |
| Transicoes para processada, rejeitada e falha | `wagering.TestTransicaoParaProcessada`, `TestTransicaoParaRejeitada`, `TestTransicaoParaFalha` |
| Estado terminal nao aceita nova transicao | `wagering.TestEstadoTerminalNaoAceitaNovaTransicao`, `repositorios.TestTransacaoTerminalNaoVoltaAoNaoTerminal` |
| `PENDING_REFERENCE` com validacao | `wagering.TestEsperaPorReferenciaSemReferenciaRecusa` |
| Replay consulta resultado persistido sem reaplicar | `repositorios.TestResultadoOriginalEPersistido`, `e2e.TestReencontroDevolveOResultadoPersistidoAposReinicio` |
| Reidratacao restaura o estado terminal | `wagering.TestReidratacaoRepoeEstadoTerminal` |
| Codigo de falha persistido | `repositorios.TestRejeicaoPersisteCodigoDeFalha` |
| `OPENING` recusado quando enviado por HTTP ou SQS | `wagering.TestAberturaPelaOrigemExternaRecusa`, `wagering.TestAberturaExternaTemCodigoDeFalhaProprio` |
| Politica de valor por tipo no schema | `persistencia.TestPoliticaDeValorPorTipo` |

## Secao 6.4 -- WalletLedgerEntry

| Requisito | Teste |
|---|---|
| Campos `id`, `walletId`, `transactionId`, direcao, valor, saldos e instante | `wallet.TestLancamentoCarregaOsIdentificadoresDaUnicidade`, `persistencia.TestDirecaoEValorDoLancamento` |
| `balanceAfter = balanceBefore mais ou menos money`, validado na construcao | `wallet.TestLancamentoRecusaSaldoPosteriorIncompativel`, `persistencia.TestLancamentoComSaldoPosteriorErradoRecusado` |
| Encadeamento conferido pelo banco | `persistencia.TestSequenciaDeLancamentosEncadeia`, `TestLancamentoQueNaoEncadeiaComOAnteriorRecusado` |
| Unicidade de `(walletId, transactionId)` | `persistencia.TestUnicidadeDoLancamentoPorTransacao` |
| Protecao contra edicao e exclusao | `persistencia.TestLedgerAppendOnlyRecusaUpdateEDelete`, `privilegio_test.go` inteiro |
| `LOSS` nao produz lancamento | `wallet.TestOperacaoRecusadaNaoProduzLancamento`, `casos.TestLossNaoMoveSaldoNemPublicaMudancaDeSaldo` |

## Secao 6.5 -- Inbox e outbox

| Requisito | Teste |
|---|---|
| Inbox com identidade da mensagem e do consumidor, hash, recebimento e conclusao | `repositorios.TestInboxConcluiEmissao`, `persistencia.TestInboxPorConsumidorEMensagem` |
| Unicidade de `(consumerName, messageId)` | `repositorios.TestReentregaDaMesmaMensagemNaoEError` |
| Outbox com identidade estavel, agregado, tipo, payload, ocorrencia, tentativas e proximo envio | `repositorios.TestInserirEBuscarTransacaoPorChave`, `relayteste.TestChaveDeParticaoEoAgregado` |
| Inbox e alteracoes de dominio na mesma transacao SQL | `repositorios.TestInboxEOutboxNaMesmaUnidade`, `TestFalhaNaUnidadeNaoDeixaNemInboxNemOutbox` |
| Mensagem concluida apos a pendencia persistida | `e2e.TestReversaoAntesDaApostaAssumidaPorOutraInstancia`, `consumidorteste.TestMensagemComApagamentoFalhoEReentregadaSemMoverDinheiroDuasVezes` |

---

## Secao 7 -- Operacoes e referencias

| Requisito | Teste |
|---|---|
| `BET`: debito, valor positivo, saldo suficiente | `casos.TestBetDebitaOLancaEeventua`, `casos.TestBetSemSaldoERecusaComCodigoEConfirmaOCommit` |
| `WIN`: credito, pode informar aposta da mesma rodada | `casos.TestWinCreditaOValorApostado` |
| `LOSS`: sem movimentacao, exige `"0.00"` | `wagering.TestLossRecusaValorDiferenteDeZero`, `wagering.TestLossAceitaValorZero`, `casos.TestLossNaoMoveSaldoNemPublicaMudancaDeSaldo` |
| `LOSS` exige a moeda da carteira e produz `WagerTransactionProcessed` sem `WalletBalanceChanged` | `wagering.TestLossExigeMoedaDaCarteira`, `eventos.TestTransacaoProcessadaTemOsCamposDoContrato` |
| `REFUND`: credito integral de uma `BET` processada | `wagering.TestRefundSoPodeReferenciarBet`, `casos.TestRollbackDeWinDesfazOCredito` |
| `ROLLBACK`: movimento contrario, de `BET`, `WIN` ou `REFUND` | `wagering.TestRollbackPodeReferenciarBetWinOuRefund` |
| `referenceExternalTransactionId` obrigatorio nas reversoes | `persistencia.TestReferenciaObrigatoriaNasReversoes` |
| Resolucao por `(providerId, referenceExternalTransactionId)` | `repositorios.TestBuscarPorProvedorEExterno` |
| Operacao e referencia concordam em provedor, jogador, carteira, moeda e rodada | `wagering.TestReversaoExigeConcordanciaEntreOperacaoEReferencia` |
| Valor da reversao igual ao referenciado | `wagering.TestReversaoComValorDivergenteRecusa` |
| Referencia nao recebe duas reversoes do mesmo tipo | `wagering.TestBetNaoPodeSerNeutralizadaDuasVezes`, `wagering.TestReversaoDeWinAceitaApenasUmRollback`, `pendenciasteste.TestSegundaReversaoDaMesmaApostaERecusadaNaHora` |
| Combinacao de `REFUND` e `ROLLBACK` sobre a mesma aposta | `wagering.TestReversaoDeWinAceitaApenasUmRollback` |
| Reversao que estouraria o saldo: recusada, auditavel, codigo proprio | `wagering.TestRollbackQueEstourariaSaldoRecusaComCodigoProprio`, `casos.TestRollbackSemSaldoRecusaComCodigoProprio` |
| Codigo de `BET` sem saldo diferente do de reversao sem saldo | `casos.TestBetSemSaldoERecusaComCodigoEConfirmaOCommit` e `casos.TestRollbackSemSaldoRecusaComCodigoProprio`, comparados |
| `PENDING_REFERENCE` quando a referencia nao chegou | `casos.TestReversaoAntesDaApostaFicaPendente` |
| Retentativa com backoff exponencial | `pendenciasteste.TestIntervaloDobraAteOTeto`, `TestPendenciaSemReferenciaEAgendadaComIntervaloCrescente` |
| Retomada apos reinicializacao | `pendenciasteste.TestRetomadaNaoDuplicaDinheiro`, `e2e.TestReversaoAntesDaApostaAssumidaPorOutraInstancia` |
| TTL: esgota tentativas e finaliza `REJECTED` | `pendenciasteste.TestPendenciaExpiraDepoisDoMaximoDeTentativas` |
| Expiracao nao se confunde com recusa | `pendenciasteste.TestExpiracaoNaoSeConfundeComRecusa` |
| Expiracao nao duplica dinheiro | `consumidorteste.TestReversaoAntesDaApostaFicaPendenteEContinuaAposAReferenciaChegar` |
| Referencia pendente, depois resolvida | `pendenciasteste.TestReversaoInvertidaERetomadaQuandoAApostaChega` |
| `failureCode` estavel e documentado | `repositorios.TestRejeicaoPersisteCodigoDeFalha`, `httpapi.TestFalhaDeRegraCarregaOCodigoDeDominio` |

## Secao 8 -- Concorrencia

| Requisito | Teste |
|---|---|
| Pelo menos tres processos independentes | `e2e` inteiro: cada cenario sobe tres `fx.App` com portas efemeras |
| Duas apostas de 80 sobre saldo de 100 | `e2e.TestDisputaDeDuasApostasDeOitentaSobreSaldoCemEmTresInstancias` |
| Uma processada, uma recusada, saldo 20, um debito | o mesmo teste |
| Reenvios nao alteram o resultado | `e2e.TestApostaUnicaSobCinquentaEnviosEmTresInstancias` |
| Carteiras diferentes em paralelo | `e2e.TestCarteirasDistintasProcessamEmParalelo` |
| Lock por carteira, sem global | `repositorios.TestLockPorCarteiraSerializaEscritores` |
| Lock esgotado falha rapido | `persistencia.TestLockTimeoutDoPapelDeRuntimeFalhaRapido` |

---

## Secao 9 -- Contratos HTTP

| Requisito | Teste |
|---|---|
| `POST /wallets` cria `OPENING`, lancamento e outbox no mesmo commit | `casos.TestAberturaComSaldoPositivoCriaTudoNoMesmoCommit`, `apiteste.TestAberturaPeloHTTPCriaCarteira` |
| Saldo inicial zero nao cria nada | `casos.TestAberturaComSaldoZeroNaoCriaNemMovimentacaoNemEvento` |
| Segunda carteira do mesmo jogador: conflito | `apiteste.TestSegundaCarteiraDoMesmoJogadorE409` |
| `GET /wallets/:id` e `GET /wallets/:id/ledger` | `apiteste.TestLeituraDaCarteiraDepoisDaOperacao`, `TestLedgerPaginaComCursor` |
| Paginao por cursor opaco e ordenacao estavel | `repositorios.TestPaginacaoPercorreTodoOLedgerSemRepetirNemPular`, `TestPaginacaoComLancamentosNoMesmoInstante`, `TestCursorDeidaVolta` |
| `GET /wagering/transactions/:id` | `apiteste.TestLeituraPorIdentificadorExterno` |
| `GET /providers/:id/...` | `apiteste.TestLeituraPorIdentificadorExterno`, `TestProvedorNaoLeTransacaoDeOutro` |
| Leitura inexistente: 404 | `apiteste.TestLeituraDeTransacaoInexistenteE404` |
| `POST /wagering/transactions` com `Idempotency-Key` obrigatorio | `httpapi.TestOperacaoSemChaveDeIdempotenciaE400`, `apiteste.TestOperacaoSemChaveERecusada` |
| Chave e conteudo equivalentes: resultado persistido, `idempotentReplay: true` | `casos.TestReenvioDaMesmaChaveNaoMoveDinheiro`, `apiteste.TestReenvioDaMesmaChaveNaoMoveDinheiro` |
| Chave reusada com conteudo diferente: conflito | `casos.TestMesmaChaveComConteudoDiferenteEConflito`, `apiteste.TestChaveReusadaComConteudoDiferenteE409` |
| Mesma operacao externa com outra chave: nao reaplicada | `apiteste.TestMesmaOperacaoExternaComOutraChaveNaoEReaplicada`, `e2e.TestConteudoDiferenteComAMesmaChaveEConflito` |
| Replay devolve o saldo do processamento original | `e2e.TestReencontroDevolveOResultadoPersistidoAposReinicio` |
| Reconciliacao nao altera o saldo | `apiteste.TestReconciliacaoNaoAlteraOSaldo` |
| Divergencia reportada na resposta | `casos.TestReconciliacaoDetectaSaldoDivergenteDoLedger` |
| Reconciliacao exige cliente interno com escopo | `casos.TestReconciliacaoExigeClienteInternoComEscopo` |
| Health checks publicos | `httpapi.TestHealthChecksSaoPublicos`, `ciclo.TestServicoCompletoSobeEResponde` |
| Liveness nao consulta dependencia | `httpapi.TestLivenessNaoConsultaDependencia` |
| Readiness consulta dependencia | `httpapi.TestReadinessConsultaDependencia` |
| Entrada invalida: 400 com o campo | `httpapi.TestCorpoInvalidoE400`, `apiteste.TestValorInvalidoE400`, `TestTipoDesconhecidoE400` |
| Recusa de negocio: 422 com `failureCode` | `apiteste.TestApostaSemSaldoE422ComCodigoDeDominio` |
| Pendencia: 202 | `httpapi.TestTraducaoDeErrosFixaOContratoDeStatus`, caso "referencia ausente" |
| Conflito: 409 | `httpapi.TestTraducaoDeErrosFixaOContratoDeStatus`, caso "conflito de chave" |
| Indisponibilidade transitoria: 503 dizendo que repetir e seguro | `httpapi.TestIndisponibilidadeTransitoriaDizQueRepetirESeguro` |
| As situacoes sao distinguiveis entre si | `httpapi.TestTraducaoDeErrosFixaOContratoDeStatus` |
| Nenhum erro vem com 200 | `apiteste.TestRespostasDeErroNaoVemCom200` |
| Content-Type conferido | `httpapi.TestContentTypeErradoERecusado` |
| Metodo errado: 405, rota inexistente: 404 | `httpapi.TestMetodoErradoE405`, `TestRotaInexistenteE404` |

## Secao 10 -- Consumidor SQS

| Requisito | Teste |
|---|---|
| Filas FIFO e de cartao morto provisionadas com redrive | `deploy/localstack/init/00-filas.sh`, verificado por `consumidorteste.TestOperacaoNaFilaDesbitaOSaldo` (a fila precisa existir) |
| `MessageGroupId` e ordem por particao | `consumidorteste.TestOperacoesDaMesmaCarteiraSaoAplicadasEmOrdem` |
| `MessageDeduplicationId` deduplica | `consumidorteste.TestDeduplicacaoDaFilaBloqueiaAMesmaChaveDeDeduplicacao` |
| `messageId` como identidade duravel, hash conferido | `repositorios.TestReentregaDaMesmaMensagemNaoEError` |
| Chave e `data.idempotencyKey` | `consumidorteste.TestChaveRepetidaMoveDinheiroUmaVez` |
| Remocao da mensagem so depois do commit | `consumidorteste.TestMensagemComApagamentoFalhoEReentregadaSemMoverDinheiroDuasVezes` |
| Mensagem malformada sai da fila | `consumidorteste.TestMensagemSemChaveEDescartada`, `TestMensagemNaoJsonEDescartada` |
| Falha transitoria volta para a fila | `consumidorteste.TestMensagemComApagamentoFalhoEReentregadaSemMoverDinheiroDuasVezes` |
| `SIGTERM` para de buscar e conclui o trabalho | `ciclo.TestShutdownEsperaRequisicaoEmAndamento`, `app.TestEncerrarSemSubirNaoRegistraEncerramento` |
| Limites de tentativa e visibility timeout documentados | `ARCHITECTURE.md`, secao "Inbox e outbox"; `docs/auditoria.md` secao 6.2 |
| Concorrencia entre HTTP e SQS | `e2e.TestMesmaOperacaoPorHTTPESQSAplicaUmaVez` e o inverso |

## Secao 11 -- Publicacao com transactional outbox

| Requisito | Teste |
|---|---|
| Estado, saldo, ledger, inbox e eventos confirmados atomicamente | `repositorios.TestInboxEOutboxNaMesmaUnidade` |
| Worker separado publica | `relayteste.TestRelayPublicaEConfirma` |
| Publicacao nunca antes do commit | `relayteste.TestRelayNaoPublicaAntesDoCommit` |
| Multiplos publishers disputando | `relayteste.TestDoisRelaysNaoPublicamODuplicado`, `e2e.TestDoisRelaysDisputandoNaoPublicamODuplicado` |
| Backoff e recuperacao | `relayteste.TestFalhaNaPublicacaoReprograma`, `TestTentativasEsgotadasParamARetomada` |
| Trabalho abandonado assumido por outra instancia | `relayteste.TestRelayAssumeReservaVencida`, `repositorios.TestReservaVencidaEhAssumidaPorOutro`, `e2e.TestEventoPendenteEAssumidoPorOutraInstanciaAposReinicio` |
| Republicacao preserva o `eventId` | `relayteste.TestRepublicacaoPreservaOEventID`, `eventos.TestEventIDEEstableEntrePublicacoes` |
| Envelope com todos os campos do contrato | `eventos.TestEnvelopeTemTodosOsCamposDoContrato`, `TestEnvelopeSerializaTodosOsCamposDoContrato` |
| `WagerTransactionProcessed` | `eventos.TestTransacaoProcessadaTemOsCamposDoContrato` |
| `WagerTransactionRejected` | `eventos.TestTransacaoRejeitadaTemOCodigoDeFalha` |
| `WalletBalanceChanged` com todos os campos | `eventos.TestSaldoAlteradoTemTodosOsCamposDaMudanca` |
| `WagerTransactionPendingReference` | `eventos.TestPendenteReferenciaTemAReferenciaQueFalta` |
| Payload e snapshot imutavel | `eventos.TestPayloadEUmSnapshotImutavel`, `TestPayloadDevolveCopia` |
| Timestamps em UTC e RFC 3339 | `eventos.TestOcorreuEmSempreEmUTC` |

## Secao 12 -- Observabilidade

| Requisito | Teste |
|---|---|
| Logs JSON com `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId` | `obs.TestLogCarregaOsIdentificadoresDoFluxo`, `obsteste.TestCorrelacaoDoClienteAtravessaAOperacao` |
| Sem credenciais nem payload financeiro | `obs.TestCampoDesconhecidoNaoAparece`, `obs.TestValorFinanceiroViraIndicadorSemValor`, `obsteste.TestMetricasNaoExpoemIdentificadorDeCliente` |
| Metricas de resultado, duplicatas, retries, DLQ, conflitos, atraso da outbox, latencia e divergencia | `obsteste.TestMetricasDoEnunciadoEstaoExpostas` |
| Health checks | `httpapi.TestHealthChecksSaoPublicos` |
| `/metrics` publico | `obsteste.TestMetricasNaoExigeToken` |
| Correlacao do cliente atravessa e volta | `obsteste.TestRespostaDevolveACorrelacaoDoCliente`, `TestCorrelacaoAusenteEGerada` |
| Medicao de latencia sob carga | `docs/carga.md` |

## Secao 13 -- Verificacao obrigatoria

Os oito testes que o enunciado pede nominalmente.

| # | Exigido | Teste |
|---|---|---|
| 1 | Mesma aposta 50 vezes em paralelo, um unico debito | `e2e.TestApostaUnicaSobCinquentaEnviosEmTresInstancias` |
| 2 | Disputa das duas apostas de 80 sobre 100 | `e2e.TestDisputaDeDuasApostasDeOitentaSobreSaldoCemEmTresInstancias` |
| 3 | Carteiras distintas simultaneamente | `e2e.TestCarteirasDistintasProcessamEmParalelo` |
| 4 | Repetir com tres instancias independentes | `e2e` inteiro sobe tres instancias por cenario |
| 5 | Interromper depois do commit e antes da remocao da mensagem | `consumidorteste.TestMensagemComApagamentoFalhoEReentregadaSemMoverDinheiroDuasVezes` |
| 6 | Dois publishers disputando a mesma outbox | `e2e.TestDoisRelaysDisputandoNaoPublicamODuplicado` |
| 7 | `REFUND` ou `ROLLBACK` antes da referencia | `e2e.TestReversaoAntesDaApostaAssumidaPorOutraInstancia`, `consumidorteste.TestReversaoAntesDaApostaFicaPendenteEContinuaAposAReferenciaChegar` |
| 8 | Reinicio com idempotencia, pendencias e consistencia preservadas | `e2e.TestReencontroDevolveOResultadoPersistidoAposReinicio`, `TestEventoPendenteEAssumidoPorOutraInstanciaAposReinicio` |
| -- | Conferir saldo contra a soma do ledger | `casos.TestReconciliacaoDetectaSaldoDivergenteDoLedger` |
| -- | Cruzamento HTTP e SQS | `e2e.TestMesmaOperacaoPorHTTPESQSAplicaUmaVez` e o inverso |
| -- | `go test -race` | `make test-race-docker` |

---

## Onde os requisitos nao tem teste dedicado

Tres, e todos registrados como pendentes em `docs/auditoria.md`:

1. **Ledger de partidas dobradas.** O enunciado diz que e opcional. Nao implementado.
2. **Consumidor de demonstracao da `wager-events.fifo`.** O relay publica e ninguem le. O
   enunciado pede documentar o contrato de consumo, e o contrato esta documentado -- falta
   um leitor que o exercite.
3. **`fx.Module`.** Decisao registrada, nao implementada. Argumento em `ARCHITECTURE.md`.
