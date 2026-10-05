package auth

import (
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
)

// claims e o que o validador le do token.
//
// E uma struct, e nao um map, para que o token traga um campo que nao foi previsto
// aqui e o banco de claims continue funcionando: o espaco de nomes do token e do
// IdP, e este processo e consumidor dele.
type claims struct {
	jwt.RegisteredClaims

	// Scope e a lista de escopos separados por espaco, como o OAuth2 define.
	Scope string `json:"scope"`

	// Azp e o client_id de quem pediu o token.
	//
	// E a identidade do cliente autenticado. Quando o token e de servico, o proprio
	// azp identifica quem e, e por isso ele vai para o Ator.
	Azp string `json:"azp"`

	// RealmAccess traz os papeis do realm.
	//
	// Nao e a fonte da identidade, que vem do Azp. Existe para ser conferido contra o
	// Azp: quando o realm concede um papel, o papel precisa concordar com quem o token
	// diz ser.
	RealmAccess struct {
		// Roles e a lista de papeis do realm.
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// ator traduz as claims em Ator.
//
// A traducao e o unico lugar onde o token vira permissao. O resto do sistema ve
// apenas o Ator, e nao sabe de onde ele veio.
//
// A identidade vem do `azp`, o client_id de quem pediu o token, e nao de um claim de
// papel. `azp` e autenticado: esta dentro da assinatura, entao quem nao for o
// cliente nao consegue apontar o token para ele. Um papel de realm seria uma segunda
// fonte de identidade que precisa ser conferida contra a primeira, e divergencia
// entre as duas e sempre um erro de configuracao esperando para virar brecha.
func (c *claims) ator() (app.Ator, error) {
	if c.Azp == "" {
		// Sem `azp` nao ha a quem atribuir a operacao. Aceitar assim transformaria
		// qualquer token do realm em um ator sem dono, e o caso de uso nao teria como
		// comparar o provedor.
		return app.Ator{}, fmt.Errorf("%w: token sem azp", ErrTokenInvalido)
	}

	escopos := strings.Fields(c.Scope)

	// O papel, quando existe, so pode confirmar a identidade. Divergir do `azp` e
	// sinal de realm mal configurado, e aceitar qualquer um dos dois faria o token
	// valer para um provedor que o IdP nao pretendia.
	for _, papel := range c.RealmAccess.Roles {
		if papel == "" || papel == FuncaoInterna || papel == c.Azp {
			continue
		}
		return app.Ator{}, fmt.Errorf(
			"%w: token de %s com papel %q", ErrProvedorAmbiguo, c.Azp, papel)
	}

	if c.Azp == FuncaoInterna {
		// Cliente de servico. Sem provedor, e por isso que abre carteira e
		// reconcilia, com os escopos que o realm lhe deu.
		return app.Ator{Cliente: c.Azp, Escopos: escopos}, nil
	}

	return app.Ator{Cliente: c.Azp, Provedor: c.Azp, Escopos: escopos}, nil
}
