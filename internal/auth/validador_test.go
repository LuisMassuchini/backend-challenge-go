package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
)

// idpDeTeste e um IdP local: serve o JWKS e assina tokens com uma chave que ele
// acabou de gerar.
type idpDeTeste struct {
	// servidor e o JWKS local.
	servidor *httptest.Server

	// chave e o par usado para assinar.
	chave *rsa.PrivateKey

	// kid e o identificador da chave.
	kid string

	// emissor e o issuer que os tokens carregam.
	emissor string

	// audiencia e a audiencia que os tokens carregam.
	audiencia string

	// chamadas conta as buscas no JWKS, para provar o cache.
	chamadas atomic.Int64
}

// novoIdpDeTeste sobe o IdP local.
func novoIdpDeTeste(t *testing.T) *idpDeTeste {
	t.Helper()

	chave, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}

	idp := &idpDeTeste{
		chave:     chave,
		kid:       "chave-de-teste",
		emissor:   "http://localhost:8080/realms/wager",
		audiencia: "wager-service",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/realms/wager/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		idp.chamadas.Add(1)
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck
		json.NewEncoder(w).Encode(documentoJWKSDeTeste(idp.kid, idp.chave))
	})
	idp.servidor = httptest.NewServer(mux)
	t.Cleanup(idp.servidor.Close)

	return idp
}

// documentoJWKSDeTeste monta o JWKS a partir da chave publica.
func documentoJWKSDeTeste(kid string, chave *rsa.PrivateKey) map[string]any {
	modulo := base64.RawURLEncoding.EncodeToString(chave.N.Bytes())
	expoente := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(chave.E)).Bytes())
	return map[string]any{
		"keys": []map[string]string{{
			"kid": kid,
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"n":   modulo,
			"e":   expoente,
		}},
	}
}

// validador devolve um validador apontado para o IdP local.
func (i *idpDeTeste) validador(t *testing.T) *Validador {
	t.Helper()

	v, err := NovoValidador(Config{
		Issuer:      i.emissor,
		Audience:    i.audiencia,
		URLJWKS:     i.servidor.URL + "/realms/wager/protocol/openid-connect/certs",
		CacheJWKS:   time.Hour,
		HTTPTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NovoValidador: %v", err)
	}
	return v
}

// opcoesDoToken sao os claim vars que um teste quer mexer.
type opcoesDoToken struct {
	azp      string
	papeis   []string
	escopos  string
	emissor  string
	audicia  string
	kid      string
	validade time.Duration
	semExp   bool
}

// assinar emite um token com as opcoes dadas.
func (i *idpDeTeste) assinar(t *testing.T, o opcoesDoToken) string {
	t.Helper()

	if o.azp == "" {
		o.azp = "provider-a"
	}
	if o.escopos == "" {
		o.escopos = app.EscopoOperacoes
	}
	if o.emissor == "" {
		o.emissor = i.emissor
	}
	if o.audicia == "" {
		o.audicia = i.audiencia
	}
	if o.kid == "" {
		o.kid = i.kid
	}
	if o.validade == 0 {
		o.validade = time.Hour
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   o.emissor,
		"aud":   o.audicia,
		"exp":   time.Now().Add(o.validade).Unix(),
		"azp":   o.azp,
		"scope": o.escopos,
		"realm_access": map[string]any{
			"roles": o.papeis,
		},
	})
	if o.semExp {
		delete(token.Claims.(jwt.MapClaims), "exp")
	}
	token.Header["kid"] = o.kid

	bruto, err := token.SignedString(i.chave)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return bruto
}

// O caminho feliz: token de provedor vira Ator com provedor e escopo.
func TestTokenDeProvedorViraAtor(t *testing.T) {
	idp := novoIdpDeTeste(t)

	ator, err := idp.validador(t).Ator(contextoDeTeste(t),
		idp.assinar(t, opcoesDoToken{
			azp:     "provider-a",
			papeis:  []string{"provider-a"},
			escopos: app.EscopoOperacoes,
		}))
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
		t.Error("ator sem o escopo de operacoes")
	}
	if ator.Interno() {
		t.Error("ator de provedor foi classificado como interno")
	}
}

// O token do cliente de servico nao tem provedor, e por isso abre carteira e
// reconcilia. A separacao vem do papel, nao do nome do cliente.
func TestTokenDoClienteDeServicoNaoTemProvedor(t *testing.T) {
	idp := novoIdpDeTeste(t)

	ator, err := idp.validador(t).Ator(contextoDeTeste(t),
		idp.assinar(t, opcoesDoToken{
			azp:     FuncaoInterna,
			papeis:  []string{FuncaoInterna},
			escopos: app.EscopoAberturaCarteira + " " + app.EscopoReconciliacao,
		}))
	if err != nil {
		t.Fatalf("Ator: %v", err)
	}

	if !ator.Interno() {
		t.Error("cliente de servico nao foi reconhecido como interno")
	}
	if ator.TemEscopo(app.EscopoOperacoes) {
		t.Error("cliente de servico ganhou escopo de operacoes")
	}
}

