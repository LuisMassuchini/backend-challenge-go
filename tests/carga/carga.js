// Carga do wager-service.
//
// Objetivo: medir o comportamento sob concorrencia de verdade -- varias threads no mesmo
// pool de carteiras -- e nao apenas requisições em paralelo no vazio. As garantias que
// o enunciado pede (disputa por carteira, sem saldo negativo, idempotencia, ordem por
// chave de particao) so aparecem quando duas requisicoes competem pelo MESMO dinheiro.
//
// O pool de carteiras e pequeno de proposito. Com poucas carteiras e muitas threads, a
// contencao e garantida; com muitas carteiras, cada requisicao tranca e destranca um
// lock que ninguem disputa, e o resultado seria bonito e sem informacao.
//
//   docker run --rm --network wager-service_default \
//     -v "$(pwd)/tests/carga:/carga" -e K6_BASE_URL=http://wager-service:8080 \
//     grafana/k6:0.54.0 run /carga/carga.js
//
// A execucao pelo container e a que o roteiro documenta, e nao o `k6 run` na maquina:
// a imagem fixa a versao, entao o resultado e reproduzivel em qualquer maquina com
// Docker, sem instalar nada. Ver `docs/carga.md` para o comando completo e o resultado.

import http from 'k6/http';
import { sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

// ---------------------------------------------------------------------------
// Metricas proprias
// ---------------------------------------------------------------------------
//
// As do k6 medem o lado do cliente. Estas medem o que o enunciado pede explicitamente:
// conflitos, duplicatas e recusas de saldo sao respostas 2xx, e sem um contador por
// desfecho o relatorio mostraria "zero erros" enquanto metade das operacoes era recusada.

const porEstado = new Counter('carga_operacoes_por_estado');
const porCodigo = new Counter('carga_recusas_por_codigo');
const latencia = new Trend('carga_operacao_latencia_ms', true);

// Quantas vezes uma operacao precisou ser reenviada depois de indisponibilidade.
//
// A contagem existe para responder a pergunta que o `http_req_failed` nao responde: um
// 503 que o cliente reenvia e um sistema que funciona, e um 503 que ele nao reenvia e
// uma transacao perdida. O mesmo codigo HTTP produz as duas situacoes.
const reenvios = new Counter('carga_reenvios_por_indisponibilidade');

// Respostas que o cliente nao pode reenviar.
//
// O complemento exato de "o que o provedor reenvia". Um limiar escrito como "zero
// respostas que falharam" apontaria para o 503, que e o comportamento certo; este aponta
// para o que de fato nao tem recuperacao: entrada invalida, credencial, conflito de
// chave, erro interno.
const semReenvio = new Counter('carga_respostas_sem_reenvio');

// Indisponibilidade que a repeticao nao resolveu.
//
// E separada de `semReenvio` porque sao coisas diferentes e misturar as duas esconderia
// uma dentro da outra. Um 503 que o cliente reenvia e um sistema sob contencao. Um 503
// que o cliente reenvia tres vezes e ainda recebe 503 e um sistema mais carregado do que
// o cliente espera -- e o operador precisa ver os dois numeros para saber em que regime
// esta.
const indisponivelDepoisDoTeto = new Counter('carga_indisponivel_apos_teto');

// O estado de sucesso da operacao.
//
// `PROCESSED` e `PENDING` sao sucesso; `REJECTED` e um desfecho legitimo que o
// provedor trata esperando deposito. Os tres sao respostas que o sistema produziu
// corretamente, entao nenhum deles entra em `carga_respostas_sem_reenvio`.
const RESPOSTA_TRATADA = [200, 201, 202];

// O limite de reenvios.
//
// `lock_timeout` de um segundo significa que a transacao pode perder o lock da carteira
// e devolver 503. O provedor reenvia; tres tentativas cobrem a janela com folga sem
// transformar o teste em espera. O que o limite protege e o relatorio: um reenvio sem
// teto transformaria contencao em hang, e um hang em um relatorio vazio.
const MAX_REENVIOS = 3;

// O codigo de indisponibilidade transitoria.
//
// E uma constante local e nao `http.StatusServiceUnavailable` porque o `http` do k6 e
// o modulo `k6/http`, e nao o `http` do Node: ele nao tem os constantes de status, e a
// comparacao `503 === undefined` e falsa sem erro nenhum. Esse bug custou uma rodada
// inteira de carga em que os 503 esgotados apareciam como "resposta sem reenvio".
const INDISPONIVEL = 503;

// O periodo de escolha das carteiras e 1 segundo, com o objetivo em cenarios
// graduais. O numero exato depende de quantas threads a maquina aguenta, entao ele
// vem do ambiente com um padrao declarado.
const THREADS = Number(__ENV.CARGA_THREADS || 30);
const DURACAO = __ENV.CARGA_DURACAO || '60s';
const CARTEIRAS = Number(__ENV.CARGA_CARTEIRAS || 20);
// O saldo inicial e alto de proposito.
//
// Com 1000.00 e apostas de 1 a 5.00, vinte carteiras secavam em cerca de um minuto de
// carga, e a rodada terminava com 40% de recusa por saldo insuficiente. Isso mede
// esgotamento de dinheiro, e nao contencao por carteira -- que e o que o `lock_timeout`
// existe para exercitar.
//
// O saldo nao e a variavel de interesse aqui, entao ele sai da via. As recusas por saldo
// continuam aparecendo no relatorio, mas como o que sao: um desfecho de regra, e nao uma
// falha. Para medir saldo ha o cenario do enunciado das duas apostas de 80 sobre 100, que
// e deterministico e verificado por teste.
const SALDO_INICIAL = '100000.00';

// O sal separa uma execucao da outra.
//
// Sem ele, os `playerId` seriao sempre os mesmos e a segunda execucao bateria na
// `uq_wallets_jogador_moeda` -- que e a constraint do enunciado fazendo o trabalho
// dela: "tentar abrir outra carteira para o mesmo jogador e moeda deve resultar em
// conflito". Um roteiro de carga que falha na segunda execucao nao e reproduzivel, e o
// conflito nao e um defeito do roteiro.
//
// A alternativa seria o setup aceitar o 409 e reutilizar a carteira, mas nao ha rota
// para buscar carteira por `playerId` -- a busca e por `walletId`, que e gerado. Aceitar
// o 409 e continuar deixaria a execucao com menos carteiras do que a declarada, sem que
// nada avisasse, e a contensao da carga dependeria de quantas carteiras sobraram.
//
// O sal padrao e o instante da execucao, entao cada rodada tem dados novos. Passar
// `CARGA_SEMENTE` de proposito faz a rodada bater na constraint, e isso serve para
// medir o comportamento sob conflito de abertura, que e um teste e nao uma carga.
const SEMENTE = __ENV.CARGA_SEMENTE || String(Date.now());

// O prefixo da execucao vai para DENTRO da chave de idempotencia e do identificador
// externo, e nao so para o `playerId`.
//
// A primeira versao deste script so salava o `playerId`, e a rodada seguinte recebeu
// 918 respostas 409 com "chave ja registrada com outro conteudo". As chaves
// `carga-3-0`, `carga-3-1` eram novas **para o `playerId`**, mas as MESMAS chaves de uma
// execucao anterior, que ja tinham gravado linha em `wager_transactions` -- e com um
// fingerprint diferente, porque o `playerId` das carteiras era outro.
//
// A resposta 409 estava certa: chave reusada com conteudo diferente e conflito, e o
// sistema behaved como o contrato manda. Quem estava errado era o roteiro, que gerava
// chaves que colidiam entre rodadas.
//
// Isso tambem explica por que o sal precisa estar nos dois lugares. Salvar so o
// `playerId` evita a constraint de jogador e moeda e cria o conflito de chave.
const prefixo = `carga-${SEMENTE}`;

// ---------------------------------------------------------------------------
// Enderecos
// ---------------------------------------------------------------------------

const base = __ENV.K6_BASE_URL || 'http://wager-service:8080';
const issuer = __ENV.K6_ISSUER || 'http://keycloak:8080/realms/wager';

export const options = {
  discardResponseBodies: false,
  thresholds: {
    // Nao ha meta de RPS no enunciado, e a de latencia tambem nao e um alvo de produto
    // -- e uma medida do que esta rodando aqui. Os limiares existem para que uma
    // regressao futura apareca como falha do comando e nao como numero piores num
    // relatorio que ninguem compara.
    //
    // `http_req_failed` mede o transporte, e um 503 de `lock_timeout` e uma resposta
    // correta: o servidor recusou o trabalho por contencao e mandou o cliente repetir.
    // Contar isso como falha transformaria a politica de concorrencia em alarme. O que
    // tem de ser zero e o que NAO tem caminho de reenvio: 400, 401, 403, 409 e 500.
    // Esse e o limiar abaixo.
    'carga_respostas_sem_reenvio': ['count==0'],
    'carga_operacao_latencia_ms': ['p(95)<500'],
  },
  scenarios: {
    carga: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '15s', target: THREADS },
        { duration: DURACAO, target: THREADS },
        { duration: '10s', target: 0 },
      ],
      gracefulRampDown: '10s',
    },
  },
};

