//go:build integration

// Package obsteste prova que uma operacao e rastreavel de ponta a ponta pelo log.
//
// Este e o portao da E16. Nao basta o log ter os campos: e preciso que a mesma
// correlacao atravesse a borda HTTP, o caso de uso, a gravacao e o evento, sem que
// ninguem passe o valor adiante a mao. Um log com os campos certos e a correlacao
// trocada em algum ponto e inutil para o operador, porque a busca acha metade do
// caminho.
//
// O teste sobe o servidor real, pede token no Keycloak real e fala com o PostgreSQL
// real, porque a correlicao atravessa as tres.
package obsteste

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/auth"
	"github.com/LuisMassuchini/backend-challenge-go/internal/httpapi"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// dsnRuntime e o papel de menor privilegio, o mesmo que a aplicacao usa.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// coletor guarda as linhas de log que o teste vai procurar.
//
// O logger e trocado por um que escreve aqui em vez da saida padrao. E a unica
// forma de verificar a rastreabilidade sem container de log: o que importa e que as
// linhas carreguem os identificadores, e nao que o processo os tenha impresso no
// stdout de quem roda o teste.
type coletor struct {
	// linhas sao as mensagens JSON, na ordem em que foram emitidas.
	linhas []map[string]any

	// mu protege linhas, porque o log vem de goroutines do servidor e do teste.
	mu sync.Mutex
}

// registrar guarda uma linha.
func (c *coletor) registrar(linha map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.linhas = append(c.linhas, linha)
}

// comValor devolve as linhas cujo campo tem o valor informado.
func (c *coletor) comValor(campo, valor string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()

	var encontradas []map[string]any
	for _, linha := range c.linhas {
		if fmt.Sprint(linha[campo]) == valor {
			encontradas = append(encontradas, linha)
		}
	}
	return encontradas
}

// todas devolve todas as linhas coletadas, para o diagnostico de falha.
func (c *coletor) todas() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	copia := make([]map[string]any, len(c.linhas))
	copy(copia, c.linhas)
	return copia
}

// instalar troca o logger padrao por um que alimenta o coletor.
func (c *coletor) instalar(t *testing.T) {
	t.Helper()

	anterior := slog.Default()
	slog.SetDefault(slog.New(&handlerJSON{coletor: c}))
	t.Cleanup(func() { slog.SetDefault(anterior) })
}

// handlerJSON e um handler slog que monta o JSON e entrega ao coletor.
//
// Existe em vez de `slog.NewJSONHandler` porque o teste precisa do mapa ja
// desserializado: comparar texto em vez de campo seria fragile, e o que se verifica
// aqui e a presenca do campo e do valor, nao a formatacao.
type handlerJSON struct {
	coletor *coletor
	// grupos mantem os atributos do logger pai, que o `With` acumula.
	grupos []slog.Attr
}

// Enabled aceita tudo. O teste nao filtra por nivel porque cada linha importa.
func (h *handlerJSON) Enabled(context.Context, slog.Level) bool { return true }

// Handle guarda a linha.
func (h *handlerJSON) Handle(_ context.Context, registro slog.Record) error {
	linha := map[string]any{"msg": registro.Message, "level": registro.Level.String()}
	for _, atributo := range h.grupos {
		linha[atributo.Key] = atributo.Value.String()
	}
	registro.Attrs(func(atributo slog.Attr) bool {
		linha[atributo.Key] = valorDe(atributo.Value)
		return true
	})

	h.coletor.registrar(linha)
	return nil
}

// WithAttrs devolve um handler que ja conhece os atributos do pai.
func (h *handlerJSON) WithAttrs(atributos []slog.Attr) slog.Handler {
	grupos := make([]slog.Attr, 0, len(h.grupos)+len(atributos))
	grupos = append(grupos, h.grupos...)
	grupos = append(grupos, atributos...)
	return &handlerJSON{coletor: h.coletor, grupos: grupos}
}

