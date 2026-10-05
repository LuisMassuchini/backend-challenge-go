package wallet

import (
	"errors"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
)

// Lancamento e o registro do ledger. Este arquivo verifica a unica coisa que o
// enunciado exige dele alem de imutabilidade: balanceAfter = balanceBefore +- money,
// conforme a direcao.

// idLancamento e um identificador estavel para o teste nao depender de geracao.
const idLancamento = "0192f299-1111-7e38-af88-e43f851a819d"

func TestLancamentoValidaCredito(t *testing.T) {
	l, err := NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		DirecaoCredito,
		deveParse(t, "25.00"),
		deveParse(t, "100.00"),
		deveParse(t, "125.00"),
		instanteFixo(),
	)
	if err != nil {
		t.Fatalf("NovoLancamento: %v", err)
	}

	if l.Direcao() != DirecaoCredito {
		t.Errorf("Direcao e %q, esperado %q", l.Direcao(), DirecaoCredito)
	}
	if l.Valor().Decimal() != "25.00" {
		t.Errorf("Valor e %q, esperado %q", l.Valor().Decimal(), "25.00")
	}
	if l.Carteira().String() != carteiraA {
		t.Errorf("Carteira e %q, esperado %q", l.Carteira(), carteiraA)
	}
	if l.Transacao().String() != transacaoA {
		t.Errorf("Transacao e %q, esperado %q", l.Transacao(), transacaoA)
	}
	if !l.CriadoEm().Equal(instanteFixo()) {
		t.Errorf("CriadoEm e %v", l.CriadoEm())
	}
}

func TestLancamentoValidaDebito(t *testing.T) {
	// Debito e o caso que a constraint do banco vai reforcar com CHECK, e o
	// dominio precisa rejeitar antes, porque o erro de dominio distingue "saldo
	// insuficiente" de "lancamento invalido", e o cliente precisa ver o primeiro.
	l, err := NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		DirecaoDebito,
		deveParse(t, "80.00"),
		deveParse(t, "100.00"),
		deveParse(t, "20.00"),
		instanteFixo(),
	)
	if err != nil {
		t.Fatalf("NovoLancamento: %v", err)
	}

	if l.Direcao() != DirecaoDebito {
		t.Errorf("Direcao e %q, esperado %q", l.Direcao(), DirecaoDebito)
	}
}

// O enunciado pede que a construcao valide balanceAfter = balanceBefore +- money.
// Sem essa validacao, um lancamento com saldo posterior errado passa e o
// reconciliacao so sinaliza a divergencia depois que o dinheiro ja foi movimentado.
func TestLancamentoRecusaSaldoPosteriorIncompativel(t *testing.T) {
	casos := []struct {
		nome    string
		direcao Direcao
		valor   string
		antes   string
		depois  string
	}{
		{"credito com posterior menor", DirecaoCredito, "25.00", "100.00", "75.00"},
		{"credito com posterior a mais", DirecaoCredito, "25.00", "100.00", "125.01"},
		{"debito com posterior maior", DirecaoDebito, "25.00", "100.00", "125.00"},
		{"debito com posterior a menos", DirecaoDebito, "25.00", "100.00", "74.99"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := NovoLancamento(
				deveID(t, idLancamento),
				deveID(t, carteiraA),
				deveID(t, transacaoA),
				c.direcao,
				deveParse(t, c.valor),
				deveParse(t, c.antes),
				deveParse(t, c.depois),
				instanteFixo(),
			)
			if !errors.Is(err, ErrSaldoPosteriorIncompativel) {
				t.Errorf("devolveu %v, esperado ErrSaldoPosteriorIncompativel", err)
			}
		})
	}
}

