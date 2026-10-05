// Package auth valida o token OIDC e traduz o que ele autoriza.
//
// A fronteira entre o IdP e o dominio e este pacote, e e a unica. O dominio nao
// conhece JWT, chave, emissor nem audiencia: ele recebe um app.Ator, que ja foi
// verificado. E por isso que um caso de uso nao consegue ser burlado por um token
// bem formado mas nao assinado pela chave certa.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
)

// Erros de autenticacao.
//
// Sao todos distintos porque o cliente precisa saber a diferenca entre "voce nao
// mandou credencial", "a credencial nao serve" e "a credencial serve mas nao
// autoriza isto". Uma resposta unica para os tres casos seria mais simples e
// menos util.
var (
	// ErrTokenAusente e a ausencia do cabecalho de autorizacao.
	ErrTokenAusente = errors.New("auth: token ausente")

	// ErrTokenInvalido cobre token que nao passou na verificacao de assinatura,
	// emissor, audiencia ou formato.
	ErrTokenInvalido = errors.New("auth: token invalido")

	// ErrTokenExpirado e token fora do prazo.
	ErrTokenExpirado = errors.New("auth: token expirado")

	// ErrProvedorAmbiguo e token que carrega mais de um papel de provedor.
	//
	// Existe porque um token com dois papeis nao tem um provedor, e escolher um
	// deles silenciosamente daria ao token um alcance que o IdP nao concedeu.
	ErrProvedorAmbiguo = errors.New("auth: token com mais de um papel de provedor")
)

// FuncaoInterna e o nome do papel do cliente de servico.
//
// O papel vem do realm, e nao de um claim livre: e o realm que diz quais papeis
// existem, e o processo so precisa saber qual deles nao e um provedor.
const FuncaoInterna = "wager-service"

// Config e o endereco do IdP.
//
// Issuer e Audience sao o que prende o token a este servico. Sem eles, um token
// valido de qualquer outro cliente do mesmo Keycloak -- um token de outro servico
// que usa o mesmo realm -- seria aceito aqui, porque a assinatura estaria correta.
type Config struct {
	// Issuer e o valor esperado do claim iss.
	Issuer string

	// Audience e o valor esperado do claim aud.
	Audience string

	// URLJWKS e onde buscar as chaves publicas.
	URLJWKS string

	// CacheJWKS e quanto tempo uma chave buscada continua valendo.
	//
	// Existe para que cada requisicao de negocio nao pague uma ida ao IdP. O
	// compromisso e o de sempre em cache de chave: uma janela curta de rejeicao
	// apos a rotacao, trocada por nao depender do IdP no caminho da requisicao.
	CacheJWKS time.Duration

	// MargemDeRelogio e quanto antes do vencimento o token e considerado invalido.
	//
	// Sem margem, um token aceito no ultimo instante vence no meio do processamento
	// da requisicao, e o trabalho e feito sem mais autorizacao valida.
	MargemDeRelogio time.Duration

	// HTTPTimeout limita a ida ate o IdP buscar as chaves.
	//
	// E obrigatorio: sem timeout, um IdP travado segura a requisicao de negocio ate
	// o lock timeout do banco expirar, e o cliente recebe um erro de banco para um
	// problema de rede.
	HTTPTimeout time.Duration
}

// esquemaBearer e o prefixo que o OAuth2 define para o token de acesso.
const esquemaBearer = "bearer"

// ExtrairToken le o token do cabecalho Authorization.
//
// Devolve string vazia quando o cabecalho nao serve, e o chamador decide o que
// responder. A funcao se limita a ler: quem decide entre 401 e 403 depende do que a
// rota exige, e essa decisao nao e do extrator.
//
// O esquema e conferido sem diferenciar maiusculas de minusculas porque o RFC
// define o valor como case-insensitive, e um cliente que envia "bearer" mandou a
// credencial certa.
func ExtrairToken(cabecalho string) string {
	esquema, token, achou := strings.Cut(strings.TrimSpace(cabecalho), " ")
	if !achou || !strings.EqualFold(esquema, esquemaBearer) {
		return ""
	}
	return strings.TrimSpace(token)
}

// Validador verifica tokens e devolve o ator.
//
// Nao guarda estado entre requisicoes alem do cache de chaves, e seguro para uso
// concorrente.
type Validador struct {
	config Config
	chaves *cacheJWKS
	agora  func() time.Time
}

// NovoValidador constroi o validador.
func NovoValidador(cfg Config) (*Validador, error) {
	if cfg.Issuer == "" || cfg.Audience == "" || cfg.URLJWKS == "" {
		return nil, fmt.Errorf(
			"%w: emissor, audiencia e URL do JWKS sao obrigatorios", ErrTokenInvalido)
	}
	if cfg.CacheJWKS <= 0 {
		cfg.CacheJWKS = 5 * time.Minute
	}
	if cfg.MargemDeRelogio <= 0 {
		cfg.MargemDeRelogio = 30 * time.Second
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 3 * time.Second
	}

	return &Validador{
		config: cfg,
		chaves: novoCacheJWKS(cfg.URLJWKS, cfg.CacheJWKS, cfg.HTTPTimeout),
		agora:  time.Now,
	}, nil
}

// Config devolve a configuracao em uso.
func (v *Validador) Config() Config { return v.config }

// Ator valida o token e devolve quem ele autentica.
//
// A ordem das verificacoes e do mais barato para o mais caro, e importa: token
// malformado e recusado sem nenhuma ida ao IdP. So quando o token precisa de uma
// chave que o cache nao tem e que o cache ainda nao buscou existe uma ida ao IdP.
func (v *Validador) Ator(ctx context.Context, bruto string) (app.Ator, error) {
	// O corte acontece aqui e nao so no extrator do cabecalho: um chamante que
	// monta o token a mao nao passa pelo extrator, e um token so com espacos e a
	// mesma coisa que token nenhum.
	bruto = strings.TrimSpace(bruto)
	if bruto == "" {
		return app.Ator{}, ErrTokenAusente
	}

	// A margem de relogio entra aqui e nao nas opcoes do parser porque ela e uma
	// decisao de negocio do servico, e nao uma opcao da biblioteca.
	agora := v.agora()
	relogio := jwt.WithTimeFunc(func() time.Time { return agora })
	margem := jwt.WithLeeway(v.config.MargemDeRelogio)

	chave := func(token *jwt.Token) (any, error) {
		alg, ok := token.Method.(*jwt.SigningMethodRSA)
		if !ok || alg.Alg() != "RS256" {
			// Aceitar o algoritmo que o proprio token declara e o caminho classico
			// para o ataque de troca de algoritmo: o token pede "none" ou HMAC com a
			// chave publica, e a biblioteca obedece. Quem decide o algoritmo e o
			// validador.
			return nil, fmt.Errorf("%w: algoritmo %v nao aceito", ErrTokenInvalido, token.Header["alg"])
		}

		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("%w: token sem kid", ErrTokenInvalido)
		}

		return v.chaves.buscar(ctx, kid)
	}

	token, err := jwt.ParseWithClaims(bruto, &claims{}, chave, relogio, margem,
		jwt.WithIssuer(v.config.Issuer),
		jwt.WithAudience(v.config.Audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return app.Ator{}, fmt.Errorf("%w: %v", ErrTokenExpirado, err)
		}
		return app.Ator{}, fmt.Errorf("%w: %v", ErrTokenInvalido, err)
	}

	verificadas, ok := token.Claims.(*claims)
	if !ok || !token.Valid {
		return app.Ator{}, fmt.Errorf("%w: token nao validado", ErrTokenInvalido)
	}

	return verificadas.ator()
}