// WithGroup e no-op.
//
// O sistema nao usa grupos de atributo, e implementar a semantica de aninhamento aqui
// seria codigo de teste que nunca roda.
func (h *handlerJSON) WithGroup(string) slog.Handler { return h }

// valorDe converte o valor de um atributo slog para algo comparavel.
func valorDe(valor slog.Value) any {
	if valor.Kind() == slog.KindString {
		return valor.String()
	}
	return valor.Any()
}

// ambiente sobe o servidor real com o coletor de log instalado.
type ambiente struct {
	servidor *httptest.Server
	// tokenInterno e o token do cliente de servico, que abre carteira.
	tokenInterno string
	// tokenProvedor e o token do provedor, que envia operacao.
	tokenProvedor string
	servicos      app.Servicos
	coletor       *coletor
}

// novoAmbiente sobe tudo com o log coletado.
func novoAmbiente(t *testing.T) *ambiente {
	t.Helper()

	dbtest.Limpa(t)

	coletor := &coletor{}
	coletor.instalar(t)

	pool, err := pg.AbrirPool(contexto(t), dsnRuntime, pg.Opcoes{MaxConexoes: 16})
	if err != nil {
		t.Fatalf("abertura do pool: %v", err)
	}
	t.Cleanup(pool.Close)

	servicos := app.Servicos{
		Unidade:    pg.NovaUnidade(pool),
		Carteiras:  pg.NovaRepositorioCarteira(),
		Ledger:     pg.NovaRepositorioLedger(),
		Transacoes: pg.NovaRepositorioTransacoes(),
		Inbox:      pg.NovaRepositorioInbox(),
		Outbox:     pg.NovaRepositorioOutbox(),
		Relogio:    relogio{},
		Correlacao: func() string { return uuid.NewString() },
	}

	emissor := emissorDoIdp()
	validador, err := auth.NovoValidador(auth.Config{
		Issuer:          emissor,
		Audience:        "wager-service",
		URLJWKS:         emissor + "/protocol/openid-connect/certs",
		CacheJWKS:       30 * time.Second,
		MargemDeRelogio: 30 * time.Second,
		HTTPTimeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("validador: %v", err)
	}

	servidor := httptest.NewServer(httpapi.NovoRoteador(httpapi.Dependencias{
		Servicos:  servicos,
		Validador: validador,
	}))
	t.Cleanup(servidor.Close)

	tokens := pedirTokens(t, emissor)

	return &ambiente{
		servidor:      servidor,
		tokenInterno:  tokens["wager-service"],
		tokenProvedor: tokens["provider-a"],
		servicos:      servicos,
		coletor:       coletor,
	}
}

// relogio e o relogio real.
type relogio struct{}

// Agora devolve o instante corrente.
func (relogio) Agora() time.Time { return time.Now().UTC() }

// contexto devolve um contexto com prazo.
func contexto(t *testing.T) context.Context {
	t.Helper()
	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelar)
	return ctx
}

// emissorDoIdp devolve o endereco do realm de teste.
func emissorDoIdp() string {
	if valor := os.Getenv("WAGER_TEST_OIDC_ISSUER"); valor != "" {
		return strings.TrimSuffix(valor, "/")
	}
	return "http://localhost:8081/realms/wager"
}

// pedirTokens busca token real de cada cliente do realm.
func pedirTokens(t *testing.T, emissor string) map[string]string {
	t.Helper()

	clientes := map[string]string{
		"provider-a":    "provider-a-secret",
		"wager-service": "wager-service-secret",
	}

	tokens := map[string]string{}
	for cliente, segredo := range clientes {
		form := url.Values{
			"grant_type":    {"client_credentials"},
			"client_id":     {cliente},
			"client_secret": {segredo},
		}
		req, err := http.NewRequestWithContext(contexto(t), http.MethodPost,
			emissor+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatalf("request do token: %v", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("token de %s: %v", cliente, err)
		}
		defer resp.Body.Close()

		//nolint:errcheck
		bruto, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("token de %s: status %d, corpo %s", cliente, resp.StatusCode, bruto)
		}

		var corpo struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(bruto, &corpo); err != nil {
			t.Fatalf("decodificacao do token de %s: %v", cliente, err)
		}
		tokens[cliente] = corpo.AccessToken
	}
	return tokens
}

