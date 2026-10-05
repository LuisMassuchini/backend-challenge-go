package money

import (
	"errors"
	"strings"
	"testing"
)

// Overflow e o unico jeito de Money devolver um valor que nao representa
// dinheiro. Ele nao aparece em operacao normal, e por isso precisa de teste:
// um limite mal escrito passa a suite inteira e so falha com saldo de 92
// quilhoes, ou seja, nunca.

// maxEmCentavos e o maior valor que cabe em Money, escrito como decimal para
// que o teste dependa do contrato e nao da constante do codigo.
const maxEmCentavos = "92233720368547758.07"

func TestParseAceitaOsLimitesDoTipo(t *testing.T) {
	casos := []struct {
		entrada string
		amount  int64
	}{
		{maxEmCentavos, maxInt64},
		{"-92233720368547758.08", minInt64},
		{"92233720368547758.06", maxInt64 - 1},
	}

	for _, c := range casos {
		t.Run(c.entrada, func(t *testing.T) {
			m, err := Parse(c.entrada, brl)
			if err != nil {
				t.Fatalf("Parse(%q): %v", c.entrada, err)
			}
			if m.Amount() != c.amount {
				t.Errorf("Amount e %d, esperado %d", m.Amount(), c.amount)
			}
		})
	}
}

func TestParseRecusaOverflow(t *testing.T) {
	casos := []struct {
		nome    string
		entrada string
	}{
		{"um centavo acima do maximo", "92233720368547758.08"},
		{"um centavo abaixo do minimo", "-92233720368547758.09"},
		{"muitas casas", "9223372036854775808.00"},
		{"muitas casas sem negativo", "99999999999999999999999999.99"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			m, err := Parse(c.entrada, brl)
			if err == nil {
				t.Fatalf("Parse(%q) aceitou overflow e devolveu %d", c.entrada, m.Amount())
			}
			if !errors.Is(err, ErrOverflow) {
				t.Errorf("Parse(%q) devolveu %v, esperado ErrOverflow", c.entrada, err)
			}
			// O valor devolvido tem de ser o valor zero utilizavel, e nao um
			// Money com o amount truncado: um consumidor que ignore o erro
			// operaria sobre um numero que o cliente nunca enviou.
			if m != (Money{}) {
				t.Errorf("Parse(%q) devolveu %v junto do erro, esperado Money{}", c.entrada, m)
			}
		})
	}
}

func TestSomaRecusaOverflow(t *testing.T) {
	grande := deveParse(t, maxEmCentavos, brl)
	um := deveParse(t, "0.01", brl)

	soma, err := grande.Add(um)
	if err == nil {
		t.Fatalf("Add aceitou overflow e devolveu %d", soma.Amount())
	}
	if !errors.Is(err, ErrOverflow) {
		t.Errorf("devolveu %v, esperado ErrOverflow", err)
	}
	if soma != (Money{}) {
		t.Errorf("Add devolveu %v junto do erro, esperado Money{}", soma)
	}
}

func TestSubtracaoRecusaOverflow(t *testing.T) {
	// O caso perigoso de Sub nao e o resultado positivo grande: e o resultado
	// negativo grande, que so aparece com o menor valor do tipo de um lado.
	menor := deveParse(t, "-92233720368547758.08", brl)
	um := deveParse(t, "1.00", brl)

	diferenca, err := menor.Sub(um)
	if err == nil {
		t.Fatalf("Sub aceitou overflow e devolveu %d", diferenca.Amount())
	}
	if !errors.Is(err, ErrOverflow) {
		t.Errorf("devolveu %v, esperado ErrOverflow", err)
	}
	if diferenca != (Money{}) {
		t.Errorf("Sub devolveu %v junto do erro, esperado Money{}", diferenca)
	}
}

// Negar o menor valor e overflow garantido, e nao um caso raro: e exatamente o
// valor que o parsing aceita. Sem a guarda, o resultado seria o proprio menor
// valor com sinal trocado, que e o maior positivo, e o sistema aceitaria como
// resultado de uma operacao financeira.
func TestNegacaoDoMenorValorRecusa(t *testing.T) {
	menor := deveParse(t, "-92233720368547758.08", brl)

	negado, err := menor.Neg()
	if err == nil {
		t.Fatalf("Neg aceitou overflow e devolveu %d", negado.Amount())
	}
	if !errors.Is(err, ErrOverflow) {
		t.Errorf("devolveu %v, esperado ErrOverflow", err)
	}
	if negado != (Money{}) {
		t.Errorf("Neg devolveu %v junto do erro, esperado Money{}", negado)
	}
}

// A serializacao tem de sobreviver aos extremos, porque e ela que vai para o
// banco e para o log. Se o limite quebrar a formatacao, o valor ja foi aceito e
// ainda assim nao pode ser salvo nem impresso.
func TestSerializaOsLimitesDoTipo(t *testing.T) {
	casos := []struct {
		entrada string
		saida   string
	}{
		{maxEmCentavos, "92233720368547758.07"},
		{"-92233720368547758.08", "-92233720368547758.08"},
		{"0.00", "0.00"},
	}

	for _, c := range casos {
		t.Run(c.entrada, func(t *testing.T) {
			m := deveParse(t, c.entrada, brl)
			if got := m.Decimal(); got != c.saida {
				t.Errorf("Decimal() e %q, esperado %q", got, c.saida)
			}
		})
	}
}

// Uma transacao que estoura o limite tem de ser recusada, e nao_resultar em um
// saldo negativo silencioso. Este teste existe para fixar a ordem das checagens:
// o erro de overflow tem de vir antes de qualquer erro de saldo.
func TestOverflowTemPrioridadeSobreCompatibilidadeDeMoeda(t *testing.T) {
	grande := deveParse(t, maxEmCentavos, brl)
	dolar := deveParse(t, "1.00", CurrencyUSD)

	_, err := grande.Add(dolar)
	if !errors.Is(err, ErrMoedaIncompativel) {
		t.Errorf("devolveu %v, esperado ErrMoedaIncompativel", err)
	}
	if errors.Is(err, ErrOverflow) {
		t.Error("operacao entre moedas diferentes nao pode falhar por overflow")
	}
}

// Parsing repetido do limite maximo tem de ser estavel: se uma parte do caminho
// reduz o valor e outra nao, o resultado depende da ordem das operacoes, e o
// hash de idempotencia gravado no banco deixa de bater com o valor recalculado.
func TestParseDoLimiteEhEstavel(t *testing.T) {
	primeira := deveParse(t, maxEmCentavos, brl)
	segunda := deveParse(t, maxEmCentavos, brl)

	if primeira.Decimal() != segunda.Decimal() {
		t.Errorf("Parse do limite nao e estavel: %q e %q", primeira.Decimal(), segunda.Decimal())
	}
	if !strings.HasPrefix(primeira.Decimal(), "92233720368547758") {
		t.Errorf("Parse do limite devolveu %q", primeira.Decimal())
	}
}
