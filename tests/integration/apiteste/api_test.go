//go:build integration

// Package apiteste exercita a borda HTTP contra o banco e o IdP de verdade.
//
// O teste sobe o roteador real, pede token no Keycloak real e fala com o
// PostgreSQL real. Nao ha servidor em memoria nem token fabricado: o que este
// arquivo prova e que a cadeia inteira, do header Authorization ate o commit, se
// liga.
package apiteste

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// dsnDono e o papel de dono do schema, o unico que consegue ler em verificacoes
// que nao passam pela aplicacao.
const dsnDono = "postgres://wager:wager@localhost:5432/wager?sslmode=disable"

// ambiente sobe o roteador com as dependencias reais.
type ambiente struct {
	// servidor e a borda HTTP em uma porta efemera.
	servidor *httptest.Server

	// tokens sao os tokens reais, por cliente do realm.
	tokens map[string]string

	// servicos e o caso de uso, para as verificacoes diretas.
	servicos app.Servicos
}

// novoAmbiente sobe tudo.
func novoAmbiente(t *testing.T) *ambiente {
	t.Helper()

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

	return &ambiente{servidor: servidor, tokens: pedirTokens(t, emissor), servicos: servicos}
}

// relogio e o relogio real. Nos testes de borda o instante nao importa, e um
// relogio parado esconderia um uso de time.Now no caminho da requisicao.
type relogio struct{}

func (relogio) Agora() time.Time { return time.Now().UTC() }

// emissorDoIdp devolve o endereco do realm de teste.
func emissorDoIdp() string {
	if valor := os.Getenv("WAGER_TEST_OIDC_ISSUER"); valor != "" {
		return strings.TrimSuffix(valor, "/")
	}
	return "http://localhost:8081/realms/wager"
}

// pedirTokens busca um token real para cada cliente do realm.
func pedirTokens(t *testing.T, emissor string) map[string]string {
	t.Helper()

	endpoint := emissor + "/protocol/openid-connect/token"
	tokens := map[string]string{}

	for _, cliente := range []string{"wager-service", "provider-a", "provider-b"} {
		form := url.Values{}
		form.Set("grant_type", "client_credentials")
		form.Set("client_id", cliente)
		form.Set("client_secret", segredoDoCliente(cliente))

		req, err := http.NewRequestWithContext(contexto(t), http.MethodPost, endpoint,
			strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatalf("request do token: %v", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("IdP acessivel? %v", err)
		}

		var corpo struct {
			AccessToken string `json:"access_token"`
			SecretHint  int    `json:"-"`
		}
		//nolint:errcheck
		bruto, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("token de %s: status %d, corpo %s (o segredo precisa estar em %s)",
				cliente, resp.StatusCode, bruto, "WAGER_TEST_OIDC_SECRET_"+strings.ToUpper(cliente))
		}
		if err := json.Unmarshal(bruto, &corpo); err != nil {
			t.Fatalf("decodificacao do token de %s: %v", cliente, err)
		}
		tokens[cliente] = corpo.AccessToken
	}
	return tokens
}