// ---------------------------------------------------------------------------
// Credenciais
// ---------------------------------------------------------------------------

// O token vive cinco minutos no realm, e a execucao pode passar disso. A funcao guarda
// o token com o prazo e renova antes de expirar, em vez de renovar a cada iteracao --
// renovar por iteracao custaria uma chamada ao IdP por operacao e o teste passaria a
// medir o Keycloak.
let tokenProvedor = null;
let tokenProvedorAte = 0;
let tokenInterno = null;
let tokenInternoAte = 0;

function pedirToken(cliente, segredo) {
  const resposta = http.post(
    `${issuer}/protocol/openid-connect/token`,
    {
      grant_type: 'client_credentials',
      client_id: cliente,
      client_secret: segredo,
    },
    { headers: { 'Content-Type': 'application/x-www-form-urlencoded' } },
  );

  if (resposta.status !== 200) {
    throw new Error(`token de ${cliente} saiu com ${resposta.status}: ${resposta.body}`);
  }

  const corpo = resposta.json();
  // Renovacao com um terco de folga: se uma thread chegar no limite exato, ela ja
  // traz o token novo em vez de descobrir que o velho morreu.
  return { valor: corpo.access_token, ate: Date.now() + (corpo.expires_in * 1000) / 1.4 };
}

function provedor() {
  if (tokenProvedor === null || Date.now() > tokenProvedorAte) {
    tokenProvedor = pedirToken('provider-a', 'provider-a-secret');
  }
  return tokenProvedor.valor;
}