// Lancamento de valor zero nao pode existir. Ele nao muda o saldo, mas entraria
// na soma do ledger, na contagem de lançamentos da reconciliacao e em qualquer
// relatorio de movimentacao, como se fosse uma operacao financeira. LOSS e uma
// operacao sem efeito: ela existe sem lancamento.
func TestLancamentoRecusaValorZero(t *testing.T) {
	for _, direcao := range []Direcao{DirecaoCredito, DirecaoDebito} {
		t.Run(string(direcao), func(t *testing.T) {
			_, err := NovoLancamento(
				deveID(t, idLancamento),
				deveID(t, carteiraA),
				deveID(t, transacaoA),
				direcao,
				deveParse(t, "0.00"),
				deveParse(t, "100.00"),
				deveParse(t, "100.00"),
				instanteFixo(),
			)
			if !errors.Is(err, ErrValorInvalido) {
				t.Errorf("devolveu %v, esperado ErrValorInvalido", err)
			}
		})
	}
}

func TestLancamentoRecusaValorNaoInicializado(t *testing.T) {
	var naoInicializado money.Money

	_, err := NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		DirecaoCredito,
		naoInicializado,
		deveParse(t, "0.00"),
		deveParse(t, "0.00"),
		instanteFixo(),
	)
	if err == nil {
		t.Fatal("NovoLancamento aceitou valor nao inicializado")
	}
}

func TestLancamentoRecusaSaldoPosteriorNegativo(t *testing.T) {
	// Um lancamento de debito que termina em saldo negativo nao pode existir: o
	// sistema inteiro proibe saldo negativo, e o ledger e a prova de que isso e
	// verdade.
	_, err := NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		DirecaoDebito,
		deveParse(t, "25.00"),
		deveParse(t, "10.00"),
		deveParse(t, "-15.00"),
		instanteFixo(),
	)
	if !errors.Is(err, ErrSaldoPosteriorIncompativel) {
		t.Errorf("devolveu %v, esperado ErrSaldoPosteriorIncompativel", err)
	}
}

func TestLancamentoRecusaMoedaIncompativel(t *testing.T) {
	dolar, err := money.Parse("25.00", money.CurrencyUSD)
	if err != nil {
		t.Fatalf("Parse em USD: %v", err)
	}

	_, err = NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		DirecaoCredito,
		deveParse(t, "25.00"),
		deveParse(t, "100.00"),
		deveParse(t, "125.00"),
		instanteFixo(),
	)
	if err != nil {
		t.Fatalf("caso base: %v", err)
	}

	_, err = NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		DirecaoCredito,
		dolar,
		deveParse(t, "100.00"),
		deveParse(t, "100.00"),
		instanteFixo(),
	)
	if !errors.Is(err, ErrMoedaDoLancamento) {
		t.Errorf("devolveu %v, esperado ErrMoedaDoLancamento", err)
	}
}

// O par (walletId, transactionId) tem unicidade no banco. No dominio, o
// lancamento nao sabe se ja existe outro: ele nao tem acesso ao repositorio, e
// nao deve ter. O que o dominio garante e que cada lancamento carrega os tres
// identificadores que a unicidade vai conferir.
func TestLancamentoCarregaOsIdentificadoresDaUnicidade(t *testing.T) {
	l, err := NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		DirecaoDebito,
		deveParse(t, "25.00"),
		deveParse(t, "100.00"),
		deveParse(t, "75.00"),
		instanteFixo(),
	)
	if err != nil {
		t.Fatalf("NovoLancamento: %v", err)
	}

	if !l.ID().Valida() {
		t.Error("lancamento sem id")
	}
	if !l.Carteira().Valida() {
		t.Error("lancamento sem carteira")
	}
	if !l.Transacao().Valida() {
		t.Error("lancamento sem transacao")
	}
}

func TestLancamentoRecusaIdentificadorInvalido(t *testing.T) {
	casos := map[string]struct {
		id, carteira, transacao Identificador
	}{
		"id vazio":        {Identificador{}, deveID(t, carteiraA), deveID(t, transacaoA)},
		"carteira vazia":  {deveID(t, idLancamento), Identificador{}, deveID(t, transacaoA)},
		"transacao vazia": {deveID(t, idLancamento), deveID(t, carteiraA), Identificador{}},
	}

	for nome, c := range casos {
		t.Run(nome, func(t *testing.T) {
			_, err := NovoLancamento(
				c.id, c.carteira, c.transacao,
				DirecaoCredito,
				deveParse(t, "25.00"),
				deveParse(t, "100.00"),
				deveParse(t, "125.00"),
				instanteFixo(),
			)
			if err == nil {
				t.Fatal("NovoLancamento aceitou identificador invalido")
			}
		})
	}
}

