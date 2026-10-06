package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/auth"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// validadorFixo e um validador que devolve um ator sem consultar IdP.
//
// Existe para os testes de borda, que precisam exercitar status, cabecalhos e
// traducao de erro. O comportamento do IdP de verdade e testado em
// tests/integration/oidc, contra o Keycloak.
type validadorFixo struct {
	// ator e o que o token valido produz.
	ator app.Ator

	// erro e o que um token recusado produz.
	erro error
}

func (v validadorFixo) Ator(_ context.Context, bruto string) (app.Ator, error) {
	if v.erro != nil {
		return app.Ator{}, v.erro
	}
	if bruto == "" {
		return app.Ator{}, auth.ErrTokenAusente
	}
	return v.ator, nil
}

// servidorDeTeste sobe um roteador com as dependencias de borda.
func servidorDeTeste(t *testing.T, v ValidadorDeToken) http.Handler {
	t.Helper()
	return NovoRoteador(Dependencias{Validador: v})
}

// requisicaoFaz dispara uma requisicao e devolve o gravador.
func requisicaoFaz(
	t *testing.T,
	h http.Handler,
	metodo, caminho, corpo string,
	cabecalhos map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()

	var leitor io.Reader
	if corpo != "" {
		leitor = strings.NewReader(corpo)
	}
	req := httptest.NewRequest(metodo, caminho, leitor)
	if corpo != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for chave, valor := range cabecalhos {
		req.Header.Set(chave, valor)
	}

	gravador := httptest.NewRecorder()
	h.ServeHTTP(gravador, req)
	return gravador
}

// O health check e publico. Um orquestrador nao tem token, e um probe que depende
// de autenticacao so prova que o IdP esta de pe.
func TestHealthChecksSaoPublicos(t *testing.T) {
	h := servidorDeTeste(t, nil)

	for _, caminho := range []string{"/health/live", "/health/ready"} {
		resp := requisicaoFaz(t, h, http.MethodGet, caminho, "", nil)
		if resp.Code != http.StatusOK {
			t.Errorf("%s respondeu %d, esperado 200", caminho, resp.Code)
		}
	}
}

// Liveness nao consulta dependencia nenhuma. Um probe que checa banco derruba o
// processo quando o banco cai, e o processo nao volta quando o banco volta.
func TestLivenessNaoConsultaDependencia(t *testing.T) {
	h := NovoRoteador(Dependencias{
		Pronto: func(context.Context) error {
			t.Fatal("o liveness consultou a dependencia")
			return nil
		},
	})

	if resp := requisicaoFaz(t, h, http.MethodGet, "/health/live", "", nil); resp.Code != http.StatusOK {
		t.Errorf("liveness respondeu %d", resp.Code)
	}
}

// Readiness e sobre poder atender, e nao sobre existir.
func TestReadinessConsultaDependencia(t *testing.T) {
	falhando := NovoRoteador(Dependencias{
		Pronto: func(context.Context) error { return errors.New("banco fora") },
	})
	if resp := requisicaoFaz(t, falhando, http.MethodGet, "/health/ready", "", nil); resp.Code != http.StatusServiceUnavailable {
		t.Errorf("readiness com dependencia fora respondeu %d, esperado 503", resp.Code)
	}

	pronto := NovoRoteador(Dependencias{
		Pronto: func(context.Context) error { return nil },
	})
	if resp := requisicaoFaz(t, pronto, http.MethodGet, "/health/ready", "", nil); resp.Code != http.StatusOK {
		t.Errorf("readiness pronto respondeu %d, esperado 200", resp.Code)
	}
}

// Rota de negocio sem credencial e 401, e nunca 200.
func TestRotaDeNegocioSemTokenE401(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodPost, "/wallets", `{"playerId":"x"}`, nil)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("respondeu %d, esperado 401", resp.Code)
	}

	var corpo respostaErro
	if err := json.Unmarshal(resp.Body.Bytes(), &corpo); err != nil {
		t.Fatalf("corpo nao e JSON: %v", err)
	}
	if corpo.Erro != "credencial_ausente" {
		t.Errorf("codigo de erro e %q", corpo.Erro)
	}
}

