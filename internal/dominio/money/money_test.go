package money

import (
	"errors"
	"testing"
)

// brl e a moeda usada na maioria dos casos do desafio. Existe como constante de
// teste para que o codigo "BRL" nao se repita em cada caso.
const brl = CurrencyBRL

func TestParseAceitaValorNormalComDuasCasas(t *testing.T) {
	m, err := Parse("25.00", brl)
	if err != nil {
		t.Fatalf("Parse(25.00): %v", err)
	}

	if m.Amount() != 2500 {
		t.Errorf("Amount e %d, esperado 2500 (unidade minima)", m.Amount())
	}
	if m.Currency() != brl {
		t.Errorf("Currency e %q, esperado %q", m.Currency(), brl)
	}
}

// O contrato externo usa string decimal, e nao numero. Um valor que chega como
// 25.00 em JSON ja passou por float em algum lugar, e a prova de que Money nao
// depende disso e que a string volta exata.
func TestParsePreservaCentavosDeValorComManyDigits(t *testing.T) {
	casos := []struct {
		entrada  string
		centavos int64
	}{
		{"0.00", 0},
		{"0.01", 1},
		{"0.99", 99},
		{"1.00", 100},
		{"19.99", 1999},
		{"1000.00", 100000},
		{"1234567.89", 123456789},
	}

	for _, c := range casos {
		m, err := Parse(c.entrada, brl)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.entrada, err)
			continue
		}
		if m.Amount() != c.centavos {
			t.Errorf("Parse(%q).Amount() e %d, esperado %d", c.entrada, m.Amount(), c.centavos)
		}
	}
}

func TestParseAceitaUmaCasaDecimal(t *testing.T) {
	m, err := Parse("25.5", brl)
	if err != nil {
		t.Fatalf("Parse(25.5): %v", err)
	}

	// Uma casa decimal e uma entrada valida: 25.5 reais sao 2550 centavos. O que
	// nao pode ser aceito e mais de duas casas, porque ai o valor silenciosamente
	// perderia precisao.
	if m.Amount() != 2550 {
		t.Errorf("Amount e %d, esperado 2550", m.Amount())
	}
}

func TestParseAceitaValorNegativo(t *testing.T) {
	// Money aceita negativo porque o dominio precisa de diferenca e de calculo
	// intermediario. A regra de "entrada externa nao negativa" vive na borda, e
	// nao aqui: quem decide o que e entrada externa e o caso de uso.
	m, err := Parse("-25.00", brl)
	if err != nil {
		t.Fatalf("Parse(-25.00): %v", err)
	}

	if m.Amount() != -2500 {
		t.Errorf("Amount e %d, esperado -2500", m.Amount())
	}
}

func TestParseRejeitaFormatoInvalido(t *testing.T) {
	casos := []struct {
		nome    string
		entrada string
	}{
		{"vazio", ""},
		{"espaco", " "},
		{"apenas ponto", "."},
		{"apenas casas", ".50"},
		{"ponto sem casas", "25."},
		{"virgula decimal", "25,00"},
		{"separador de milhar com virgula", "1.000,00"},
		{"notacao cientifica", "1e2"},
		{"notacao cientifica com sinal", "2.5e3"},
		{"NaN", "NaN"},
		{"Infinity", "Infinity"},
		{"-Infinity", "-Infinity"},
		{"hexadecimal", "0x19"},
		{"espaco no fim", "25.00 "},
		{"espaco no comeco", " 25.00"},
		{"sinal no fim", "25.00-"},
		{"sinal duplo", "--25.00"},
		{"sinal mais e menos", "+-25.00"},
		{"letras", "abc"},
		{"apenas letras", "BRL"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := Parse(c.entrada, brl)
			if err == nil {
				t.Fatalf("Parse(%q) aceitou entrada invalida", c.entrada)
			}
			if !errors.Is(err, ErrFormatoInvalido) {
				t.Errorf("Parse(%q) devolveu %v, esperado ErrFormatoInvalido", c.entrada, err)
			}
		})
	}
}