// A carteira e que produz o lancamento, e ele tem de refletir exatamente o que a
// carteira fez: mesmo valor, mesma direcao e os saldos antes e depois do
// movimento. Um lancamento montado a parte, com os mesmos numeros e outra
// relacao, e o que quebra a reconciliacao.
func TestCarteiraProduzLancamentoCompativelComOQueMovimentou(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	antes := c.Saldo()
	valor := deveParse(t, "80.00")

	apos, lancamento, err := c.Debitar(deveID(t, transacaoA), valor, instanteFixo())
	if err != nil {
		t.Fatalf("Debitar: %v", err)
	}

	if lancamento.Direcao() != DirecaoDebito {
		t.Errorf("Direcao e %q, esperado %q", lancamento.Direcao(), DirecaoDebito)
	}
	if !lancamento.Valor().Equal(valor) {
		t.Errorf("Valor e %s, esperado %s", lancamento.Valor(), valor)
	}
	if !lancamento.SaldoAnterior().Equal(antes) {
		t.Errorf("SaldoAnterior e %s, esperado %s", lancamento.SaldoAnterior(), antes)
	}
	if !lancamento.SaldoPosterior().Equal(apos.Saldo()) {
		t.Errorf("SaldoPosterior e %s, esperado %s", lancamento.SaldoPosterior(), apos.Saldo())
	}
	if lancamento.Transacao().String() != transacaoA {
		t.Errorf("Transacao e %q, esperado %q", lancamento.Transacao(), transacaoA)
	}
}

func TestCarteiraProduzLancamentoDeCredito(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	_, lancamento, err := c.Creditar(deveID(t, transacaoA), deveParse(t, "25.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Creditar: %v", err)
	}

	if lancamento.Direcao() != DirecaoCredito {
		t.Errorf("Direcao e %q, esperado %q", lancamento.Direcao(), DirecaoCredito)
	}
	if !lancamento.SaldoPosterior().Equal(deveParse(t, "125.00")) {
		t.Errorf("SaldoPosterior e %s, esperado 125.00", lancamento.SaldoPosterior())
	}
}

// Operacao recusada nao pode produzir lancamento. E o que garante que o ledger
// nao ganha linha para uma aposta que foi rejeitada por saldo insuficiente.
func TestOperacaoRecusadaNaoProduzLancamento(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	// A primeira aposta de 80.00 cabe; a segunda nao, porque o saldo e 20.00.
	c, _, err = c.Debitar(deveID(t, transacaoA), deveParse(t, "80.00"), instanteFixo())
	if err != nil {
		t.Fatalf("primeiro debito: %v", err)
	}

	_, lancamento, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "80.00"), instanteFixo())
	if !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("devolveu %v, esperado ErrSaldoInsuficiente", err)
	}
	if lancamento.Valida() {
		t.Error("operacao recusada produziu lancamento")
	}
}

func TestDirecaoInvalidaRecusada(t *testing.T) {
	_, err := NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		Direcao(""),
		deveParse(t, "25.00"),
		deveParse(t, "100.00"),
		deveParse(t, "125.00"),
		instanteFixo(),
	)
	if !errors.Is(err, ErrDirecaoInvalida) {
		t.Errorf("devolveu %v, esperado ErrDirecaoInvalida", err)
	}

	_, err = NovoLancamento(
		deveID(t, idLancamento),
		deveID(t, carteiraA),
		deveID(t, transacaoA),
		Direcao("TRANSFER"),
		deveParse(t, "25.00"),
		deveParse(t, "100.00"),
		deveParse(t, "125.00"),
		instanteFixo(),
	)
	if !errors.Is(err, ErrDirecaoInvalida) {
		t.Errorf("devolveu %v, esperado ErrDirecaoInvalida", err)
	}
}