// requisicao faz uma chamada autenticada com a correlacao informada.
func (a *ambiente) requisicao(
	t *testing.T,
	metodo, caminho, token, chave, correlacao string,
	corpo any,
) (int, []byte) {
	t.Helper()

	var leitor io.Reader
	if corpo != nil {
		bruto, err := json.Marshal(corpo)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		leitor = strings.NewReader(string(bruto))
	}

	req, err := http.NewRequestWithContext(contexto(t), metodo, a.servidor.URL+caminho, leitor)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if chave != "" {
		req.Header.Set("Idempotency-Key", chave)
	}
	if correlacao != "" {
		req.Header.Set("X-Correlation-Id", correlacao)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisicao: %v", err)
	}
	defer resp.Body.Close()

	//nolint:errcheck
	bruto, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, bruto
}

// abrirCarteira abre a carteira pelo caminho interno.
func (a *ambiente) abrirCarteira(t *testing.T, jogador string, centavos int64) string {
	t.Helper()

	status, bruto := a.requisicao(t, http.MethodPost, "/wallets", a.tokenInterno, "", "", map[string]any{
		"playerId": jogador,
		"initialBalance": map[string]any{
			"amount":   fmt.Sprintf("%d.%02d", centavos/100, centavos%100),
			"currency": "BRL",
		},
	})
	if status != http.StatusCreated {
		t.Fatalf("abertura respondeu %d: %s", status, bruto)
	}

	var resposta struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(bruto, &resposta); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	return resposta.ID
}

// A correlacao do cliente atravessa a requisicao, o caso de uso e o log da operacao
// confirmada.
//
// E o portao da E16. A busca comeca pelo valor que o cliente conhece -- o
// `X-Correlation-Id` que ele mandou -- e precisa encontrar a linha que diz o que
// aconteceu com a operacao, sem que o teste saiba em qual camada ela foi escrita.
func TestCorrelacaoDoClienteAtravessaAOperacao(t *testing.T) {
	a := novoAmbiente(t)

	carteira := a.abrirCarteira(t, uuid.NewString(), 10000)
	correlacao := "corr-" + uuid.NewString()

	status, bruto := a.requisicao(t, http.MethodPost, "/wagering/transactions",
		a.tokenProvedor, "chave-"+uuid.NewString(), correlacao, map[string]any{
			"providerId":            "provider-a",
			"externalTransactionId": "transaction-" + uuid.NewString(),
			"playerId":              jogadorDaCarteira(t, a, carteira),
			"walletId":              carteira,
			"roundId":               "round-987",
			"gameId":                "fortune-chimp",
			"kind":                  "BET",
			"money":                 map[string]any{"amount": "25.00", "currency": "BRL"},
		})
	if status != http.StatusOK {
		t.Fatalf("operacao respondeu %d: %s", status, bruto)
	}

	var resposta struct {
		TransactionId string `json:"transactionId"`
	}
	if err := json.Unmarshal(bruto, &resposta); err != nil {
		t.Fatalf("decodificacao da resposta: %v", err)
	}

	// A correlacao aparece no log da requisicao e no da operacao.
	linhas := a.coletor.comValor("correlationId", correlacao)
	if len(linhas) < 2 {
		t.Fatalf("a correlacao %s apareceu em %d linhas, esperado no minimo 2 "+
			"(a requisicao e a operacao)", correlacao, len(linhas))
	}

	// E a linha da operacao carrega a carteira, o provedor e a transacao. Sem a
	// transacao, o operador tem a correlacao mas nao sabe qual operacao foi.
	var achouConfirmada bool
	for _, linha := range linhas {
		if fmt.Sprint(linha["msg"]) != "operacao confirmada" {
			continue
		}
		achouConfirmada = true
		if linha["walletId"] != carteira {
			t.Errorf("walletId = %v, esperado %s", linha["walletId"], carteira)
		}
		if linha["providerId"] != "provider-a" {
			t.Errorf("providerId = %v", linha["providerId"])
		}
		if linha["transactionId"] != resposta.TransactionId {
			t.Errorf("transactionId = %v, esperado %s", linha["transactionId"], resposta.TransactionId)
		}
	}
	if !achouConfirmada {
		t.Error("nenhuma linha 'operacao confirmada' carregou a correlacao do cliente")
	}
}