// chamar faz uma requisicao autenticada e devolve status e corpo cru.
func (a *ambiente) chamar(
	t *testing.T,
	metodo, caminho, cliente string,
	chave string,
	corpo any,
) (int, []byte) {
	t.Helper()

	var leitor io.Reader
	if corpo != nil {
		bruto, err := json.Marshal(corpo)
		if err != nil {
			t.Fatalf("marshal do corpo: %v", err)
		}
		leitor = bytes.NewReader(bruto)
	}

	req, err := http.NewRequestWithContext(contexto(t), metodo, a.servidor.URL+caminho, leitor)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cliente != "" {
		req.Header.Set("Authorization", "Bearer "+a.tokens[cliente])
	}
	if chave != "" {
		req.Header.Set("Idempotency-Key", chave)
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

// abrirCarteira abre uma carteira pelo HTTP, que e o caminho que o enunciado pede.
func (a *ambiente) abrirCarteira(t *testing.T, jogador string, centavos int64) respostaCarteira {
	t.Helper()

	status, bruto := a.chamar(t, http.MethodPost, "/wallets", "wager-service", "", map[string]any{
		"playerId": jogador,
		"initialBalance": map[string]any{
			"amount":   fmt.Sprintf("%d.%02d", centavos/100, abs(centavos%100)),
			"currency": "BRL",
		},
	})
	if status != http.StatusCreated {
		t.Fatalf("abertura respondeu %d: %s", status, bruto)
	}

	var resposta respostaCarteira
	if err := json.Unmarshal(bruto, &resposta); err != nil {
		t.Fatalf("decodificacao da carteira: %v", err)
	}
	return resposta
}

// respostaCarteira e o corpo de leitura de carteira.
type respostaCarteira struct {
	ID       string `json:"id"`
	PlayerId string `json:"playerId"`
	Balance  struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"balance"`
	Version  int64  `json:"version"`
	Currency string `json:"currency"`
}

// respostaOperacaoHTTP e o corpo de operacao.
type respostaOperacaoHTTP struct {
	TransactionId    string `json:"transactionId"`
	Status           string `json:"status"`
	IdempotentReplay bool   `json:"idempotentReplay"`
	FailureCode      string `json:"failureCode"`
	Balance          *struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"balance"`
}

// respostaErroHTTP e o corpo de erro.
type respostaErroHTTP struct {
	Error   string `json:"error"`
	Status  int    `json:"status"`
	Detail  string `json:"detail"`
	Failure string `json:"failureCode"`
}

// corpoOperacao monta o corpo de operacao.
func corpoOperacao(carteira, jogador, externa, tipo string, centavos int64) map[string]any {
	return map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": externa,
		"playerId":              jogador,
		"walletId":              carteira,
		"roundId":               "round-987",
		"gameId":                "fortune-chimp",
		"kind":                  tipo,
		"money": map[string]any{
			"amount":   fmt.Sprintf("%d.%02d", centavos/100, abs(centavos%100)),
			"currency": "BRL",
		},
	}
}

// ---------------------------------------------------------------------------
// Abertura de carteira
// ---------------------------------------------------------------------------

// A abertura pelo HTTP cria a carteira e responde 201 com o Location.
func TestAberturaPeloHTTPCriaCarteira(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 100000)

	if carteira.ID == "" {
		t.Fatal("resposta sem id")
	}
	if carteira.PlayerId != jogador {
		t.Errorf("playerId e %q, esperado %q", carteira.PlayerId, jogador)
	}
	if carteira.Balance.Amount != "1000.00" {
		t.Errorf("saldo e %s, esperado 1000.00", carteira.Balance.Amount)
	}
	if carteira.Version != 1 {
		t.Errorf("versao e %d, esperado 1", carteira.Version)
	}
	if carteira.Currency != "BRL" {
		t.Errorf("moeda e %q", carteira.Currency)
	}
}

// O provedor nao abre carteira: o token dele nao tem o escopo interno.
func TestProvedorNaoAbreCarteira(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	status, bruto := a.chamar(t, http.MethodPost, "/wallets", "provider-a", "", map[string]any{
		"playerId": uuid.NewString(),
		"initialBalance": map[string]any{
			"amount": "10.00", "currency": "BRL",
		},
	})

	if status != http.StatusForbidden {
		t.Fatalf("respondeu %d, esperado 403: %s", status, bruto)
	}
}

// A segunda carteira do mesmo jogador e conflito, e nao 500.
func TestSegundaCarteiraDoMesmoJogadorE409(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	a.abrirCarteira(t, jogador, 10000)

	status, bruto := a.chamar(t, http.MethodPost, "/wallets", "wager-service", "", map[string]any{
		"playerId": jogador,
		"initialBalance": map[string]any{
			"amount": "10.00", "currency": "BRL",
		},
	})

	if status != http.StatusConflict {
		t.Fatalf("respondeu %d, esperado 409: %s", status, bruto)
	}
}

// ---------------------------------------------------------------------------
// Operacao de wagering
// ---------------------------------------------------------------------------