function interno() {
  if (tokenInterno === null || Date.now() > tokenInternoAte) {
    tokenInterno = pedirToken('wager-service', 'wager-service-secret');
  }
  return tokenInterno.valor;
}

const cabecaProvedor = () => ({
  Authorization: `Bearer ${provedor()}`,
  'Content-Type': 'application/json',
});

// ---------------------------------------------------------------------------
// Montagem
// ---------------------------------------------------------------------------

// As carteiras nascem aqui, e nao durante a execucao.
//
// Criar carteira dentro da carga poluiria a medicao com a operacao mais pesada que o
// sistema tem -- abertura e `OPENING` no mesmo commit, com credito e dois eventos -- e o
// resultado seria uma leitura de latencia dominada por uma operacao que nao e a que se
// quer medir. A carga mede aposta; a abertura fica fora.
export function setup() {
  const carteiras = [];

  for (let i = 0; i < CARTEIRAS; i++) {
    const resposta = http.post(
      `${base}/wallets`,
      JSON.stringify({
        playerId: uuid(i),
        initialBalance: { amount: SALDO_INICIAL, currency: 'BRL' },
      }),
      { headers: { Authorization: `Bearer ${interno()}`, 'Content-Type': 'application/json' } },
    );

    if (resposta.status !== 201 && resposta.status !== 200) {
      throw new Error(`abertura de carteira saiu com ${resposta.status}: ${resposta.body}`);
    }

    const corpo = resposta.json();
    carteiras.push({ id: corpo.id, jogador: corpo.playerId });
  }

  return { carteiras };
}

