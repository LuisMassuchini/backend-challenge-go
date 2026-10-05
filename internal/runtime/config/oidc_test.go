package config

import (
	"strings"
	"testing"
	"time"
)

// ambienteDeTeste monta um getter de variavel de ambiente a partir de um mapa.
func ambienteDeTeste(valores map[string]string) func(string) (string, bool) {
	return func(chave string) (string, bool) {
		valor, existe := valores[chave]
		return valor, existe
	}
}

// Sem nenhuma variavel de OIDC a aplicacao sobe sem autenticacao. E o que permite
// rodar migrations e testes de dominio sem um IdP no ar.
func TestOIDCAusenteDeixaGrupoVazio(t *testing.T) {
	cfg, err := FromEnv(ambienteDeTeste(map[string]string{}))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	if cfg.OIDC.Issuer != "" || cfg.OIDC.Audience != "" || cfg.OIDC.URLJWKS != "" {
		t.Errorf("grupo OIDC deveria vir vazio, veio %+v", cfg.OIDC)
	}
}

// Issuer e Audience juntos formam o par que prende o token a este servico. Aceitar
// um sem o outro e aceitar token de qualquer cliente do realm.
func TestAudienceSemIssuerFalha(t *testing.T) {
	_, err := FromEnv(ambienteDeTeste(map[string]string{
		ChaveOIDCAudience: "wager-service",
	}))

	var erro *ValidationError
	if err == nil {
		t.Fatal("audience sem issuer foi aceita")
	}
	if !strings.Contains(err.Error(), ChaveOIDCIssuer) {
		t.Errorf("erro e %q, deveria apontar %s", err, ChaveOIDCIssuer)
	}
	_ = erro
}

// Um issuer com barra no fim nao pode gerar um JWKS com barra dupla, porque o
// resultado seria um 404 em vez de um token recusado.
func TestIssuerComBarraNoFimNaoDuplicaBarraNoJWKS(t *testing.T) {
	cfg, err := FromEnv(ambienteDeTeste(map[string]string{
		ChaveOIDCIssuer:   "http://localhost:8081/realms/wager/",
		ChaveOIDCAudience: "wager-service",
	}))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	if cfg.OIDC.URLJWKS != "http://localhost:8081/realms/wager"+caminhoPadraoJWKS {
		t.Errorf("JWKS e %q", cfg.OIDC.URLJWKS)
	}
	if strings.Contains(cfg.OIDC.URLJWKS, "//protocol") {
		t.Errorf("JWKS com barra dupla: %q", cfg.OIDC.URLJWKS)
	}
	if cfg.OIDC.Issuer != "http://localhost:8081/realms/wager" {
		t.Errorf("issuer ficou %q, com barra", cfg.OIDC.Issuer)
	}
}

// Os prazos tem padrao, e o padrao nao e zero: um cache de chave zero significa
// buscar no IdP em toda requisicao.
func TestPrazosDeOIDCTemPadraoSensato(t *testing.T) {
	cfg, err := FromEnv(ambienteDeTeste(map[string]string{
		ChaveOIDCIssuer:   "http://localhost:8081/realms/wager",
		ChaveOIDCAudience: "wager-service",
	}))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	if cfg.OIDC.CacheJWKS <= 0 {
		t.Error("cache de JWKS sem padrao")
	}
	if cfg.OIDC.MargemDeRelogio <= 0 {
		t.Error("margem de relogio sem padrao")
	}
	if cfg.OIDC.HTTPTimeout <= 0 {
		t.Error("timeout do IdP sem padrao")
	}
	// A margem precisa ser menor que a validade util do token, senao todo token
	// pareceria expirado.
	if cfg.OIDC.MargemDeRelogio >= 5*time.Minute {
		t.Errorf("margem de %s e maior que a validade do token do realm", cfg.OIDC.MargemDeRelogio)
	}
}

// Os prazos podem ser informados, e uma duracao invalida aponta a variavel.
func TestPrazosDeOIDCPodemSerInformados(t *testing.T) {
	cfg, err := FromEnv(ambienteDeTeste(map[string]string{
		ChaveOIDCIssuer:        "http://localhost:8081/realms/wager",
		ChaveOIDCAudience:      "wager-service",
		ChaveOIDCCacheJWKS:     "90s",
		ChaveOIDCMargemRelogio: "10s",
		ChaveOIDCHTTPTimeout:   "2s",
	}))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	if cfg.OIDC.CacheJWKS != 90*time.Second {
		t.Errorf("cache e %s, esperado 90s", cfg.OIDC.CacheJWKS)
	}
	if cfg.OIDC.MargemDeRelogio != 10*time.Second {
		t.Errorf("margem e %s, esperado 10s", cfg.OIDC.MargemDeRelogio)
	}
	if cfg.OIDC.HTTPTimeout != 2*time.Second {
		t.Errorf("timeout e %s, esperado 2s", cfg.OIDC.HTTPTimeout)
	}
}

func TestPrazosDeOIDCInvalidosApontamAVariavel(t *testing.T) {
	_, err := FromEnv(ambienteDeTeste(map[string]string{
		ChaveOIDCIssuer:    "http://localhost:8081/realms/wager",
		ChaveOIDCAudience:  "wager-service",
		ChaveOIDCCacheJWKS: "cinco minutos",
	}))

	var erro *ValidationError
	if !erroDeValidacao(err, &erro) {
		t.Fatalf("erro e %v, esperado erro de validacao", err)
	}
	if erro.Field != ChaveOIDCCacheJWKS {
		t.Errorf("apontou %q, esperado %q", erro.Field, ChaveOIDCCacheJWKS)
	}
}

// Variavel presente e vazia e erro, e nao ausencia. Um grupo pela metade e erro de
// configuracao, e nao configuracao padrao.
func TestVariavelOIDCApresenteEVaziaFalha(t *testing.T) {
	casos := map[string]map[string]string{
		"issuer vazio": {
			ChaveOIDCIssuer: "",
		},
		"issuer em branco": {
			ChaveOIDCIssuer: "   ",
		},
		"audience vazia": {
			ChaveOIDCIssuer:   "http://localhost:8081/realms/wager",
			ChaveOIDCAudience: "",
		},
		"jwks vazio": {
			ChaveOIDCIssuer:   "http://localhost:8081/realms/wager",
			ChaveOIDCAudience: "wager-service",
			ChaveOIDCURLJWKS:  "",
		},
	}

	for nome, valores := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := FromEnv(ambienteDeTeste(valores)); err == nil {
				t.Fatal("configuracao pela metade foi aceita")
			}
		})
	}
}

// O grupo ausente aceita HTTP sem OIDC. Uma configuracao de producao que esqueceu
// o issuer precisa continuar funcionando, com os endpoints protegidos recusando
// por falta de autenticacao, e nao derrubando o processo na subida.
func TestConfiguracaoDeHTTPContinuaValendoSemOIDC(t *testing.T) {
	cfg, err := FromEnv(ambienteDeTeste(map[string]string{
		ChaveHTTPAddress: ":9090",
	}))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.HTTP.Address != ":9090" {
		t.Errorf("endereco e %q", cfg.HTTP.Address)
	}
}

// erroDeValidacao diz se o erro e um erro de validacao e o entrega.
func erroDeValidacao(err error, destino **ValidationError) bool {
	alvo, ok := err.(*ValidationError)
	if !ok {
		return false
	}
	*destino = alvo
	return true
}