// A operacao pelo HTTP debita o saldo e devolve o estado.
func TestOperacaoPeloHTTPDebitaSaldo(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"provider-a:transaction-123",
		corpoOperacao(carteira.ID, jogador, "transaction-123", "BET", 2500))

	if status != http.StatusOK {
		t.Fatalf("respondeu %d: %s", status, bruto)
	}

	var resposta respostaOperacaoHTTP
	if err := json.Unmarshal(bruto, &resposta); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	if resposta.Status != "PROCESSED" {
		t.Errorf("estado e %q", resposta.Status)
	}
	if resposta.IdempotentReplay {
		t.Error("primeira operacao foi marcada como replay")
	}
	if resposta.Balance == nil || resposta.Balance.Amount != "75.00" {
		t.Errorf("saldo e %+v, esperado 75.00", resposta.Balance)
	}
}

// O reenvio com a mesma chave devolve o resultado persistido e nao move dinheiro.
func TestReenvioDaMesmaChaveNaoMoveDinheiro(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)
	corpo := corpoOperacao(carteira.ID, jogador, "transaction-123", "BET", 2500)

	_, primeiro := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"provider-a:transaction-123", corpo)
	status, segundo := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"provider-a:transaction-123", corpo)

	if status != http.StatusOK {
		t.Fatalf("reenvio respondeu %d: %s", status, segundo)
	}

	var uma, outra respostaOperacaoHTTP
	if err := json.Unmarshal(primeiro, &uma); err != nil {
		t.Fatalf("primeira: %v", err)
	}
	if err := json.Unmarshal(segundo, &outra); err != nil {
		t.Fatalf("segunda: %v", err)
	}

	if !outra.IdempotentReplay {
		t.Error("o reenvio nao foi marcado como replay")
	}
	if outra.TransactionId != uma.TransactionId {
		t.Errorf("o reenvio criou outra transacao: %s e %s", outra.TransactionId, uma.TransactionId)
	}
	// O saldo do replay e o do processamento original, e nao o saldo atual.
	if outra.Balance == nil || outra.Balance.Amount != "75.00" {
		t.Errorf("saldo do replay e %+v, esperado o resultado original 75.00", outra.Balance)
	}
	if contar(t, `SELECT count(*) FROM wallet_ledger_entries`) != 2 {
		t.Errorf("o reenvio lancou no ledger")
	}
}

// Chave reusada com conteudo diferente e 409.
func TestChaveReusadaComConteudoDiferenteE409(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 100000)

	if status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500)); status != http.StatusOK {
		t.Fatalf("primeira respondeu %d: %s", status, bruto)
	}

	// Mesma chave, valor diferente.
	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 9900))

	if status != http.StatusConflict {
		t.Fatalf("respondeu %d, esperado 409: %s", status, bruto)
	}

	var erro respostaErroHTTP
	if err := json.Unmarshal(bruto, &erro); err != nil {
		t.Fatalf("decodificacao do erro: %v", err)
	}
	if erro.Error == "" {
		t.Error("conflito sem codigo no corpo")
	}
}

// A mesma operacao externa com outra chave nao e reaplicada. O enunciado pede que
// ela nao possa ser reaplicada, e devolver o resultado persistido satisfaz isso sem
// inventar um codigo de erro que o cliente nao saberia tratar.
//
// O que nao pode e mudar o saldo, e o ledger e a prova.
func TestMesmaOperacaoExternaComOutraChaveNaoEReaplicada(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 100000)

	if status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500)); status != http.StatusOK {
		t.Fatalf("primeira respondeu %d: %s", status, bruto)
	}

	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-2", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500))

	if status != http.StatusOK {
		t.Fatalf("respondeu %d, esperado 200: %s", status, bruto)
	}

	var resposta respostaOperacaoHTTP
	if err := json.Unmarshal(bruto, &resposta); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	if !resposta.IdempotentReplay {
		t.Error("a segunda chamada nao foi marcada como replay")
	}
	if contar(t, `SELECT count(*) FROM wallet_ledger_entries`) != 2 {
		t.Error("a segunda chamada lancou no ledger")
	}
	if contar(t, `SELECT count(*) FROM wager_transactions WHERE kind = 'BET'`) != 1 {
		t.Error("a segunda chamada criou outra transacao")
	}
}