// UUID para o `playerId`, derivado do indice e do sal da execucao.
//
// O gerador e um xorshift de 32 bits em vez de um UUID v4 de verdade porque o k6 nao tem
// crypto: `crypto.randomUUID()` nao existe no ambiente. Um xorshift semeado pelo indice
// e pelo sal cobre o que o teste precisa -- identificadores distintos, em hex valido -- e
// nao pretende ser aleatorio.
//
// O formato e `8-4-4-4-12` em hex minusculo porque e o que `IdentificadorDe` aceita via
// `uuid.Parse`. Um preenchimento com zeros no fim pareceria UUID e seria recusado, o que
// faria o setup inteiro falhar com "playerId invalido".
//
// Incluir o sal no valor inicial muda todos os identificadores de uma execucao para a
// seguinte, e e o que permite rodar o roteiro duas vezes sem colidir com a constraint de
// jogador e moeda.
function uuid(indice) {
  let semente = 0;
  for (let i = 0; i < SEMENTE.length; i++) {
    semente = (semente * 31 + SEMENTE.charCodeAt(i)) >>> 0;
  }
  semente = (semente + indice + 1) >>> 0 || 1;

  const proximo = () => {
    semente ^= semente << 13;
    semente >>>= 0;
    semente ^= semente >>> 17;
    semente ^= semente << 5;
    semente >>>= 0;
    return semente;
  };

  const octeto = () => proximo().toString(16).padStart(8, '0');

  return [
    octeto(),
    octeto().slice(0, 4),
    octeto().slice(0, 4),
    octeto().slice(0, 4),
    `${octeto()}${octeto().slice(0, 4)}`,
  ].join('-');
}

// ---------------------------------------------------------------------------
// Carga
// ---------------------------------------------------------------------------

// O estado que uma thread carrega entre iteracoes.
//
// A ultima aposta processada e o que permite gerar reversao referenciando algo que
// existe. Sem ela, a reversao apontaria para uma operacao recusada -- o que o sistema
// trata como "referencia sem sucesso" e rejeita. Guardar so o que voltou `PROCESSED`
// mantem a carga no caminho feliz da reversao, que e o que se quer medir.
export default function (dados) {
  const carteira = dados.carteiras[(__VU - 1) % dados.carteiras.length];

  const sorteio = Math.random();
  let tipo = 'BET';
  if (sorteio < 0.7) tipo = 'BET';
  else if (sorteio < 0.8) tipo = 'LOSS';
  else if (sorteio < 0.9) tipo = 'WIN';
  else if (sorteio < 0.95) tipo = 'REFUND';
  else tipo = 'ROLLBACK';

  const externa = `${prefixo}-${__VU}-${__ITER}`;
  const chave = `provider-a:${externa}`;
  const reversaoDe = estadoAnterior.aposta ?? null;

  // LOSS exige valor zero, e a regra e do enunciado e nao negociavel: um LOSS com valor
  // maior que zero vira recusa, e a carga inteira passaria a medir recusa.
  const valor = tipo === 'LOSS' ? '0.00' : `${1 + Math.floor(Math.random() * 5)}.00`;

  const corpo = {
    providerId: 'provider-a',
    externalTransactionId: externa,
    playerId: carteira.jogador,
    walletId: carteira.id,
    roundId: `rodada-${Math.floor(Math.random() * 100)}`,
    gameId: 'fortune-chimp',
    kind: tipo,
    money: { amount: valor, currency: 'BRL' },
  };

  // A reversao so vai com referencia quando existe uma aposta processada para desfazer.
  // Enviar referencia vazia produziria pendencia esperando referencia, que e um
  // caminho legitimo mas nao e o que a carga de reversao deve exercitar.
  if ((tipo === 'REFUND' || tipo === 'ROLLBACK') && reversaoDe) {
    corpo.referenceExternalTransactionId = reversaoDe;
  }

  const inicio = Date.now();
  const resposta = enviarComReenvio(corpo, chave);

  latencia.add(Date.now() - inicio);

  const estado = resposta.status < 400 ? resposta.json('status') : `http_${resposta.status}`;

  // O veredito e "o sistema respondeu corretamente", e nao "o status foi 200". Um 422
  // de saldo insuficiente e o sistema funcionando: a regra recusou, que e o que ela
  // deveria fazer. Medir o transporte como se 422 fosse erro faria o relatorio dizer que
  // o sistema falhou 40% das vezes em uma carga em que ele fez exatamente o que devia.
  const tratou = RESPOSTA_TRATADA.includes(resposta.status) || resposta.status === 422;

  if (!tratou) {
    porEstado.add(1, { desfecho: estado });

    if (resposta.status === INDISPONIVEL) {
      // Contencao que a repeticao nao resolveu. Nao entra no limiar de zero: o cliente
      // fez o que o contrato manda, e o que resta e o sistema sob mais carga do que o
      // cliente espera -- um numero de regime, nao um defeito.
      indisponivelDepoisDoTeto.add(1, { operacao: tipo });
    } else {
      // O limiar do relatorio: o que nao tem recuperacao para o cliente.
      semReenvio.add(1, { status: resposta.status });
      if (__ENV.CARGA_DEBUG) {
        console.log(`SEM REENVIO status=${resposta.status} erro=${resposta.error} tipo=${tipo}`);
      }
    }
    return;
  }

  porEstado.add(1, { desfecho: estado });

  if (estado === 'PROCESSED') {
    if (tipo === 'BET') {
      estadoAnterior.aposta = externa;
    } else if (tipo === 'REFUND' || tipo === 'ROLLBACK') {
      // A aposta foi desfeita: ela nao pode ser desfeita de novo, e apontar a segunda
      // reversao para ela produziria "referencia ja revertida" -- um caminho de regra que
      // nao e o que se quer exercitar em volume.
      estadoAnterior.aposta = null;
    }
  }

  if (estado === 'REJECTED') {
    porCodigo.add(1, { codigo: resposta.json('failureCode') || 'desconhecido' });
  }

  // Uma leitura a cada cinco operacoes. Sem ela a carga mede so escrita, e a latencia de
  // leitura -- que e o caminho comum de quem consulta saldo -- fica sem numero.
  if (__ITER % 5 === 0) {
    http.get(`${base}/wallets/${carteira.id}`, {
      headers: { Authorization: `Bearer ${interno()}` },
      tags: { operacao: 'leitura' },
    });
  }

  // A pausa e o que transforma "N threads" em "N threads disputando". Sem ela, cada
  // thread fecha a iteracao e abre outra em menos de um milissegundo, e a fila de
  // espera do pool PostgreSQL vira o gargalo -- o que mede o pool, e nao a disputa por
  // carteira.
  sleep(Math.random() * 0.2);
}