// Credencial recusada pelo validador e 401.
func TestTokenRecusadoE401(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{erro: auth.ErrTokenInvalido})

	resp := requisicaoFaz(t, h, http.MethodPost, "/wallets", `{"playerId":"x"}`, map[string]string{
		"Authorization": "Bearer qualquer",
	})
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("respondeu %d, esperado 401", resp.Code)
	}
}

// O esquema do header e conferido. "Basic", texto solto e "Bearer" sem token sao
// ausencia de credencial.
func TestEsquemaDoHeaderEConferido(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	casos := []string{"abc.def.ghi", "Basic abc", "Bearer", "Bearer   "}
	for _, cabecalho := range casos {
		resp := requisicaoFaz(t, h, http.MethodGet, "/wallets/"+uuid.NewString(), "", map[string]string{
			"Authorization": cabecalho,
		})
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q respondeu %d, esperado 401", cabecalho, resp.Code)
		}
	}
}

// Sem validador montado a rota fica indisponivel, e nao aberta.
func TestSemValidadorARotaNaoAbre(t *testing.T) {
	h := servidorDeTeste(t, nil)

	resp := requisicaoFaz(t, h, http.MethodPost, "/wallets", `{"playerId":"x"}`, map[string]string{
		"Authorization": "Bearer qualquer",
	})
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("respondeu %d, esperado 503", resp.Code)
	}
}

// A correlacao volta na resposta, seja ela do cliente ou gerada.
func TestCorrelacaoVoltaNaResposta(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Cliente: "wager-service"}})

	// Informada pelo cliente.
	resp := requisicaoFaz(t, h, http.MethodGet, "/health/live", "", map[string]string{
		cabecalhoCorrelacao: "corr-123",
	})
	if resp.Header().Get(cabecalhoCorrelacao) != "corr-123" {
		t.Errorf("correlacao informed nao voltou: %q", resp.Header().Get(cabecalhoCorrelacao))
	}

	// Gerada quando ausente: sem isso o log de uma requisicao que falha antes de
	// qualquer identificador ficaria sem como casar com a resposta.
	gerada := requisicaoFaz(t, h, http.MethodGet, "/health/live", "", nil)
	if gerada.Header().Get(cabecalhoCorrelacao) == "" {
		t.Error("correlacao ausente na resposta")
	}
}

// A correlacao informada e limitada. Um cliente pode mandar um texto enorme e
// transformar o identificador em entrada de log inutil.
func TestCorrelacaoInformadaELimitada(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{})

	enorme := strings.Repeat("a", 500)
	resp := requisicaoFaz(t, h, http.MethodGet, "/health/live", "", map[string]string{
		cabecalhoCorrelacao: enorme,
	})

	voltou := resp.Header().Get(cabecalhoCorrelacao)
	if len(voltou) > 128 {
		t.Errorf("correlacao de %d caracteres nao foi limitada", len(voltou))
	}
}