// A aposta sem saldo e 422 com o codigo de dominio no corpo.
func TestApostaSemSaldoE422ComCodigoDeDominio(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 999999))

	if status != http.StatusUnprocessableEntity {
		t.Fatalf("respondeu %d, esperado 422: %s", status, bruto)
	}

	var resposta respostaOperacaoHTTP
	if err := json.Unmarshal(bruto, &resposta); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	if resposta.Status != "REJECTED" {
		t.Errorf("estado e %q, esperado REJECTED", resposta.Status)
	}
	if resposta.FailureCode != "BET_SEM_SALDO" {
		t.Errorf("codigo de falha e %q, esperado BET_SEM_SALDO", resposta.FailureCode)
	}
	if contar(t, `SELECT count(*) FROM wallet_ledger_entries`) != 1 {
		t.Error("a recusa lancou no ledger")
	}
}

// Sem a chave de idempotencia a requisicao e recusada antes de tocar o banco.
func TestOperacaoSemChaveERecusada(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a", "",
		corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500))

	if status != http.StatusBadRequest {
		t.Fatalf("respondeu %d, esperado 400: %s", status, bruto)
	}
	if contar(t, `SELECT count(*) FROM wager_transactions WHERE kind = 'BET'`) != 0 {
		t.Error("a operacao sem chave foi gravada")
	}
}

// O cliente interno nao opera apostas: o escopo de operacoes e do provedor.
func TestClienteInternoNaoOperaApostas(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "wager-service",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500))

	if status != http.StatusForbidden {
		t.Fatalf("respondeu %d, esperado 403: %s", status, bruto)
	}
}

// O provedor A nao opera em nome do B. E 403, e nao 404: o token e valido.
func TestProvedorNaoOperaEmNomeDeOutro(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	corpo := corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500)
	corpo["providerId"] = "provider-b"

	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpo)

	if status != http.StatusForbidden {
		t.Fatalf("respondeu %d, esperado 403: %s", status, bruto)
	}
}

// ---------------------------------------------------------------------------
// Leitura
// ---------------------------------------------------------------------------

// A leitura devolve o saldo depois da operacao.
func TestLeituraDaCarteiraDepoisDaOperacao(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	if status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500)); status != http.StatusOK {
		t.Fatalf("operacao respondeu %d: %s", status, bruto)
	}

	status, bruto := a.chamar(t, http.MethodGet, "/wallets/"+carteira.ID, "provider-a", "", nil)
	if status != http.StatusOK {
		t.Fatalf("leitura respondeu %d: %s", status, bruto)
	}

	var lida respostaCarteira
	if err := json.Unmarshal(bruto, &lida); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	if lida.Balance.Amount != "75.00" {
		t.Errorf("saldo e %s, esperado 75.00", lida.Balance.Amount)
	}
	if lida.Version != 2 {
		t.Errorf("versao e %d, esperado 2: a abertura e o debito", lida.Version)
	}
}

// O ledger pagina e volta, com cursor opaco e ordem estavel.
func TestLedgerPaginaComCursor(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 100000)

	for i := 0; i < 5; i++ {
		externa := fmt.Sprintf("transaction-%d", i)
		if status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
			"chave-"+externa, corpoOperacao(carteira.ID, jogador, externa, "BET", 1000)); status != http.StatusOK {
			t.Fatalf("operacao %d respondeu %d: %s", i, status, bruto)
		}
	}

	// Primeira pagina com limite 2.
	status, bruto := a.chamar(t, http.MethodGet,
		"/wallets/"+carteira.ID+"/ledger?limit=2", "provider-a", "", nil)
	if status != http.StatusOK {
		t.Fatalf("leitura respondeu %d: %s", status, bruto)
	}

	var pagina struct {
		Entries []struct {
			ID      string                  `json:"id"`
			Amount  struct{ Amount string } `json:"amount"`
			After   struct{ Amount string } `json:"balanceAfter"`
			Direcao string                  `json:"direction"`
		} `json:"entries"`
		NextCursor string `json:"nextCursor"`
		HasMore    bool   `json:"hasMore"`
	}
	if err := json.Unmarshal(bruto, &pagina); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}

	if len(pagina.Entries) != 2 {
		t.Fatalf("entradas e %d, esperado 2", len(pagina.Entries))
	}
	if !pagina.HasMore || pagina.NextCursor == "" {
		t.Error("a primeira pagina nao oferece continuacao")
	}
	// A ordem e decrescente, e a abertura foi de 1000.00 com cinco apostas de
	// 10.00: o lancamento mais recente deixa o saldo em 950.00.
	if pagina.Entries[0].After.Amount != "950.00" {
		t.Errorf("saldo na primeira entrada e %s, esperado 950.00", pagina.Entries[0].After.Amount)
	}
	if pagina.Entries[0].Direcao != "DEBIT" {
		t.Errorf("a primeira entrada e %q, esperado DEBIT", pagina.Entries[0].Direcao)
	}

	// Segunda pagina a partir do cursor.
	_, segundoBruto := a.chamar(t, http.MethodGet,
		fmt.Sprintf("/wallets/%s/ledger?limit=2&cursor=%s", carteira.ID,
			url.QueryEscape(pagina.NextCursor)), "provider-a", "", nil)

	var segunda struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(segundoBruto, &segunda); err != nil {
		t.Fatalf("decodificacao da segunda pagina: %v", err)
	}
	if len(segunda.Entries) != 2 {
		t.Fatalf("entradas na segunda pagina e %d, esperado 2", len(segunda.Entries))
	}
	// As paginas nao podem repetir linha.
	for _, antes := range pagina.Entries {
		for _, depois := range segunda.Entries {
			if antes.ID == depois.ID {
				t.Errorf("a entrada %s aparece nas duas paginas", antes.ID)
			}
		}
	}
}

