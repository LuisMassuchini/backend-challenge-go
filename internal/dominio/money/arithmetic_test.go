package money

import (
	"errors"
	"testing"
)

// deveParse e um atalho de teste que falha o teste inteiro quando o valor base
// nao e valido. Sem ele, um erro no parsing de "25.00" aparece como falha de
// aritmetica, e o sintome aponta para o lugar errado.
func deveParse(t *testing.T, texto string, moeda Currency) Money {
	t.Helper()
	m, err := Parse(texto, moeda)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", texto, moeda, err)
	}
	return m
}

func TestSomaValoresDaMesmaMoeda(t *testing.T) {
	a := deveParse(t, "25.00", brl)
	b := deveParse(t, "0.75", brl)

	soma, err := a.Add(b)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if soma.Amount() != 2575 {
		t.Errorf("Amount e %d, esperado 2575", soma.Amount())
	}
	if soma.Currency() != brl {
		t.Errorf("Currency e %q, esperado %q", soma.Currency(), brl)
	}
	if soma.Decimal() != "25.75" {
		t.Errorf("Decimal e %q, esperado %q", soma.Decimal(), "25.75")
	}
}

// Soma com zero e a operacao mais frequente do sistema: credito de WIN sobre
// carteira vazia, e acrescimo de valor negativo em operacao de ajuste.
func TestSomaComZeroEmCadaMoeda(t *testing.T) {
	a := deveParse(t, "25.00", brl)

	soma, err := a.Add(Zero(brl))
	if err != nil {
		t.Fatalf("Add(Zero): %v", err)
	}
	if !soma.Equal(a) {
		t.Errorf("25.00 + 0.00 deu %s, esperado %s", soma, a)
	}

	zeroMaisZero, err := Zero(brl).Add(Zero(brl))
	if err != nil {
		t.Fatalf("Zero.Add(Zero): %v", err)
	}
	if !zeroMaisZero.IsZero() {
		t.Errorf("Zero + Zero deu %s, esperado zero", zeroMaisZero)
	}
	if zeroMaisZero.Currency() != brl {
		t.Errorf("Currency e %q, esperado %q", zeroMaisZero.Currency(), brl)
	}
}

func TestSomaAceitaOperandoNegativo(t *testing.T) {
	// O dominio precisa de diferenca, e a diferenca e Money negativa somada.
	// E por isso que Money aceita negativo no parsing.
	a := deveParse(t, "10.00", brl)
	b := deveParse(t, "-2.50", brl)

	soma, err := a.Add(b)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if soma.Decimal() != "7.50" {
		t.Errorf("Decimal e %q, esperado %q", soma.Decimal(), "7.50")
	}
}

func TestSubtracao(t *testing.T) {
	a := deveParse(t, "25.00", brl)
	b := deveParse(t, "0.75", brl)

	diferenca, err := a.Sub(b)
	if err != nil {
		t.Fatalf("Sub: %v", err)
	}

	if diferenca.Decimal() != "24.25" {
		t.Errorf("Decimal e %q, esperado %q", diferenca.Decimal(), "24.25")
	}
}

func TestSubtracaoResultaEmNegativo(t *testing.T) {
	// Subtracao que passa de zero e legitima no dominio: e assim que se calcula
	// o saldo projetado antes de decidir se o debito cabe. O saldo negativo e
	// proibido na carteira, e nao no valor.
	a := deveParse(t, "10.00", brl)
	b := deveParse(t, "25.00", brl)

	diferenca, err := a.Sub(b)
	if err != nil {
		t.Fatalf("Sub: %v", err)
	}

	if diferenca.Amount() != -1500 {
		t.Errorf("Amount e %d, esperado -1500", diferenca.Amount())
	}
	if !diferenca.IsNegative() {
		t.Error("10.00 - 25.00 nao se diz negativo")
	}
}

func TestNegacao(t *testing.T) {
	casos := []struct {
		entrada string
		saida   string
	}{
		{"0.00", "0.00"},
		{"25.00", "-25.00"},
		{"-25.00", "25.00"},
		{"0.01", "-0.01"},
	}

	for _, c := range casos {
		t.Run(c.entrada, func(t *testing.T) {
			m := deveParse(t, c.entrada, brl)

			negado, err := m.Neg()
			if err != nil {
				t.Fatalf("Neg: %v", err)
			}
			if negado.Decimal() != c.saida {
				t.Errorf("Neg de %q deu %q, esperado %q", c.entrada, negado.Decimal(), c.saida)
			}
			if negado.Currency() != m.Currency() {
				t.Errorf("Neg perdeu a moeda: %q", negado.Currency())
			}
		})
	}
}