// Um panic no handler vira 500 e nao derruba o processo. Um cliente que mande um
// corpo que provoque panic derrubaria o servico inteiro.
func TestPanicVira500(t *testing.T) {
	rotas := http.NewServeMux()
	rotas.HandleFunc("GET /explodir", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	// Nil no conjunto de metricas e o estado de quem monta o handler sem registro: a
	// cronometragem continua medindo para o log e nao publica nada.
	h := comMiddlewares(rotas, nil)

	gravador := httptest.NewRecorder()
	h.ServeHTTP(gravador, httptest.NewRequest(http.MethodGet, "/explodir", nil))

	if gravador.Code != http.StatusInternalServerError {
		t.Errorf("respondeu %d, esperado 500", gravador.Code)
	}
}

// A traducao de erro para status e o que o enunciado pede: entrada invalida,
// conflito, recusa, pendencia e indisponivel sao distinguiveis.
func TestTraducaoDeErrosFixaOContratoDeStatus(t *testing.T) {
	casos := map[string]struct {
		erro     error
		esperado int
		codigo   string
	}{
		"requisicao invalida": {
			erro:     app.ErrRequisicaoInvalida,
			esperado: http.StatusBadRequest,
			codigo:   "requisicao_invalida",
		},
		"nao autorizado": {
			erro:     app.ErrNaoAutorizado,
			esperado: http.StatusForbidden,
			codigo:   "nao_autorizado",
		},
		"provedor divergente": {
			erro:     app.ErrProvedorDivergente,
			esperado: http.StatusForbidden,
			codigo:   "provedor_divergente",
		},
		"conflito de chave": {
			erro:     app.ErrConflitoDeChave,
			esperado: http.StatusConflict,
			codigo:   "conflito",
		},
		"nao encontrado": {
			erro:     errDeNaoEncontrado{},
			esperado: http.StatusNotFound,
			codigo:   "nao_encontrado",
		},
		"recusa de regra": {
			erro: &app.FalhaDeRegra{
				Codigo: "BET_SEM_SALDO",
				Motivo: "saldo insuficiente",
			},
			esperado: http.StatusUnprocessableEntity,
			codigo:   "regra_de_negocio_recusou",
		},
		"referencia ausente": {
			// Pendencia nao e recusa: o trabalho foi aceito e sera retomado.
			erro: &app.FalhaDeRegra{
				Codigo: "REFERENCIA_NAO_ENCONTRADA",
				Motivo: "referencia nao chegou",
			},
			esperado: http.StatusAccepted,
			codigo:   "regra_de_negocio_recusou",
		},
		"disputa de concorrencia": {
			// O `lock_timeout` de um segundo e o que transforma contencao em falha
			// rapida e repetivel, e 503 e o status que diz "repita". Com 500, o
			// provedor costuma NAO repetir -- 500 significa "o erro e meu" -- e a
			// transacao se perdia sem nunca ter sido aplicada.
			//
			// Este caso entrou na tabela depois da carga, e nao antes: em
			// `tests/integration` nao ha contensao o bastante para esbarrar no
			// `lock_timeout`, entao o caminho so aparecia sob carga.
			erro: fmt.Errorf("%w: lock nao disponivel: %w",
				pg.ErrConflitoDeVersao,
				errors.New(`ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)`)),
			esperado: http.StatusServiceUnavailable,
			codigo:   "indisponivel",
		},
		"erro interno": {
			erro:     errors.New("qualquer"),
			esperado: http.StatusInternalServerError,
			codigo:   "erro_interno",
		},
	}

	for nome, caso := range casos {
		t.Run(nome, func(t *testing.T) {
			status, corpo := classificarErro(caso.erro)
			if status != caso.esperado {
				t.Errorf("status e %d, esperado %d", status, caso.esperado)
			}
			if corpo.Erro != caso.codigo {
				t.Errorf("codigo e %q, esperado %q", corpo.Erro, caso.codigo)
			}
			if corpo.Codigo != status {
				t.Errorf("status no corpo e %d e no transporte %d", corpo.Codigo, status)
			}
		})
	}
}

// O corpo da indisponibilidade transitoria tem de dizer que repetir e seguro.
//
// E o que o enunciado pede ao pedir que indisponibilidade transitoria seja
// distinguivel: sem essa frase no corpo, o provedor tem 503 na mao e nenhuma base para
// decidir entre repetir e desistir. A garantia de que repetir e seguro vem da chave de
// idempotencia, e e o corpo quem leva essa informacao ate o cliente.
func TestIndisponibilidadeTransitoriaDizQueRepetirESeguro(t *testing.T) {
	_, corpo := classificarErro(fmt.Errorf("%w: lock nao disponivel", pg.ErrConflitoDeVersao))

	if !strings.Contains(corpo.Detalhe, "repetir") {
		t.Errorf("o corpo da indisponibilidade nao orienta o cliente: detalhe %q", corpo.Detalhe)
	}
	if !strings.Contains(corpo.Detalhe, "idempotencia") {
		t.Errorf("o corpo nao diz POR QUE repetir e seguro: detalhe %q", corpo.Detalhe)
	}
}

// A falha de regra carrega o codigo de dominio no corpo, para que o cliente
// diferencie "payload errado" de "a regra recusou".
func TestFalhaDeRegraCarregaOCodigoDeDominio(t *testing.T) {
	_, corpo := classificarErro(&app.FalhaDeRegra{
		Codigo: "ROLLBACK_SEM_SALDO",
		Motivo: "sem saldo para desfazer o premio",
	})

	if corpo.CodigoDeFalha != "ROLLBACK_SEM_SALDO" {
		t.Errorf("codigo de dominio e %q", corpo.CodigoDeFalha)
	}
	if corpo.Detalhe == "" {
		t.Error("recusa sem detalhe para o humano")
	}
}

// Toda requisicao responde JSON, inclusive a de erro. Um cliente que teve de tratar
// dois formatos selon o status quebraria em producao.
func TestErroRespondeJSON(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodPost, "/wallets", "{nao e json", map[string]string{
		"Authorization": "Bearer x",
	})

	if ct := resp.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type e %q", ct)
	}
	var corpo respostaErro
	if err := json.Unmarshal(resp.Body.Bytes(), &corpo); err != nil {
		t.Fatalf("corpo nao e JSON: %v", err)
	}
	if corpo.Erro == "" {
		t.Error("erro sem codigo")
	}
}