// A leitura por identificador externo devolve a transacao.
func TestLeituraPorIdentificadorExterno(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)
	if status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500)); status != http.StatusOK {
		t.Fatalf("operacao respondeu %d: %s", status, bruto)
	}

	status, bruto := a.chamar(t, http.MethodGet,
		"/providers/provider-a/wagering/transactions/transaction-1", "provider-a", "", nil)
	if status != http.StatusOK {
		t.Fatalf("respondeu %d: %s", status, bruto)
	}

	var lida struct {
		Id       string `json:"id"`
		State    string `json:"state"`
		Kind     string `json:"kind"`
		Provider string `json:"providerId"`
	}
	if err := json.Unmarshal(bruto, &lida); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	if lida.State != "PROCESSED" {
		t.Errorf("estado e %q", lida.State)
	}
	if lida.Kind != "BET" {
		t.Errorf("tipo e %q", lida.Kind)
	}
}

// O provedor A nao le a transacao do B. E 403 sem revelar se ela existe.
func TestProvedorNaoLeTransacaoDeOutro(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	status, bruto := a.chamar(t, http.MethodGet,
		"/providers/provider-b/wagering/transactions/transaction-1", "provider-a", "", nil)

	if status != http.StatusForbidden {
		t.Fatalf("respondeu %d, esperado 403: %s", status, bruto)
	}
	if strings.Contains(string(bruto), "not found") || strings.Contains(string(bruto), "nao_encontrado") {
		t.Error("a resposta de 403 revelou se a operacao existe")
	}
}

// A leitura de transacao inexistente e 404.
func TestLeituraDeTransacaoInexistenteE404(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	status, _ := a.chamar(t, http.MethodGet,
		"/wagering/transactions/"+uuid.NewString(), "provider-a", "", nil)
	if status != http.StatusNotFound {
		t.Errorf("respondeu %d, esperado 404", status)
	}
}

// ---------------------------------------------------------------------------
// Reconciliacao
// ---------------------------------------------------------------------------

// A reconciliacao de uma carteira sadia vem consistente.
func TestReconciliacaoDeCarteiraSadia(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)
	if status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500)); status != http.StatusOK {
		t.Fatalf("operacao respondeu %d: %s", status, bruto)
	}

	status, bruto := a.chamar(t, http.MethodPost,
		"/wallets/"+carteira.ID+"/reconciliation", "wager-service", "", nil)
	if status != http.StatusOK {
		t.Fatalf("respondeu %d: %s", status, bruto)
	}

	var lida struct {
		WalletId     string                  `json:"walletId"`
		Stored       struct{ Amount string } `json:"storedBalance"`
		Calculated   struct{ Amount string } `json:"calculatedBalance"`
		Difference   struct{ Amount string } `json:"difference"`
		Consistent   bool                    `json:"consistent"`
		CheckedCount int64                   `json:"checkedEntries"`
	}
	if err := json.Unmarshal(bruto, &lida); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}

	if !lida.Consistent {
		t.Error("carteira sadia foi marcada como inconsistente")
	}
	if lida.Stored.Amount != "75.00" || lida.Calculated.Amount != "75.00" {
		t.Errorf("saldos %s e %s", lida.Stored.Amount, lida.Calculated.Amount)
	}
	if lida.Difference.Amount != "0.00" {
		t.Errorf("diferenca e %s", lida.Difference.Amount)
	}
	if lida.CheckedCount != 2 {
		t.Errorf("lancamentos conferidos e %d, esperado 2", lida.CheckedCount)
	}
	if lida.WalletId != carteira.ID {
		t.Errorf("walletId e %q", lida.WalletId)
	}
}