func TestComparacao(t *testing.T) {
	menor := deveParse(t, "10.00", brl)
	igual := deveParse(t, "10.00", brl)
	maior := deveParse(t, "10.01", brl)

	casos := []struct {
		nome     string
		a, b     Money
		esperado int
	}{
		{"menor que maior", menor, maior, -1},
		{"maior que menor", maior, menor, 1},
		{"igual", menor, igual, 0},
		{"negativo menor que positivo", deveParse(t, "-10.00", brl), menor, -1},
		{"positivo maior que negativo", maior, deveParse(t, "-10.00", brl), 1},
		{"zero menor que positivo", Zero(brl), menor, -1},
		{"negativo menor que zero", deveParse(t, "-0.01", brl), Zero(brl), -1},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, err := c.a.Compare(c.b)
			if err != nil {
				t.Fatalf("Compare: %v", err)
			}
			if got != c.esperado {
				t.Errorf("Compare(%s, %s) = %d, esperado %d", c.a, c.b, got, c.esperado)
			}
		})
	}
}

func TestIgualdadeEZero(t *testing.T) {
	a := deveParse(t, "25.00", brl)
	b := deveParse(t, "25.00", brl)
	c := deveParse(t, "25.01", brl)

	if !a.Equal(b) {
		t.Error("25.00 deveria ser igual a 25.00")
	}
	if a.Equal(c) {
		t.Error("25.00 nao deveria ser igual a 25.01")
	}
	if a.IsZero() {
		t.Error("25.00 nao se diz zero")
	}
	if !Zero(brl).IsZero() {
		t.Error("Zero(BRL) nao se diz zero")
	}
	if !deveParse(t, "-0.00", brl).IsZero() {
		t.Error("-0.00 se diz zero e nao e")
	}
	if deveParse(t, "0.00", brl).IsPositive() {
		t.Error("0.00 nao pode se dizer positivo")
	}
	if deveParse(t, "0.00", brl).IsNegative() {
		t.Error("0.00 nao pode se dizer negativo")
	}
	if deveParse(t, "-0.01", brl).IsPositive() {
		t.Error("-0.01 se diz positivo")
	}
}

// igualar moedas diferentes e o erro que mais aparece em bug real de integracao
// financeira: o valor sai certo e a moeda errada, e o total do relatorio nao
// bate. Nenhuma aritmetica entre moedas distintas e definida.
func TestOperacoesEntreMoedasDiferentesRecusam(t *testing.T) {
	real := deveParse(t, "25.00", CurrencyBRL)
	dolar := deveParse(t, "25.00", CurrencyUSD)

	operacoes := map[string]func() error{
		"Add":     func() error { _, err := real.Add(dolar); return err },
		"Sub":     func() error { _, err := real.Sub(dolar); return err },
		"Compare": func() error { _, err := real.Compare(dolar); return err },
		"Equal":   func() error { return real.ValidarMoeda(dolar) },
	}

	for nome, op := range operacoes {
		t.Run(nome, func(t *testing.T) {
			err := op()
			if err == nil {
				t.Fatal("operacao entre moedas diferentes foi aceita")
			}
			if !errors.Is(err, ErrMoedaIncompativel) {
				t.Errorf("devolveu %v, esperado ErrMoedaIncompativel", err)
			}
		})
	}
}

// Uma aritmetica que aceita o valor zero de Money produziria um resultado sem
// moeda, e o erro apareceria muito depois, na serializacao.
func TestOperacoesComValorNaoInicializadoRecusam(t *testing.T) {
	var naoInicializado Money
	valido := deveParse(t, "25.00", brl)

	operacoes := map[string]func() error{
		"Add":     func() error { _, err := valido.Add(naoInicializado); return err },
		"Sub":     func() error { _, err := valido.Sub(naoInicializado); return err },
		"Compare": func() error { _, err := valido.Compare(naoInicializado); return err },
	}

	for nome, op := range operacoes {
		t.Run(nome, func(t *testing.T) {
			err := op()
			if err == nil {
				t.Fatal("operacao com valor nao inicializado foi aceita")
			}
			if !errors.Is(err, ErrInvalido) {
				t.Errorf("devolveu %v, esperado ErrInvalido", err)
			}
		})
	}
}

// Equal nao devolve erro, e ainda assim precisa expor a checagem: para o
// chamador, comparar valores de moedas diferentes e erro de programacao e nao
// "sao diferentes". Por isso a checagem e explicita e publica em ValidarMoeda.
func TestEqualNaoConfundeMoedaDiferenteComValorDiferente(t *testing.T) {
	real := deveParse(t, "25.00", CurrencyBRL)
	dolar := deveParse(t, "25.00", CurrencyUSD)

	if err := real.ValidarMoeda(dolar); !errors.Is(err, ErrMoedaIncompativel) {
		t.Errorf("ValidarMoeda devolveu %v, esperado ErrMoedaIncompativel", err)
	}

	// A dokumentacao precisa ser explicita sobre o que Equal faz em vez de
	// recusar: para o chamador, comparar valores de moedas diferentes e erro de
	// programacao, nao "sao diferentes".
	if real.Equal(dolar) {
		t.Error("Equal entre BRL e USD nao pode ser verdadeiro")
	}
}