// Um corpo JSON invalido e 400, e nao 500. O problema e do cliente.
func TestCorpoInvalidoE400(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodPost, "/wallets", "{nao e json", map[string]string{
		"Authorization": "Bearer x",
	})
	if resp.Code != http.StatusBadRequest {
		t.Errorf("respondeu %d, esperado 400", resp.Code)
	}
}

// Content-Type de outro formato e recusado antes da decodificacao.
func TestContentTypeErradoERecusado(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	req := httptest.NewRequest(http.MethodPost, "/wallets",
		strings.NewReader("playerId="+uuid.NewString()))
	req.Header.Set("Authorization", "Bearer x")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	gravador := httptest.NewRecorder()
	h.ServeHTTP(gravador, req)

	if gravador.Code != http.StatusBadRequest {
		t.Errorf("respondeu %d, esperado 400", gravador.Code)
	}
}

// A rota de operacao exige a chave de idempotencia. Sem ela o servidor nao tem como
// prometer que repetir nao move dinheiro duas vezes.
func TestOperacaoSemChaveDeIdempotenciaE400(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodPost, "/wagering/transactions", `{}`, map[string]string{
		"Authorization": "Bearer x",
	})
	if resp.Code != http.StatusBadRequest {
		t.Errorf("respondeu %d, esperado 400", resp.Code)
	}

	var corpo respostaErro
	if err := json.Unmarshal(resp.Body.Bytes(), &corpo); err != nil {
		t.Fatalf("corpo nao e JSON: %v", err)
	}
	if !strings.Contains(corpo.Detalhe, "Idempotency-Key") {
		t.Errorf("o detalhe nao menciona a chave: %q", corpo.Detalhe)
	}
}

