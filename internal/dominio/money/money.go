// Package money e a base financeira do sistema: um valor imutavel com valor e
// moeda.
//
// A decisao que sustenta tudo o que vem depois e a representacao. O valor e um
// int64 em unidade minima -- centavos -- com escala fixa de duas casas. Nao ha
// float32 nem float64 em nenhum caminho, nem no parsing, nem na aritmetica, nem
// na serializacao. Float nao serve para dinheiro: 0.1 e 0.2 nao somam 0.3 em
// ponto flutuante, e o enunciado trata isso como eliminatorio.
//
// O valor zero de Money nao e um Money valido. Ele tem moeda vazia, e moeda
// vazia nao existe. Isso e deliberado: uma carteira recem-criada que operasse
// sobre o valor zero passaria a mover dinheiro em uma moeda indefinida.
package money

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Currency e o codigo ISO 4217 da moeda.
//
// O catalogo e fechado de proposito. Uma tabela de centenas de codigos aceita
// mais, e aceitar mais e o modo rapido de o dado entrar valido e sair com
// significado diferente do que o cliente pediu. O desafio opera em BRL nos
// cenarios principais; USD e EUR existem para que os testes de incompatibilidade
// entre moedas tenham com o que trabalhar.
type Currency string

const (
	// CurrencyBRL e o real.
	CurrencyBRL Currency = "BRL"
	// CurrencyUSD e o dolar dos Estados Unidos.
	CurrencyUSD Currency = "USD"
	// CurrencyEUR e o euro.
	CurrencyEUR Currency = "EUR"
)

// Valida informa se a moeda pertence ao catalogo.
func (c Currency) Valida() bool {
	switch c {
	case CurrencyBRL, CurrencyUSD, CurrencyEUR:
		return true
	}
	return false
}

// Escala e o numero de casas decimais de todo valor monetario do sistema.
//
// Fixa em duas casas porque o contrato externo manda "25.00" e porque a
// persistencia e BIGINT em unidade minima. Uma escala por moeda permitiria JPY
// sem casas e USD com duas, e cada combinacao seria um caso especial no schema,
// no hash de idempotencia e no contrato JSON.
const Escala = 2

// Erros classificaveis do tipo.
//
// Sao sentinelas de proposito: o chamador decide com errors.Is o que fazer com
// entrada invalida -- recusar com 400, rejeitar com regra de negocio, ou tentar
// de novo -- e nao com o texto da mensagem, que muda com a traducao.
var (
	// ErrFormatoInvalido cobre tudo que nao e a forma decimal esperada.
	ErrFormatoInvalido = errors.New("money: formato decimal invalido")
	// ErrEscalaExcedida cobre mais de duas casas decimais.
	ErrEscalaExcedida = errors.New("money: escala decimal excedida")
	// ErrMoedaInvalida cobre codigo fora do catalogo.
	ErrMoedaInvalida = errors.New("money: moeda invalida")
	// ErrInvalido cobre o valor zero de Money, que nao tem moeda.
	ErrInvalido = errors.New("money: valor nao inicializado")
)

// Money e um valor monetario imutavel.
//
// Os campos sao privados de proposito: nada fora do pacote constroi um Money
// com amount arbitrario, e portanto nada consegue montar um valor que o
// contrato proibe. Toda mudanca passa por metodo que valida.
type Money struct {
	// amount e o valor em unidade minima da moeda. 2500 significa 25.00.
	amount int64
	// currency e o codigo ISO 4217 do valor.
	currency Currency
}

// Zero devolve o valor zero da moeda informada.
//
// Existe para que o chamador declare a moeda em vez de deduzi-la: um zero sem
// moeda e um valor invalido, e o compilador nao consegue impedir que ele exista.
func Zero(moeda Currency) Money {
	return Money{currency: moeda}
}

// Amount devolve o valor em unidade minima.
func (m Money) Amount() int64 { return m.amount }

// Currency devolve o codigo da moeda.
func (m Money) Currency() Currency { return m.currency }

// IsZero informa se o valor e zero.
func (m Money) IsZero() bool { return m.amount == 0 }

// Valida informa se o valor e um Money utilizavel.
//
// O valor zero nao e. A distincao importa na borda: um DTO sem o campo money
// chega como valor zero, e tratar isso como zero reais seria abrir uma
// carteira com saldo zero sem a-moeda, ou processo uma aposta sem valor.
func (m Money) Valida() bool { return m.currency.Valida() }

// Validar devolve o erro que explica por que o valor nao e utilizavel, ou nil.
func (m Money) Validar() error {
	if !m.currency.Valida() {
		return fmt.Errorf("%w: moeda %q fora do catalogo", ErrInvalido, m.currency)
	}
	return nil
}

