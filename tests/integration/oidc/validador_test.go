//go:build integration

// Package oidc testa a autenticacao contra o Keycloak de verdade.
//
// Nao ha mock de IdP aqui, e uma decisao do enunciado: um teste com IdP falso
// passa enquanto a assinatura, a audiencia, o emissor e a atribuicao de provedor
// estao errados. O que este arquivo protege e justamente aintegracao com um IdP
// real, e um mock nao testaria nada disso.
package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/auth"
)

// emissorDeTeste e o endereco do realm.
//
// Vem do ambiente porque o emissor depende de onde a requisicao parte: de dentro da
// rede do compose o realm e `keycloak:8080`, e da maquina do teste e `localhost`.
// O token carrega o emissor de quem pediu, entao emissor e URL do token precisam
// concordar.
func emissorDeTeste(t *testing.T) string {
	t.Helper()

	emissor := os.Getenv("WAGER_TEST_OIDC_ISSUER")
	if emissor == "" {
		emissor = "http://localhost:8081/realms/wager"
	}
	return strings.TrimSuffix(emissor, "/")
}

// segredoDeTeste devolve o segredo do cliente no realm de teste.
//
// Os segredos estao no repositorio de proposito: sao credenciais de ambiente local
// de teste, e quem roda os testes precisa delas sem configuracao extra.
func segredoDeTeste(t *testing.T, cliente string) string {
	t.Helper()

	chave := "WAGER_TEST_OIDC_SECRET_" + strings.ToUpper(strings.ReplaceAll(cliente, "-", "_"))
	if valor := os.Getenv(chave); valor != "" {
		return valor
	}
	return cliente + "-secret"
}

// pedirToken faz o fluxo de client_credentials no IdP real.
func pedirToken(t *testing.T, cliente string) string {
	t.Helper()

	endpoint := emissorDeTeste(t) + "/protocol/openid-connect/token"
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", cliente)
	form.Set("client_secret", segredoDeTeste(t, cliente))

	req, err := http.NewRequestWithContext(contextoDeTeste(t), http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("o IdP esta acessivel? %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		corpo, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		t.Fatalf("token de %s: status %d, corpo %s", cliente, resp.StatusCode, corpo)
	}

	var resposta struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&resposta); err != nil {
		t.Fatalf("decodificacao da resposta do IdP: %v", err)
	}
	if resposta.AccessToken == "" {
		t.Fatal("o IdP devolveu token vazio")
	}
	return resposta.AccessToken
}