// O provedor A nao le a operacao do provedor B. E a exposicao que a rota por provedor
// poderia ter, e a resposta e 403 sem revelar se a operacao existe.
func TestProvedorNaoLeOperacaoDeOutro(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{
		Cliente:  "provider-a",
		Provedor: "provider-a",
	}})

	resp := requisicaoFaz(t, h, http.MethodGet,
		"/providers/provider-b/wagering/transactions/transaction-123", "", map[string]string{
			"Authorization": "Bearer x",
		})

	if resp.Code != http.StatusForbidden {
		t.Errorf("respondeu %d, esperado 403", resp.Code)
	}
}

// O provedor A le a propria operacao pela rota por provedor. Chega a leitura, e a
// leitura devolve 404 porque o dado nao existe.
func TestProvedorLeOperacaoPropria(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{
		Cliente:  "provider-a",
		Provedor: "provider-a",
	}})

	// Sem servicos montados, o caso de uso falha na verificacao. O que importa aqui
	// e que ele NAO respondeu 403: a autorizacao passou.
	resp := requisicaoFaz(t, h, http.MethodGet,
		"/providers/provider-a/wagering/transactions/transaction-123", "", map[string]string{
			"Authorization": "Bearer x",
		})
	if resp.Code == http.StatusForbidden {
		t.Error("o provedor foi recusado na propria rota")
	}
}

// Um identificador de carteira malformado e 400, e nao 404 nem 500.
func TestIdentificadorMalformadoE400(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodGet, "/wallets/nao-e-uuid", "", map[string]string{
		"Authorization": "Bearer x",
	})
	if resp.Code != http.StatusBadRequest {
		t.Errorf("respondeu %d, esperado 400", resp.Code)
	}
}

// Um limit malformado e 400. O limite vem do cliente e precisa ser conferido.
func TestLimitMalformadoE400(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodGet,
		"/wallets/"+uuid.NewString()+"/ledger?limit=dez", "", map[string]string{
			"Authorization": "Bearer x",
		})
	if resp.Code != http.StatusBadRequest {
		t.Errorf("respondeu %d, esperado 400", resp.Code)
	}
}

// Um cursor quebrado e 400 e nao pagina vazia: devolver vazio para um cursor
// invalido faria o cliente receber a primeira pagina sem saber que o resultado
// estava errado.
func TestCursorInvalidoE400(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodGet,
		"/wallets/"+uuid.NewString()+"/ledger?cursor=%%nao-base64", "", map[string]string{
			"Authorization": "Bearer x",
		})
	if resp.Code != http.StatusBadRequest {
		t.Errorf("respondeu %d, esperado 400", resp.Code)
	}
}

// Uma rota que nao existe responde 404 e nao entra no middleware de autenticacao. Um
// 404 nao deve exigir token.
func TestRotaInexistenteE404(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodGet, "/nao-existe", "", nil)
	if resp.Code != http.StatusNotFound {
		t.Errorf("respondeu %d, esperado 404", resp.Code)
	}
}

// O metodo errado em rota existente responde 405, e nao 404: a rota existe com outro
// verbo.
func TestMetodoErradoE405(t *testing.T) {
	h := servidorDeTeste(t, validadorFixo{ator: app.Ator{Provedor: "provider-a"}})

	resp := requisicaoFaz(t, h, http.MethodDelete, "/wallets", "", map[string]string{
		"Authorization": "Bearer x",
	})
	if resp.Code != http.StatusMethodNotAllowed {
		t.Errorf("respondeu %d, esperado 405", resp.Code)
	}
}

// errDeNaoEncontrado evita que o teste dependa do pacote de persistencia para
// exercitar a traducao de erro.
type errDeNaoEncontrado struct{}

func (errDeNaoEncontrado) Error() string { return "nao encontrado" }

func (errDeNaoEncontrado) Is(alvo error) bool {
	return alvo != nil && alvo.Error() == "pg: registro nao encontrado"
}

// ---------------------------------------------------------------------------
// A metrica de latencia de requisicao tem writer
// ---------------------------------------------------------------------------