// A reconciliacao nao altera o saldo. Rodar duas vezes tem de dar o mesmo resultado.
func TestReconciliacaoNaoAlteraOSaldo(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	_, antes := a.chamar(t, http.MethodGet, "/wallets/"+carteira.ID, "provider-a", "", nil)
	_, relatorio := a.chamar(t, http.MethodPost, "/wallets/"+carteira.ID+"/reconciliation",
		"wager-service", "", nil)
	_, depois := a.chamar(t, http.MethodGet, "/wallets/"+carteira.ID, "provider-a", "", nil)

	var um, dois respostaCarteira
	if err := json.Unmarshal(antes, &um); err != nil {
		t.Fatalf("antes: %v", err)
	}
	if err := json.Unmarshal(depois, &dois); err != nil {
		t.Fatalf("depois: %v", err)
	}

	if um.Balance.Amount != dois.Balance.Amount || um.Version != dois.Version {
		t.Errorf("a reconciliacao alterou a carteira: %s v%d virou %s v%d",
			um.Balance.Amount, um.Version, dois.Balance.Amount, dois.Version)
	}
	if !strings.Contains(string(relatorio), `"consistent"`) {
		t.Errorf("relatorio sem o campo consistent: %s", relatorio)
	}
}

// O provedor nao roda reconciliacao: e ferramenta do cliente interno.
func TestProvedorNaoReconcilia(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	carteira := a.abrirCarteira(t, uuid.NewString(), 10000)

	status, bruto := a.chamar(t, http.MethodPost,
		"/wallets/"+carteira.ID+"/reconciliation", "provider-a", "", nil)
	if status != http.StatusForbidden {
		t.Fatalf("respondeu %d, esperado 403: %s", status, bruto)
	}
}

// ---------------------------------------------------------------------------
// O cenario de concorrencia do enunciado
// ---------------------------------------------------------------------------

