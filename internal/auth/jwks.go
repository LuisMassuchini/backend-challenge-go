package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// cacheJWKS guarda as chaves publicas do IdP.
//
// O guard e um mutex com um unico cache, e nao um mapa por kid com evicted: o IdP
// tem poucas chaves e a rotacao e rara, entao o ganho de um cache sofisticado nao
// paga a complexidade. O que importa e que a busca e serializada, porque sem isso
// uma rotacao de chave derruba todas as requisicoes do processo em uma enxurrada de
// chamadas simultaneas ao IdP.
type cacheJWKS struct {
	url     string
	ttl     time.Duration
	timeout time.Duration

	mu      sync.Mutex
	chaves  map[string]*rsa.PublicKey
	buscado time.Time
}

// jwksDocumento e o formato do endpoint de chaves do IdP.
type jwksDocumento struct {
	// Chaves e a lista de chaves publicadas.
	Chaves []struct {
		// Kid e o identificador que o token cita no cabecalho.
		Kid string `json:"kid"`
		// Kty precisa ser RSA: e o unico tipo que este servico aceita.
		Kty string `json:"kty"`
		// N e o modulo da chave, em base64url.
		N string `json:"n"`
		// E e o expoente da chave, em base64url.
		E string `json:"e"`
		// Use marca a chave destinada a verificar assinatura.
		Use string `json:"use"`
	} `json:"keys"`
}

// tamanhoMaximoJWKS limita o corpo lido do endpoint de chaves.
//
// O limite existe porque a resposta vem da rede: um IdP que devolve corpo sem fim
// deixaria o processo crescer ate o fim da memoria em vez de recusar o token.
const tamanhoMaximoJWKS = 1 << 20

// bitsMinimosDeChave e o piso do tamanho do modulo.
//
// Abaixo de 2048 bits a assinatura pode ser forjada com esforco razoavel, e um token
// forjado e exatamente o que este servico precisa impedir. Recusar a chave curta e
// melhor do que aceitar um token que nao significa nada.
const bitsMinimosDeChave = 2048

// novoCacheJWKS constroi o cache.
func novoCacheJWKS(url string, ttl, timeout time.Duration) *cacheJWKS {
	return &cacheJWKS{url: url, ttl: ttl, timeout: timeout}
}

// buscar devolve a chave do kid, buscando no IdP quando o cache nao resolve.
func (c *cacheJWKS) buscar(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	chave, achou := c.chaves[kid]
	expirada := time.Since(c.buscado) > c.ttl
	c.mu.Unlock()

	if achou && !expirada {
		return chave, nil
	}

	// O cache vencido, ou um kid que ele nao conhece, vao para o IdP. Este e tambem
	// o caminho da rotacao de chave: o token novo cita um kid que o cache ainda nao
	// viu, e a busca acontece.
	chaves, err := c.buscarNoIdP(ctx)
	if err != nil {
		if achou {
			// Chave conhecida com cache vencido e IdP fora do ar. Recusar o token
			// derrubaria o servico inteiro por causa de uma indisponibilidade do
			// IdP, e o token continua valido: foi assinado por uma chave que o IdP
			// publicou e ainda nao retirou.
			return chave, nil
		}
		return nil, err
	}

	c.mu.Lock()
	c.chaves = chaves
	c.buscado = time.Now()
	c.mu.Unlock()

	chave, achou = chaves[kid]
	if !achou {
		return nil, fmt.Errorf("%w: kid %q nao publicado pelo IdP", ErrTokenInvalido, kid)
	}
	return chave, nil
}

// buscarNoIdP busca e monta o documento de chaves.
func (c *cacheJWKS) buscarNoIdP(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	ctx, cancelar := context.WithTimeout(ctx, c.timeout)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalido, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: JWKS indisponivel: %v", ErrTokenInvalido, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: JWKS respondeu %d", ErrTokenInvalido, resp.StatusCode)
	}

	corpo, err := io.ReadAll(io.LimitReader(resp.Body, tamanhoMaximoJWKS))
	if err != nil {
		return nil, fmt.Errorf("%w: leitura do JWKS: %v", ErrTokenInvalido, err)
	}

	var documento jwksDocumento
	if err := json.Unmarshal(corpo, &documento); err != nil {
		return nil, fmt.Errorf("%w: JWKS malformado: %v", ErrTokenInvalido, err)
	}

	chaves := make(map[string]*rsa.PublicKey, len(documento.Chaves))
	for _, k := range documento.Chaves {
		// Uma chave de encriptacao nao verifica assinatura. Filtrar pelo uso e o que
		// impede que um token aponte para uma chave que o IdP publica para outro fim.
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}

		// Uma chave malformada e ignorada em vez de derrubar o documento inteiro: o
		// IdP pode publicar uma chave em rotacao, e recusar todas por causa dela
		// seria pior do que usar as que prestam.
		//nolint:errcheck
		if chave, err := montarRSA(k.N, k.E); err == nil {
			chaves[k.Kid] = chave
		}
	}

	if len(chaves) == 0 {
		return nil, fmt.Errorf("%w: JWKS sem chave RSA utilizavel", ErrTokenInvalido)
	}
	return chaves, nil
}

// montarRSA decodifica o modulo e o expoente em base64url e monta a chave publica.
func montarRSA(modulo, expoente string) (*rsa.PublicKey, error) {
	brutoModulo, err := base64.RawURLEncoding.DecodeString(modulo)
	if err != nil {
		return nil, fmt.Errorf("modulo nao e base64url: %w", err)
	}
	brutoExpoente, err := base64.RawURLEncoding.DecodeString(expoente)
	if err != nil {
		return nil, fmt.Errorf("expoente nao e base64url: %w", err)
	}

	n := new(big.Int).SetBytes(brutoModulo)
	e := new(big.Int).SetBytes(brutoExpoente)
	if !e.IsInt64() {
		return nil, fmt.Errorf("expoente fora do alcance")
	}

	// O expoente de uma chave publica RSA e impar e por definicao pelo menos 3.
	valor := e.Int64()
	if valor < 3 || valor%2 == 0 {
		return nil, fmt.Errorf("expoente invalido: %d", valor)
	}
	if n.BitLen() < bitsMinimosDeChave {
		return nil, fmt.Errorf("modulo curto: %d bits", n.BitLen())
	}

	return &rsa.PublicKey{N: n, E: int(valor)}, nil
}