// A serie `wager_requisicao_duracao_ms` foi declarada na E16 e nunca observada: existia
// no registro, aparecia no `/metrics` como histograma vazio, e ninguem percebia porque
// um histograma declarado e nunca observado tem a mesma forma de um que nao existe.
//
// A forma do defeito e a mesma da `Inbox.Concluir`: um produtor declarado e sem writer.
// `go vet` nao acusa isso, e a suite passava. Por isso o teste verifica que a metrica
// recebe a observacao -- e nao que o handler responde, que ja era coberto.
func TestCronometrarPublicaALatenciaDaRequisicao(t *testing.T) {
	registro := obs.NovoRegistro()
	metricas := obs.NovasMetricas(registro)

	rotas := http.NewServeMux()
	rotas.HandleFunc("POST /operacao", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	h := comMiddlewares(rotas, metricas)

	requisicao := httptest.NewRequest(http.MethodPost, "/operacao", nil)
	registrador := httptest.NewRecorder()
	h.ServeHTTP(registrador, requisicao)

	exposto := registro.Expor()

	// O `_count` e o que prova que houve observacao. Procurar pelo nome do histograma
	// sem o `_count` passaria mesmo com a serie vazia, que era exatamente o estado que
	// passou despercebido.
	if !strings.Contains(exposto, `wager_requisicao_duracao_ms_count{metodo="POST"} 1`) {
		t.Errorf("a latencia da requisicao nao foi observada.\nExposto:\n%s", exposto)
	}
	if !strings.Contains(exposto, `wager_requisicao_duracao_ms_bucket{metodo="POST",le="+Inf"} 1`) {
		t.Errorf("o bucket infinito da latencia nao foi observado.\nExposto:\n%s", exposto)
	}
}

func TestCronometrarMedeEmilliseconds(t *testing.T) {
	registro := obs.NovoRegistro()
	metricas := obs.NovasMetricas(registro)

	rotas := http.NewServeMux()
	rotas.HandleFunc("GET /lento", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	h := comMiddlewares(rotas, metricas)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/lento", nil))

	// O teste verifica a UNIDADE, e nao que a duracao e exatamente trinta: medir tempo
	// num teste faz o teste depender da maquina.
	//
	// A discriminacao e pelos buckets, e ela depende de os valores serem CUMULADOS --
	// como o Prometheus escreve. Se a unidade registrada fosse segundos, 0,03 cairia em
	// `le="5"`; em milissegundos cai em `le="50"` e deixa `le="5"` zerado. Os dois
	// together e que fecham a prova, porque qualquer um deles sozinho passa numa metade
	// dos casos.
	if !strings.Contains(registro.Expor(), `wager_requisicao_duracao_ms_bucket{metodo="GET",le="5"} 0`) {
		t.Errorf("uma requisicao de 30ms nao deveria caber em 5ms: a unidade registrada nao parece ser milissegundo.\nExposto:\n%s",
			registro.Expor())
	}
	if !strings.Contains(registro.Expor(), `wager_requisicao_duracao_ms_bucket{metodo="GET",le="50"} 1`) {
		t.Errorf("uma requisicao de 30ms deveria caber no bucket de 50ms.\nExposto:\n%s",
			registro.Expor())
	}
}

func TestCronometrarSemConjuntoDeMetricasNaoQuebra(t *testing.T) {
	// Nil e o estado dos testes que montam o handler sem registro. Se o middleware
	// dereferenciasse nil aqui, o teste de panic deixaria de testar panic e passaria a
	// testar nil-pointer -- que e um caminho diferente com o mesmo sintoma.
	rotas := http.NewServeMux()
	rotas.HandleFunc("GET /ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := comMiddlewares(rotas, nil)

	registrador := httptest.NewRecorder()
	h.ServeHTTP(registrador, httptest.NewRequest(http.MethodGet, "/ok", nil))

	if registrador.Code != http.StatusOK {
		t.Errorf("status %d sem conjunto de metricas, esperado 200", registrador.Code)
	}
}