// Duas apostas de 80.00 em uma carteira de 100.00, ao mesmo tempo: uma processada,
// uma recusada por saldo, saldo final 20.00 e um unico debito no ledger.
//
// E o teste que o enunciado nomeia, e o que vale se o lock da carteira funcionar.
// Sem o lock, as duas leituras veem 100.00 e as duas debitam, terminando em -60.00.
func TestDuasApostasSimultaneasUmaEntraEOutraERecusada(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000) // 100.00

	var (
		espera    sync.WaitGroup
		protecao  sync.Mutex
		resultado = map[int]respostaOperacaoHTTP{}
	)

	for i := 0; i < 2; i++ {
		espera.Add(1)
		go func(indice int) {
			defer espera.Done()

			externa := fmt.Sprintf("transaction-%d", indice)
			// Cada goroutine tem seu proprio cliente http e seu proprio contexto: o
			// http.Client padrao e seguro para uso concorrente, mas o contexto
			// compartilhado seria cancelado quando a primeira requisicao terminasse.
			status, bruto := a.chamarEmParalelo(contexto(t), indice,
				corpoOperacao(carteira.ID, jogador, externa, "BET", 8000))

			var resposta respostaOperacaoHTTP
			//nolint:errcheck
			json.Unmarshal(bruto, &resposta)

			protecao.Lock()
			defer protecao.Unlock()
			resultado[indice] = resposta
			_ = status
		}(i)
	}
	espera.Wait()

	processadas, recusadas := 0, 0
	for _, resposta := range resultado {
		switch resposta.Status {
		case "PROCESSED":
			processadas++
		case "REJECTED":
			recusadas++
			if resposta.FailureCode != "BET_SEM_SALDO" {
				t.Errorf("codigo da recusa e %q", resposta.FailureCode)
			}
		default:
			t.Errorf("estado inesperado: %q", resposta.Status)
		}
	}

	if processadas != 1 || recusadas != 1 {
		t.Errorf("processadas %d e recusadas %d, esperado 1 e 1", processadas, recusadas)
	}

	// Saldo final 20.00.
	_, bruto := a.chamar(t, http.MethodGet, "/wallets/"+carteira.ID, "provider-a", "", nil)
	var final respostaCarteira
	if err := json.Unmarshal(bruto, &final); err != nil {
		t.Fatalf("leitura final: %v", err)
	}
	if final.Balance.Amount != "20.00" {
		t.Errorf("saldo final e %s, esperado 20.00", final.Balance.Amount)
	}

	// Um unico debito: a abertura e a aposta que entrou.
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries WHERE direction = 'DEBIT'`); got != 1 {
		t.Errorf("debitos e %d, esperado 1", got)
	}
	if got := contar(t, `SELECT count(*) FROM wallet_ledger_entries`); got != 2 {
		t.Errorf("lancamentos e %d, esperado 2", got)
	}
}

// chamarEmParalelo faz a requisicao de uma goroutine.
func (a *ambiente) chamarEmParalelo(ctx context.Context, indice int, corpo any) (int, []byte) {
	bruto, err := json.Marshal(corpo)
	if err != nil {
		return 0, nil
	}

	externa := fmt.Sprintf("transaction-%d", indice)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.servidor.URL+"/wagering/transactions", bytes.NewReader(bruto))
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.tokens["provider-a"])
	req.Header.Set("Idempotency-Key", "chave-"+externa)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()

	//nolint:errcheck
	leitura, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, leitura
}

// Cartas diferentes avancam em paralelo. O lock e por carteira, e um lock global
// serializaria o sistema inteiro, o que o enunciado proibe.
func TestCarteirasDiferentesAvancamEmParalelo(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	const carteiras = 4
	var (
		espera   sync.WaitGroup
		protecao sync.Mutex
		sucesso  = map[string]bool{}
	)

	for i := 0; i < carteiras; i++ {
		espera.Add(1)
		go func(indice int) {
			defer espera.Done()

			ctx := contexto(t)
			jogador := uuid.NewString()

			abertura := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"100.00","currency":"BRL"}}`, jogador)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				a.servidor.URL+"/wallets", strings.NewReader(abertura))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+a.tokens["wager-service"])

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			//nolint:errcheck
			aberturaBruto, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != http.StatusCreated {
				return
			}

			var criada respostaCarteira
			//nolint:errcheck
			json.Unmarshal(aberturaBruto, &criada)

			externa := fmt.Sprintf("transaction-%d", indice)
			operacao, _ := json.Marshal(corpoOperacao(criada.ID, jogador, externa, "BET", 2500))

			reqOperacao, err := http.NewRequestWithContext(ctx, http.MethodPost,
				a.servidor.URL+"/wagering/transactions", bytes.NewReader(operacao))
			if err != nil {
				return
			}
			reqOperacao.Header.Set("Content-Type", "application/json")
			reqOperacao.Header.Set("Authorization", "Bearer "+a.tokens["provider-a"])
			reqOperacao.Header.Set("Idempotency-Key", "chave-"+externa)

			respostaOperacao, err := http.DefaultClient.Do(reqOperacao)
			if err != nil {
				return
			}
			//nolint:errcheck
			operacaoBruto, _ := io.ReadAll(respostaOperacao.Body)
			respostaOperacao.Body.Close()

			var lida respostaOperacaoHTTP
			//nolint:errcheck
			json.Unmarshal(operacaoBruto, &lida)

			protecao.Lock()
			defer protecao.Unlock()
			sucesso[criada.ID] = lida.Status == "PROCESSED" && lida.Balance != nil &&
				lida.Balance.Amount == "75.00"
		}(i)
	}
	espera.Wait()

	if len(sucesso) != carteiras {
		t.Fatalf("carteiras processadas %d, esperado %d", len(sucesso), carteiras)
	}
	for id, ok := range sucesso {
		if !ok {
			t.Errorf("a carteira %s nao chegou a 75.00", id)
		}
	}
}