// Escala excedente e o caso que o enunciado proibe que passe silencioso.
// "25.999" nao pode virar 26.00 nem 25999: os dois mudam dinheiro.
func TestParseRejeitaEscalaExcedente(t *testing.T) {
	// "1.000" entra aqui e nao na lista de formato invalido por um motivo que
	// importa: com ponto como separador decimal, "1.000" sao tres casas, nao mil.
	// Adivinhar que o autor quis dizer "1000" seria aceitar, em silencio, uma
	// entrada com precisao que o nao enviou.
	casos := []string{"25.999", "0.001", "1.000", "1.23456789", "-25.999"}

	for _, entrada := range casos {
		t.Run(entrada, func(t *testing.T) {
			_, err := Parse(entrada, brl)
			if err == nil {
				t.Fatalf("Parse(%q) aceitou escala excedente", entrada)
			}
			if !errors.Is(err, ErrEscalaExcedida) {
				t.Errorf("Parse(%q) devolveu %v, esperado ErrEscalaExcedida", entrada, err)
			}
		})
	}
}

func TestParseRejeitaMoedaDesconhecida(t *testing.T) {
	casos := []struct {
		entrada string
		moeda   Currency
	}{
		{"25.00", ""},
		{"25.00", "brl"},
		{"25.00", "BRL "},
		{"25.00", "BRLL"},
		{"25.00", "XX"},
		{"25.00", "BRL,BRL"},
	}

	for _, c := range casos {
		t.Run(string(c.moeda), func(t *testing.T) {
			_, err := Parse(c.entrada, c.moeda)
			if err == nil {
				t.Fatalf("Parse(%q, %q) aceitou moeda invalida", c.entrada, c.moeda)
			}
			if !errors.Is(err, ErrMoedaInvalida) {
				t.Errorf("Parse(%q, %q) devolveu %v, esperado ErrMoedaInvalida", c.entrada, c.moeda, err)
			}
		})
	}
}

func TestParseRejeitaValorZeroNaoInicializado(t *testing.T) {
	// O valor zero de Money nao e um Money: tem moeda vazia, que nao existe. Se
	// o valor zero fosse aceito, uma carteira recem-criada sem moeda passaria a
	// operar em uma moeda indefinida.
	var m Money
	if m.Valida() {
		t.Fatal("Money nao inicializado se diz valido")
	}
	if !errors.Is(m.Validar(), ErrInvalido) {
		t.Errorf("Validar devolveu %v, esperado ErrInvalido", m.Validar())
	}
}

func TestZeroPorMoeda(t *testing.T) {
	m := Zero(CurrencyUSD)

	if m.Amount() != 0 {
		t.Errorf("Zero(USD).Amount() e %d, esperado 0", m.Amount())
	}
	if m.Currency() != CurrencyUSD {
		t.Errorf("Zero(USD).Currency() e %q, esperado %q", m.Currency(), CurrencyUSD)
	}
	if !m.IsZero() {
		t.Error("Zero(USD) nao se diz zero")
	}
}

// A serializacao e o contrato: {"amount":"25.00","currency":"BRL"}. Uma
// string que volta com tres casas, ou com notacao cientifica, quebra o
// contrato do lado do cliente.
func TestSerializaComDuasCasas(t *testing.T) {
	casos := []struct {
		entrada string
		saida   string
	}{
		{"0.00", "0.00"},
		{"0.01", "0.01"},
		{"1.00", "1.00"},
		{"25.00", "25.00"},
		{"25.5", "25.50"},
		{"0.1", "0.10"},
		{"1234.56", "1234.56"},
		{"-25.00", "-25.00"},
		{"-0.01", "-0.01"},
	}

	for _, c := range casos {
		t.Run(c.entrada, func(t *testing.T) {
			m, err := Parse(c.entrada, brl)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.entrada, err)
			}
			if got := m.Decimal(); got != c.saida {
				t.Errorf("Parse(%q).Decimal() e %q, esperado %q", c.entrada, got, c.saida)
			}
		})
	}
}

func TestStringIncluiValorEMoeda(t *testing.T) {
	m, err := Parse("25.00", brl)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// String e para log e para mensagem de erro. Um log que mostra 25.00 sem
	// moeda e um log que permite ler "25.00" como dollars.
	if got := m.String(); got != "25.00 BRL" {
		t.Errorf("String() e %q, esperado %q", got, "25.00 BRL")
	}
}

func TestMoedaValidaAceitaCodigoConhecido(t *testing.T) {
	// O catalogo e fechado de proposito. Aceitar qualquer codigo ISO 4217
	// exigiria uma tabela, e tabela errada e pior que recusa: o dado entra
	// valido e sai com significado diferente do que o cliente pediu.
	conhecidas := []Currency{CurrencyBRL, CurrencyUSD, CurrencyEUR}

	for _, c := range conhecidas {
		if !c.Valida() {
			t.Errorf("moeda %q do catalogo se diz invalida", c)
		}
	}

	if Currency("XXX").Valida() {
		t.Error("moeda fora do catalogo se diz valida")
	}
}