// A assinatura e o que vale. Um token bem formado assinado por outra chave nao
// passa, mesmo com emissor, audiencia e prazo perfeitos.
func TestTokenAssinadoPorOutraChaveERecusado(t *testing.T) {
	idp := novoIdpDeTeste(t)

	outra, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   idp.emissor,
		"aud":   idp.audiencia,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"azp":   "provider-a",
		"scope": app.EscopoOperacoes,
		"realm_access": map[string]any{
			"roles": []string{"provider-a"},
		},
	})
	token.Header["kid"] = idp.kid
	bruto, err := token.SignedString(outra)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	if _, err := idp.validador(t).Ator(contextoDeTeste(t), bruto); !errors.Is(err, ErrTokenInvalido) {
		t.Fatalf("erro e %v, esperado token invalido", err)
	}
}

// O token declara o algoritmo no cabecalho, e quem decide e o validador. Um token
// "none", sem assinatura, e o ataque classico.
func TestTokenSemAssinaturaERecusado(t *testing.T) {
	idp := novoIdpDeTeste(t)

	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"iss":   idp.emissor,
		"aud":   idp.audiencia,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"azp":   "provider-a",
		"scope": app.EscopoOperacoes,
	})
	token.Header["kid"] = idp.kid
	bruto, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}

	if _, err := idp.validador(t).Ator(contextoDeTeste(t), bruto); err == nil {
		t.Fatal("token sem assinatura foi aceito")
	}
}

// Emissor e audiencia prendem o token a este servico. Sem isso, um token valido de
// outro servico do mesmo realm seria aceito aqui.
func TestEmissorEAudienciaErradosSaoRecusados(t *testing.T) {
	idp := novoIdpDeTeste(t)
	v := idp.validador(t)

	if _, err := v.Ator(contextoDeTeste(t), idp.assinar(t, opcoesDoToken{
		emissor: "http://outro-servidor/realms/wager",
	})); !errors.Is(err, ErrTokenInvalido) {
		t.Errorf("emissor errado deu %v, esperado recusa", err)
	}

	if _, err := v.Ator(contextoDeTeste(t), idp.assinar(t, opcoesDoToken{
		audicia: "outro-servico",
	})); !errors.Is(err, ErrTokenInvalido) {
		t.Errorf("audiencia errada deu %v, esperado recusa", err)
	}
}

// Expirado e recusado, e o erro e distinguivel para o cliente.
func TestTokenExpiradoERecusado(t *testing.T) {
	idp := novoIdpDeTeste(t)

	_, err := idp.validador(t).Ator(contextoDeTeste(t), idp.assinar(t, opcoesDoToken{
		validade: -time.Hour,
	}))
	if !errors.Is(err, ErrTokenExpirado) {
		t.Fatalf("erro e %v, esperado token expirado", err)
	}
}

// Token sem exp nao e aceito: aceitar um token sem prazo seria aceitar um token
// eterno.
func TestTokenSemPrazoERecusado(t *testing.T) {
	idp := novoIdpDeTeste(t)

	if _, err := idp.validador(t).Ator(contextoDeTeste(t), idp.assinar(t, opcoesDoToken{
		semExp: true,
	})); !errors.Is(err, ErrTokenInvalido) {
		t.Fatalf("erro e %v, esperado recusa de token sem exp", err)
	}
}

// Kid desconhecido e recusado: o token aponta para uma chave que o IdP nao publica.
func TestKidDesconhecidoERecusado(t *testing.T) {
	idp := novoIdpDeTeste(t)

	_, err := idp.validador(t).Ator(contextoDeTeste(t), idp.assinar(t, opcoesDoToken{
		kid: "chave-que-nao-existe",
	}))
	if !errors.Is(err, ErrTokenInvalido) {
		t.Fatalf("erro e %v, esperado recusa", err)
	}
}

// Token com dois papeis de provedor nao tem provedor definido, e escolher um deles
// daria ao token um alcance que o IdP nao concedeu.
func TestTokenComDoisPapeisDeProvedorERecusado(t *testing.T) {
	idp := novoIdpDeTeste(t)

	_, err := idp.validador(t).Ator(contextoDeTeste(t), idp.assinar(t, opcoesDoToken{
		azp:    "provider-a",
		papeis: []string{"provider-a", "provider-b"},
	}))
	if !errors.Is(err, ErrProvedorAmbiguo) {
		t.Fatalf("erro e %v, esperado ambiguidade de provedor", err)
	}
}

// O cache evita uma ida ao IdP por requisicao, que e o ponto de cachear a chave.
func TestChaveECacheadaEntreRequisicoes(t *testing.T) {
	idp := novoIdpDeTeste(t)
	v := idp.validador(t)
	token := idp.assinar(t, opcoesDoToken{papeis: []string{"provider-a"}})

	for i := 0; i < 5; i++ {
		if _, err := v.Ator(contextoDeTeste(t), token); err != nil {
			t.Fatalf("requisicao %d: %v", i, err)
		}
	}

	if got := idp.chamadas.Load(); got != 1 {
		t.Errorf("buscas no JWKS e %d, esperado 1: a chave deveria estar em cache", got)
	}
}