// O corpo de uma operacao com valor invalido e 400, e nao 422: o problema e o
// payload.
func TestValorInvalidoE400(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	corpo := corpoOperacao(carteira.ID, jogador, "transaction-1", "BET", 2500)
	corpo["money"] = map[string]any{"amount": "vinte e cinco", "currency": "BRL"}

	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpo)
	if status != http.StatusBadRequest {
		t.Fatalf("respondeu %d, esperado 400: %s", status, bruto)
	}
}

// Um tipo de operacao desconhecido e 400.
func TestTipoDesconhecidoE400(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	jogador := uuid.NewString()
	carteira := a.abrirCarteira(t, jogador, 10000)

	status, bruto := a.chamar(t, http.MethodPost, "/wagering/transactions", "provider-a",
		"chave-1", corpoOperacao(carteira.ID, jogador, "transaction-1", "MEIA_GANHA", 2500))

	if status != http.StatusBadRequest {
		t.Fatalf("respondeu %d, esperado 400: %s", status, bruto)
	}
}

// O teste roda contra o que o servidor devolve, e nao contra um codigo fixo: um
// 200 com corpo de erro esconderia a falha.
func TestRespostasDeErroNaoVemCom200(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	casos := []struct {
		nome    string
		chamada func() (int, []byte)
	}{
		{"sem token", func() (int, []byte) {
			return a.chamar(t, http.MethodGet, "/wallets/"+uuid.NewString(), "", "", nil)
		}},
		{"carteira inexistente", func() (int, []byte) {
			return a.chamar(t, http.MethodGet, "/wallets/"+uuid.NewString(), "provider-a", "", nil)
		}},
		{"corpo invalido", func() (int, []byte) {
			return a.chamar(t, http.MethodPost, "/wallets", "wager-service", "", "nao e objeto")
		}},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			status, bruto := caso.chamada()
			if status == http.StatusOK {
				t.Errorf("respondeu 200: %s", bruto)
			}
			if len(bruto) > 0 {
				var erro respostaErroHTTP
				if err := json.Unmarshal(bruto, &erro); err == nil && erro.Error == "" && status >= 400 {
					t.Errorf("status %d sem codigo de erro no corpo", status)
				}
			}
		})
	}
}

// O teste usa o servidor de verdade, entao um erro de autenticacao tem de aparecer
// como 401 e nao como panic.
func TestCadeiaCompletaDeAutenticacao(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t)

	// Token de cliente que existe no realm, mas com audience errada: o validador
	// recusa, e a resposta e 401.
	req, err := http.NewRequestWithContext(contexto(t), http.MethodGet,
		a.servidor.URL+"/wallets/"+uuid.NewString(), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer nao.e.um.token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisicao: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("respondeu %d, esperado 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type e %q", resp.Header.Get("Content-Type"))
	}
}

// contar devolve quantas linhas a consulta encontra, com o papel de dono.
func contar(t *testing.T, consulta string, args ...any) int {
	t.Helper()

	db, err := sql.Open("pgx", dsnDono)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	var total int
	if err := db.QueryRowContext(context.Background(), consulta, args...).Scan(&total); err != nil {
		t.Fatalf("contagem %q: %v", consulta, err)
	}
	return total
}

// segredoDoCliente devolve o segredo do cliente no realm de teste.
//
// O padrao e o segredo do realm local, que esta no repositorio de proposito: e
// credencial de ambiente de teste, e quem roda precisa dela sem configuracao extra.
// A variavel de ambiente existe para o caso de o realm local ter outro segredo.
func segredoDoCliente(cliente string) string {
	chave := "WAGER_TEST_OIDC_SECRET_" + strings.ToUpper(strings.ReplaceAll(cliente, "-", "_"))
	if valor := os.Getenv(chave); valor != "" {
		return valor
	}
	return cliente + "-secret"
}
func contexto(t *testing.T) context.Context {
	t.Helper()

	ctx, cancelar := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancelar)
	return ctx
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

var _ = errors.Is