// A resposta devolve a mesma correlacao que o cliente mandou.
//
// Sem isso o cliente nao tem como citar a correlacao ao abrir um chamado, e a
// rastreabilidade serve so para quem ja esta olhando o log.
func TestRespostaDevolveACorrelacaoDoCliente(t *testing.T) {
	a := novoAmbiente(t)

	req, err := http.NewRequestWithContext(contexto(t), http.MethodGet,
		a.servidor.URL+"/health/live", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	correlacao := "corr-" + uuid.NewString()
	req.Header.Set("X-Correlation-Id", correlacao)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisicao: %v", err)
	}
	defer resp.Body.Close()

	if devolvida := resp.Header.Get("X-Correlation-Id"); devolvida != correlacao {
		t.Errorf("a resposta devolveu a correlacao %q, esperado %q", devolvida, correlacao)
	}
}

// Sem correlacao do cliente, o servidor gera uma e devolve.
//
// E o que permite rastrear uma requisicao cujo cliente nao manda o cabecalho, que e o
// caso de qualquer consumidor que ainda nao foi ajustado.
func TestCorrelacaoAusenteEGerada(t *testing.T) {
	a := novoAmbiente(t)

	req, err := http.NewRequestWithContext(contexto(t), http.MethodGet,
		a.servidor.URL+"/health/live", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisicao: %v", err)
	}
	defer resp.Body.Close()

	gerada := resp.Header.Get("X-Correlation-Id")
	if gerada == "" {
		t.Fatal("a resposta nao trouxe correlacao gerada")
	}
	if len(a.coletor.comValor("correlationId", gerada)) == 0 {
		t.Errorf("a correlacao gerada %s nao apareceu em nenhuma linha de log. Linhas coletadas: %v", gerada, a.coletor.todas())
	}
}

// httpTestServer e o servidor de teste.
//
// E um alias com nome, e nao `*httptest.Server` direto nos dois arquivos, para que a
// troca de implementacao -- um `net/http` de verdade na E17, com porta efemera -- nao
// toque em nenhum dos dois.
type httpTestServer = httptest.Server

// sobeServidor sobe um servidor com as dependencias informadas.
//
// Existe separado do `novoAmbiente` porque nem todo teste precisa de Keycloak nem de
// banco. Um teste de metrica que sobe realm OIDC para conferir uma string e lento sem
// provar nada: o que importa e a rota e o corpo, e os dois nao dependem de token.
func sobeServidor(t *testing.T, deps httpapi.Dependencias) *httpTestServer {
	t.Helper()

	servidor := httptest.NewServer(httpapi.NovoRoteador(deps))
	t.Cleanup(servidor.Close)
	return servidor
}

// jogadorDaCarteira le o jogador dono da carteira.
//
// A leitura e pelo caso de uso, e nao pelo SQL, porque o teste verifica
// rastreabilidade de log e nao precisa de privilegio de leitura direta do banco.
func jogadorDaCarteira(t *testing.T, a *ambiente, carteira string) string {
	t.Helper()

	status, bruto := a.requisicao(t, http.MethodGet, "/wallets/"+carteira, a.tokenInterno, "", "", nil)
	if status != http.StatusOK {
		t.Fatalf("leitura da carteira: %d %s", status, bruto)
	}

	var resposta struct {
		PlayerId string `json:"playerId"`
	}
	if err := json.Unmarshal(bruto, &resposta); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	return resposta.PlayerId
}