// Parse le um valor monetario de string decimal.
//
// A gramática aceita e apenas esta forma:
//
//	["-"] digitos+"." digitos{1,2}
//
// Ou seja: sem espaco em volta, sem separador de milhar, sem virgula decimal, sem
// notacao cientifica, sem sinal de mais e com no maximo duas casas. Tudo que
// foge disso e recusado em vez de arredondado, porque um arredondamento silencioso
// muda dinheiro sem ninguem ver.
//
// A moeda vem separada e e validada contra o catalogo. Parse nao aceita "25.00
// BRL" como entrada: o valor e a moeda sao campos diferentes no contrato, e
// aceitar os dois juntos cria uma segunda forma de expressar a mesma coisa.
func Parse(texto string, moeda Currency) (Money, error) {
	if !moeda.Valida() {
		return Money{}, fmt.Errorf("%w: %q", ErrMoedaInvalida, moeda)
	}

	negativo := false
	corpo := texto
	if strings.HasPrefix(corpo, "-") {
		// Um segundo sinal, como em "--25.00", sobra no corpo e e reprovado pela
		// validacao de digitos abaixo.
		negativo = true
		corpo = corpo[1:]
	}

	inteiro, casas, temPonto := strings.Cut(corpo, ".")
	if !digitos(inteiro) || inteiro == "" {
		return Money{}, fmt.Errorf("%w: %q", ErrFormatoInvalido, texto)
	}

	centavos := int64(0)
	if temPonto {
		switch {
		case casas == "":
			// "25." nao informa a unidade minima, e assumir zero seria inventar
			// precisao que o cliente nao pediu.
			return Money{}, fmt.Errorf("%w: %q nao tem casas decimais", ErrFormatoInvalido, texto)
		case !digitos(casas):
			return Money{}, fmt.Errorf("%w: %q", ErrFormatoInvalido, texto)
		case len(casas) > Escala:
			return Money{}, fmt.Errorf("%w: %q tem %d casas", ErrEscalaExcedida, texto, len(casas))
		}

		valor, err := strconv.ParseInt(casas, 10, 64)
		if err != nil {
			return Money{}, fmt.Errorf("%w: %q", ErrFormatoInvalido, texto)
		}
		centavos = valor
		if len(casas) == 1 {
			centavos *= 10
		}
	}

	// Montar a unidade minima como texto e converter uma unica vez evita
	// multiplicar dentro do dominio sem nenhuma checagem de limite.
	unidadeMinima := inteiro
	if temPonto {
		// Nao usa Sprintf com %02d porque o preenchimento depende de locale em
		// alguns ambientes; dois digitos formatados a mao sao previsiveis.
		casas = casas + "0"[:Escala-len(casas)]
		unidadeMinima = inteiro + casas
	}

	amount, err := strconv.ParseInt(unidadeMinima, 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: %q", ErrFormatoInvalido, texto)
	}
	if negativo {
		amount = -amount
	}

	return Money{amount: amount, currency: moeda}, nil
}

// digitos informa se o texto nao esta vazio e tem apenas digitos ASCII.
//
// strconv.ParseInt aceitaria sinal e prefixo de base, e o objetivo aqui e o
// contrario: recusar tudo que nao e a forma exata esperada.
func digitos(texto string) bool {
	if texto == "" {
		return false
	}
	for _, c := range texto {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Decimal devolve o valor no formato decimal de duas casas, sem a moeda.
//
// E o formato do contrato externo: {"amount":"25.00","currency":"BRL"}.
func (m Money) Decimal() string {
	// O valor negativo e montado em unsigned de proposito: -MinInt64 nao cabe
	// em int64, e a negacao-ingestao em int64 seria o unico ponto do codigo
	// inteiro capaz de transbordar.
	negativo := m.amount < 0
	magnitude := magnitudeDe(m.amount)

	inteiro := magnitude / 100
	centavos := magnitude % 100

	var b strings.Builder
	if negativo {
		b.WriteByte('-')
	}
	b.WriteString(strconv.FormatUint(inteiro, 10))
	b.WriteByte('.')
	if centavos < 10 {
		b.WriteByte('0')
	}
	b.WriteString(strconv.FormatUint(centavos, 10))
	return b.String()
}

// String devolve valor e moeda, para log e mensagem de erro.
func (m Money) String() string {
	return m.Decimal() + " " + string(m.currency)
}

// magnitudeDe devolve o valor absoluto como unsigned, sem overflow.
func magnitudeDe(amount int64) uint64 {
	if amount >= 0 {
		return uint64(amount)
	}
	// -(amount+1) + 1 funciona para MinInt64, que nao tem oposto em int64.
	return uint64(-(amount + 1)) + 1
}