// enviarComReenvio envia a operacao e reenvia em caso de indisponibilidade.
//
// O reenvio e o que um provedor real faz, e ele e seguro por construcao: a mesma chave
// de idempotencia vai de novo, entao a segunda entrega encontra a transacao ja
// confirmada e devolve o resultado sem mover dinheiro de novo. Se essa garantia
// quebrasse, o saldo desta carga mudaria -- e o relatorio mostraria a quebra como saldo
// errado em vez de como duplicata.
//
// Reenviar com chave NOVA seria o erro classico de dupla movimentacao, e o roteiro
// preserva a chave de proposito: e a garantia sendo exercitada, nao evitada.
function enviarComReenvio(corpo, chave) {
  let resposta = null;

  for (let tentativa = 0; tentativa <= MAX_REENVIOS; tentativa++) {
    resposta = http.post(`${base}/wagering/transactions`, JSON.stringify(corpo), {
      headers: { ...cabecaProvedor(), 'Idempotency-Key': chave },
    });

    if (resposta.status !== INDISPONIVEL) {
      return resposta;
    }

    if (tentativa < MAX_REENVIOS) {
      reenvios.add(1);
      // A espera e curta e o motivo e o `lock_timeout` de um segundo: repetir
      // imediatamente bateria no mesmo lock que acabou de recusar. Um backoff
      // exponencial seria o certo em producao; aqui um intervalo unico ja separa as
      // tentativas e mantem a carga curta.
      sleep(0.25 * (tentativa + 1));
    }
  }
  return resposta;
}

// Estado por thread.
//
// O k6 roda cada thread em seu proprio runtime JavaScript, com sua propria instancia do
// modulo. Um objeto em escopo de modulo e, portanto, por thread -- e e por isso que este
// `aposta` nao e compartilhado: se fosse, a ultima aposta seria a ultima de qualquer
// thread, e a reversao apontaria para a aposta de outra carteira, caindo em "outra
// carteira" ou "outra rodada" em vez de desfazer a aposta.
//
// Por isso ele fica em escopo de modulo e nao dentro da funcao: dentro da funcao ele
// seria recriado a cada iteracao e nunca sobreviveria para a reversao.
const estadoAnterior = { aposta: null };
