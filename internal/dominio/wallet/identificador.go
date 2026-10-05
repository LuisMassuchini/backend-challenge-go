package wallet

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Identificador e um UUID versionado por identificador.
//
// Existe como tipo proprio por dois motivos que ja custaram bug em sistema
// financeiro: primeiro, string crua aceita qualquer coisa, e "transacao-123" ou
// um UUID com espaco no fim chegam ao banco e so falham na constraint; segundo,
// nao ha como dizer "id ausente" sem comparar com string vazia, e a comparacao
// com string vazia esquece o caso do UUID zerado, que e um valor valido que
// ninguem deveria usar.
//
// O sistema gera v7, que e ordenado por tempo. O motivo e o indice do banco:
// chave primaria de UUID v4 em tabela alta e cada escrita um ponto de
// contencao, e a carteira e a tabela com mais escrita do sistema.
type Identificador struct {
	id uuid.UUID
}

// Erros de identificador.
//
// O texto do erro carrega o valor recusado de proposito: em um sistema com
// varias instancias, o identificador errado aparece primeiro no log, e um erro
// sem o valor obrigaria a procurar o valor em outro lugar.
var (
	// ErrIdentificadorInvalido cobre texto que nao e UUID.
	ErrIdentificadorInvalido = errors.New("wallet: identificador invalido")
	// ErrIdentificadorNaoInicializado cobre o valor zero de Identificador.
	ErrIdentificadorNaoInicializado = errors.New("wallet: identificador nao inicializado")
)

// IdentificadorDe converte o texto de um UUID em identificador.
//
// Aceita o formato canonico com hifens, em caixa baixa ou alta, que e o que o
// PostgreSQL devolve e o que o contrato HTTP envia. As demais formas -- sem
// hifens, com espacos, com caractere invalido -- sao recusadas em vez de
// normalizadas, porque normalizar aqui esconderia um cliente que envia o
// identificador no formato errado.
func IdentificadorDe(texto string) (Identificador, error) {
	if texto == "" {
		return Identificador{}, fmt.Errorf("%w: texto vazio", ErrIdentificadorInvalido)
	}
	id, err := uuid.Parse(texto)
	if err != nil || id == uuid.Nil {
		return Identificador{}, fmt.Errorf("%w: %q", ErrIdentificadorInvalido, texto)
	}
	// uuid.Parse aceita o formato sem hifens e o formato com urn: prefixo. A
	// comparacao de volta com o texto canonico e o que garante que so o formato
	// canonico passa.
	if id.String() != canonical(texto) {
		return Identificador{}, fmt.Errorf("%w: %q nao esta no formato canonico", ErrIdentificadorInvalido, texto)
	}
	return Identificador{id: id}, nil
}

// canonical devolve o texto em caixa baixa, que e a forma canonica do UUID.
func canonical(texto string) string {
	minusculas := make([]byte, len(texto))
	for i := 0; i < len(texto); i++ {
		c := texto[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		minusculas[i] = c
	}
	return string(minusculas)
}

// NovoIdentificador gera um identificador v7.
//
// O gerador nao e do sistema: um UUID gerado no processo impede que o teste
// produza id repetido entre execucoes e que a comparacao de versao do lock
// pessimista dependa do relogio da maquina.
func NovoIdentificador() Identificador {
	return Identificador{id: uuid.Must(uuid.NewV7())}
}

// String devolve o texto canonico do identificador.
func (i Identificador) String() string { return i.id.String() }

// Valida informa se o identificador pode ser usado.
//
// O valor zero nao pode. Ele e um UUID valido -- o UUID zerado -- e por isso o
// teste de string vazia nao pegaria.
func (i Identificador) Valida() bool { return i.id != uuid.Nil }

// UUID devolve o valor bruto, para conversao em SQL e em log.
func (i Identificador) UUID() uuid.UUID { return i.id }