// Rotacao de chave: um token novo, assinado por uma chave recem-publicada, entra
// em vigor sem reiniciar o processo.
func TestRotacaoDeChaveEntraEmVigor(t *testing.T) {
	idp := novoIdpDeTeste(t)
	v := idp.validador(t)

	if _, err := v.Ator(contextoDeTeste(t), idp.assinar(t, opcoesDoToken{
		papeis: []string{"provider-a"},
	})); err != nil {
		t.Fatalf("primeira chave: %v", err)
	}

	// O IdP publica uma chave nova e passa a assinar com ela.
	nova, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	idp.chave = nova
	idp.kid = "chave-nova"

	token := idp.assinar(t, opcoesDoToken{papeis: []string{"provider-a"}})
	// O token novo cita um kid que o cache ainda nao viu, entao o cache busca.
	ator, err := v.Ator(contextoDeTeste(t), token)
	if err != nil {
		t.Fatalf("token da chave nova: %v", err)
	}
	if ator.Provedor != "provider-a" {
		t.Errorf("provedor e %q, esperado provider-a", ator.Provedor)
	}
}

// Ausencia de credencial e erro proprio, e nao token invalido: o cliente precisa
// saber que forgot o cabecalho.
func TestTokenAusenteTemErroProprio(t *testing.T) {
	idp := novoIdpDeTeste(t)

	if _, err := idp.validador(t).Ator(contextoDeTeste(t), "  "); !errors.Is(err, ErrTokenAusente) {
		t.Fatalf("erro e %v, esperado token ausente", err)
	}
}

// Configuracao incompleta falha na construcao, e nao na primeira requisicao.
func TestConfiguracaoIncompletaFalhaNaConstrucao(t *testing.T) {
	if _, err := NovoValidador(Config{Audience: "wager-service"}); err == nil {
		t.Fatal("validador sem emissor foi construido")
	}
}

// ExtrairToken le o valor do cabecalho Authorization.
//
// O esquema e conferido aqui e nao no middleware: "Bearer" escrito de outra forma
// e um erro de cliente, e a resposta precisa ser 401 com o motivo certo.
func TestExtrairTokenConfereOSesquema(t *testing.T) {
	casos := map[string]struct {
		cabecalho string
		quer      string
	}{
		"token simples":   {"Bearer abc.def.ghi", "abc.def.ghi"},
		"minusculas":      {"bearer abc.def.ghi", "abc.def.ghi"},
		"espacos a volta": {"Bearer   abc.def.ghi", "abc.def.ghi"},
		"sem esquema":     {"abc.def.ghi", ""},
		"outro esquema":   {"Basic abc", ""},
		"so o esquema":    {"Bearer", ""},
		"vazio":           {"", ""},
	}

	for nome, caso := range casos {
		t.Run(nome, func(t *testing.T) {
			obtido := ExtrairToken(caso.cabecalho)
			if obtido != caso.quer {
				t.Errorf("ExtrairToken(%q) = %q, esperado %q", caso.cabecalho, obtido, caso.quer)
			}
		})
	}
}

// Um token que nao tem os tres ponto do JWT nao deve chegar ao parser.
func TestTokenMalformadoERecusado(t *testing.T) {
	idp := novoIdpDeTeste(t)

	for _, bruto := range []string{"nao-e-token", "a.b", strings.Repeat("x", 50)} {
		if _, err := idp.validador(t).Ator(contextoDeTeste(t), bruto); err == nil {
			t.Errorf("token malformado %q foi aceito", bruto)
		}
	}
}

// A chave publica precisa ser serializavel: o JWKS do IdP e JSON, e uma chave que
// nao sobrevive a serializacao nunca entraria em producao.
func TestChaveSerializaEmJWKS(t *testing.T) {
	idp := novoIdpDeTeste(t)
	documento := documentoJWKSDeTeste(idp.kid, idp.chave)

	bruto, err := json.Marshal(documento)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var lido jwksDocumento
	if err := json.Unmarshal(bruto, &lido); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(lido.Chaves) != 1 || lido.Chaves[0].Kid != idp.kid {
		t.Fatalf("documento nao sobreviveu a ida e volta: %+v", lido)
	}
}

// x509 e usado so para garantir que a chave gerada serve para assinar, que e o que
// o IdP faria. Sem este teste, uma falha de geracao so apareceria no primeiro token.
func TestChaveGeradaAssinaEVerifica(t *testing.T) {
	idp := novoIdpDeTeste(t)

	bruto := idp.assinar(t, opcoesDoToken{papeis: []string{"provider-a"}})
	token, err := jwt.Parse(bruto, func(*jwt.Token) (any, error) { return &idp.chave.PublicKey, nil })
	if err != nil || !token.Valid {
		t.Fatalf("a chave gerada nao verifica o proprio token: %v", err)
	}

	//nolint:errcheck
	x509.MarshalPKCS1PublicKey(&idp.chave.PublicKey)
}

func contextoDeTeste(t *testing.T) context.Context {
	t.Helper()

	ctx, cancelar := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancelar)
	return ctx
}