// validadorDeTeste monta o validador apontado para o realm de teste.
func validadorDeTeste(t *testing.T) *auth.Validador {
	t.Helper()

	emissor := emissorDeTeste(t)
	v, err := auth.NovoValidador(auth.Config{
		Issuer:   emissor,
		Audience: "wager-service",
		URLJWKS:  emissor + "/protocol/openid-connect/certs",
		// Cache curto de proposito: o teste de rotacao e a segunda requisicao nao
		// podem ficar presas a uma chave antiga por cinco minutos.
		CacheJWKS:   10 * time.Second,
		HTTPTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NovoValidador: %v", err)
	}
	return v
}

// O caminho feliz contra o IdP real: o token de um provedor vira ator com o
// provedor certo e apenas o escopo de operacoes.
func TestTokenRealDeProvedorVirarAtor(t *testing.T) {
	ator, err := validadorDeTeste(t).Ator(contextoDeTeste(t), pedirToken(t, "provider-a"))
	if err != nil {
		t.Fatalf("Ator: %v", err)
	}

	if ator.Provedor != "provider-a" {
		t.Errorf("provedor e %q, esperado provider-a", ator.Provedor)
	}
	if ator.Cliente != "provider-a" {
		t.Errorf("cliente e %q, esperado provider-a", ator.Cliente)
	}
	if !ator.TemEscopo(app.EscopoOperacoes) {
		t.Error("falta o escopo de operacoes")
	}
}

// O token do provedor nao carrega escopo de abertura nem de reconciliacao. E o que
// impede um provedor de abrir carteira de terceiro pelo caminho do cliente interno.
func TestTokenDeProvedorNaoTemEscopoInterno(t *testing.T) {
	ator, err := validadorDeTeste(t).Ator(contextoDeTeste(t), pedirToken(t, "provider-a"))
	if err != nil {
		t.Fatalf("Ator: %v", err)
	}

	if ator.TemEscopo(app.EscopoAberturaCarteira) {
		t.Error("provedor recebeu escopo de abertura de carteira")
	}
	if ator.TemEscopo(app.EscopoReconciliacao) {
		t.Error("provedor recebeu escopo de reconciliacao")
	}
	if ator.Interno() {
		t.Error("token de provedor foi reconhecido como cliente interno")
	}
}

// O cliente de servico e interno e tem os escopos internos, e nao o de operacoes.
func TestTokenRealDoServicoVirarAtorInterno(t *testing.T) {
	ator, err := validadorDeTeste(t).Ator(contextoDeTeste(t), pedirToken(t, "wager-service"))
	if err != nil {
		t.Fatalf("Ator: %v", err)
	}

	if !ator.Interno() {
		t.Error("cliente de servico nao foi reconhecido como interno")
	}
	if !ator.TemEscopo(app.EscopoAberturaCarteira) {
		t.Error("falta o escopo de abertura de carteira")
	}
	if !ator.TemEscopo(app.EscopoReconciliacao) {
		t.Error("falta o escopo de reconciliacao")
	}
	if ator.TemEscopo(app.EscopoOperacoes) {
		t.Error("cliente de servico recebeu escopo de operacoes")
	}
}

// Os dois provedores do realm sao atores diferentes. E a base do isolamento: o token
// de A nao pode operar como B.
func TestProvedoresDoRealmSaoAtoresDiferentes(t *testing.T) {
	v := validadorDeTeste(t)

	a, err := v.Ator(contextoDeTeste(t), pedirToken(t, "provider-a"))
	if err != nil {
		t.Fatalf("provider-a: %v", err)
	}
	b, err := v.Ator(contextoDeTeste(t), pedirToken(t, "provider-b"))
	if err != nil {
		t.Fatalf("provider-b: %v", err)
	}

	if a.Provedor == b.Provedor {
		t.Errorf("os dois provedores viraram o mesmo ator: %q", a.Provedor)
	}
}

// Um token adulterado e recusado mesmo tendo audience, emissor e prazo corretos. E
// o teste que a assinatura esta sendo conferida de verdade contra o JWKS do IdP.
func TestTokenAdulteradoERecusadoPeloIdPReal(t *testing.T) {
	token := pedirToken(t, "provider-a")
	partes := strings.Split(token, ".")
	if len(partes) != 3 {
		t.Fatalf("token em formato inesperado: %d partes", len(partes))
	}

	// Troca um caractere do corpo, mantendo o cabecalho e a assinatura.
	corpo := []byte(partes[1])
	pos := len(corpo) / 2
	if corpo[pos] == 'A' {
		corpo[pos] = 'B'
	} else {
		corpo[pos] = 'A'
	}
	adulterado := partes[0] + "." + string(corpo) + "." + partes[2]

	if _, err := validadorDeTeste(t).Ator(contextoDeTeste(t), adulterado); err == nil {
		t.Fatal("token adulterado foi aceito")
	}
}

// Assinatura feita com uma chave que o IdP nao publica e recusada. O JWKS real
// contem a chave do realm, e nenhuma outra.
func TestTokenAssinadoPorChaveEstranhaERecusadoPeloIdPReal(t *testing.T) {
	token := pedirToken(t, "provider-a")
	partes := strings.Split(token, ".")

	// Reescreve o cabecalho para citar um kid que o realm nao publica, mantendo a
	// assinatura original. A assinatura nao valida com nenhuma chave do JWKS.
	cabecalho, err := base64.RawURLEncoding.DecodeString(partes[0])
	if err != nil {
		t.Fatalf("decodificacao do cabecalho: %v", err)
	}
	var mapa map[string]any
	if err := json.Unmarshal(cabecalho, &mapa); err != nil {
		t.Fatalf("decodificacao do cabecalho json: %v", err)
	}
	mapa["kid"] = "chave-que-o-realm-nao-publica"
	novo, err := json.Marshal(mapa)
	if err != nil {
		t.Fatalf("serializacao do cabecalho: %v", err)
	}
	adulterado := base64.RawURLEncoding.EncodeToString(novo) + "." + partes[1] + "." + partes[2]

	if _, err := validadorDeTeste(t).Ator(contextoDeTeste(t), adulterado); !errors.Is(err, auth.ErrTokenInvalido) {
		t.Fatalf("erro e %v, esperado token invalido", err)
	}
}

// Ausencia de credencial e erro proprio, para o cliente distinguir "nao mandei" de
// "mandei e nao serviu".
func TestSemCabecalhoDeAutorizacaoDaErroProprio(t *testing.T) {
	if _, err := validadorDeTeste(t).Ator(contextoDeTeste(t), ""); !errors.Is(err, auth.ErrTokenAusente) {
		t.Fatalf("erro e %v, esperado token ausente", err)
	}
}

// A chave e cacheada entre requisicoes. Repetir a validacao nao pode custar uma
// chamada ao IdP por requisicao.
func TestChaveDoIdpRealECacheada(t *testing.T) {
	v := validadorDeTeste(t)
	token := pedirToken(t, "provider-a")

	// A primeira chamada popula o cache; as seguintes tem de vir do cache.
	if _, err := v.Ator(contextoDeTeste(t), token); err != nil {
		t.Fatalf("primeira: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := v.Ator(contextoDeTeste(t), token); err != nil {
			t.Fatalf("requisicao %d: %v", i, err)
		}
	}
}

func contextoDeTeste(t *testing.T) context.Context {
	t.Helper()

	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancelar)
	return ctx
}
