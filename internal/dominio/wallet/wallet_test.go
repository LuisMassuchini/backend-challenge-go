package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
)

// Os identificadores de teste seguem o formato UUID v7, que e o que o sistema
// usa. O formato importa: um identificador invalido que passa pelo construtor
// acaba no banco, e o erro so aparece na constraint, longe da origem.
const (
	jogadorA   = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	carteiraA  = "0192f291-27dd-7d3f-8071-5f8685deef37"
	transacaoA = "0192f298-345e-7e38-af88-e43f851a819d"
)

func deveParse(t *testing.T, texto string) money.Money {
	t.Helper()
	m, err := money.Parse(texto, money.CurrencyBRL)
	if err != nil {
		t.Fatalf("money.Parse(%q): %v", texto, err)
	}
	return m
}

func deveID(t *testing.T, texto string) Identificador {
	t.Helper()
	id, err := IdentificadorDe(texto)
	if err != nil {
		t.Fatalf("IdentificadorDe(%q): %v", texto, err)
	}
	return id
}

// instanteFixo e um tempo deterministico. time.Now em teste de dominio produz
// valor diferente a cada execucao, e um teste que so falha as vezes nao e
// teste, e sorte.
func instanteFixo() time.Time {
	return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
}

func TestNovaCarteiraComSaldoInicialZero(t *testing.T) {
	agora := instanteFixo()

	c, err := Nova(deveID(t, jogadorA), money.Zero(money.CurrencyBRL), agora)
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	if !c.ID().Valida() {
		t.Error("carteira recem-criada sem id valido")
	}
	if c.Jogador().String() != jogadorA {
		t.Errorf("Jogador e %q, esperado %q", c.Jogador(), jogadorA)
	}
	if !c.Saldo().IsZero() {
		t.Errorf("Saldo e %s, esperado zero", c.Saldo())
	}
	if c.Saldo().Currency() != money.CurrencyBRL {
		t.Errorf("moeda do saldo e %q, esperado %q", c.Saldo().Currency(), money.CurrencyBRL)
	}
	// O enunciado e explicito: a versao inicial de uma carteira e 1.
	if c.Versao() != 1 {
		t.Errorf("Versao e %d, esperado 1", c.Versao())
	}
	if !c.CriadaEm().Equal(agora) {
		t.Errorf("CriadaEm e %v, esperado %v", c.CriadaEm(), agora)
	}
	if !c.AtualizadaEm().Equal(agora) {
		t.Errorf("AtualizadaEm e %v, esperado %v", c.AtualizadaEm(), agora)
	}
}

func TestNovaCarteiraComSaldoInicialPositivo(t *testing.T) {
	// O par (jogador, moeda) identifica a carteira, e a moeda vem do saldo
	// inicial. E o que impede duas carteiras do mesmo jogador em moedas
	// diferentes sem que a chave do banco precise ser composta.
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "1000.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	if c.Saldo().Decimal() != "1000.00" {
		t.Errorf("Saldo e %q, esperado %q", c.Saldo().Decimal(), "1000.00")
	}
	if c.Versao() != 1 {
		t.Errorf("Versao e %d, esperado 1", c.Versao())
	}
}

func TestNovaRecusaValorInvalido(t *testing.T) {
	casos := []struct {
		nome    string
		jogador Identificador
		saldo   money.Money
	}{
		{"saldo nao inicializado", deveID(t, jogadorA), money.Money{}},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if _, err := Nova(c.jogador, c.saldo, instanteFixo()); err == nil {
				t.Fatal("Nova aceitou valor invalido")
			}
		})
	}

	t.Run("jogador nao inicializado", func(t *testing.T) {
		if _, err := Nova(Identificador{}, money.Zero(money.CurrencyBRL), instanteFixo()); err == nil {
			t.Fatal("Nova aceitou jogador nao inicializado")
		}
	})

	t.Run("instante zero", func(t *testing.T) {
		if _, err := Nova(deveID(t, jogadorA), money.Zero(money.CurrencyBRL), time.Time{}); err == nil {
			t.Fatal("Nova aceitou instante nao inicializado")
		}
	})
}

// A reidratacao nao pode reaplicar movimentacao nem emitir evento: ela so
// reconstroi o que ja estava no banco. Por isso o construtor exige versao e
// instante, e nao os gera.
func TestReidratacaoPreservaEstadoLidoDoBanco(t *testing.T) {
	criadaEm := instanteFixo()
	atualizadaEm := criadaEm.Add(2 * time.Hour)

	c, err := Reidratar(
		deveID(t, carteiraA),
		deveID(t, jogadorA),
		deveParse(t, "20.00"),
		7,
		criadaEm,
		atualizadaEm,
	)
	if err != nil {
		t.Fatalf("Reidratar: %v", err)
	}

	if c.ID().String() != carteiraA {
		t.Errorf("ID e %q, esperado %q", c.ID(), carteiraA)
	}
	if c.Versao() != 7 {
		t.Errorf("Versao e %d, esperado 7", c.Versao())
	}
	if !c.CriadaEm().Equal(criadaEm) {
		t.Errorf("CriadaEm e %v, esperado %v", c.CriadaEm(), criadaEm)
	}
	if !c.AtualizadaEm().Equal(atualizadaEm) {
		t.Errorf("AtualizadaEm e %v, esperado %v", c.AtualizadaEm(), atualizadaEm)
	}
	if c.Saldo().Decimal() != "20.00" {
		t.Errorf("Saldo e %q, esperado %q", c.Saldo().Decimal(), "20.00")
	}
}

func TestReidratacaoRecusaEstadoInvalido(t *testing.T) {
	casos := []struct {
		nome     string
		id       Identificador
		jogador  Identificador
		saldo    money.Money
		versao   int64
		criadaEm time.Time
	}{
		{"id nao inicializado", Identificador{}, deveID(t, jogadorA), money.Zero(money.CurrencyBRL), 1, instanteFixo()},
		{"versao zero", deveID(t, carteiraA), deveID(t, jogadorA), money.Zero(money.CurrencyBRL), 0, instanteFixo()},
		{"versao negativa", deveID(t, carteiraA), deveID(t, jogadorA), money.Zero(money.CurrencyBRL), -1, instanteFixo()},
		{"criada em zero", deveID(t, carteiraA), deveID(t, jogadorA), money.Zero(money.CurrencyBRL), 1, time.Time{}},
		{"saldo nao inicializado", deveID(t, carteiraA), deveID(t, jogadorA), money.Money{}, 1, instanteFixo()},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := Reidratar(c.id, c.jogador, c.saldo, c.versao, c.criadaEm, c.criadaEm)
			if err == nil {
				t.Fatal("Reidratar aceitou estado invalido")
			}
		})
	}
}

func TestDebitoSuficiente(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	apos, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "80.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Debitar: %v", err)
	}

	if !apos.Saldo().Equal(deveParse(t, "20.00")) {
		t.Errorf("saldo e %s, esperado 20.00", apos.Saldo())
	}
	if !c.Saldo().Equal(deveParse(t, "100.00")) {
		t.Errorf("a carteira original mudou para %s: operacao tem de ser imutavel", c.Saldo())
	}
}

// O caso que o enunciado exige: 100.00 e duas apostas de 80.00. Aqui e a versao
// de dominio, sem concorrencia; a prova com tres processos vem na etapa de
// integracao. O que importa aqui e que a recusa nao mexe no saldo.
func TestDebitoInsuficienteRecusaSemAlterarSaldo(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	c, err = c.Debitar(deveID(t, transacaoA), deveParse(t, "80.00"), instanteFixo())
	if err != nil {
		t.Fatalf("primeiro debito: %v", err)
	}

	apos, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "80.00"), instanteFixo())
	if !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("devolveu %v, esperado ErrSaldoInsuficiente", err)
	}
	if !apos.Saldo().Equal(deveParse(t, "20.00")) {
		t.Errorf("saldo apos recusa e %s, esperado 20.00", apos.Saldo())
	}
}

func TestDebitoExatamenteIgualAoSaldo(t *testing.T) {
	// Saldo zerado por debito e um resultado legitimo, e e diferente de saldo
	// negativo. A constraint do banco vai ser CHECK (balance >= 0), e zero passa.
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	apos, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Debitar: %v", err)
	}

	if !apos.Saldo().IsZero() {
		t.Errorf("saldo e %s, esperado zero", apos.Saldo())
	}
}

func TestCredito(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	apos, err := c.Creditar(deveID(t, transacaoA), deveParse(t, "25.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Creditar: %v", err)
	}

	if !apos.Saldo().Equal(deveParse(t, "125.00")) {
		t.Errorf("saldo e %s, esperado 125.00", apos.Saldo())
	}
}

func TestCreditoEmCarteiraZerada(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), money.Zero(money.CurrencyBRL), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	apos, err := c.Creditar(deveID(t, transacaoA), deveParse(t, "10.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Creditar: %v", err)
	}

	if !apos.Saldo().Equal(deveParse(t, "10.00")) {
		t.Errorf("saldo e %s, esperado 10.00", apos.Saldo())
	}
}

// O saldo e int64 em unidade minima, e um credito acima do limite estouraria o
// saldo. E preferivel recusar a operacao a gravar um valor que o proprio Money
// nao representa, porque a constraint do banco nao tem como ajudar aqui: ela
// impede saldo negativo, nao saldo maior que o limite do tipo.
func TestCreditoAcimaDoLimiteDoTipoRecusa(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	limite, err := money.Parse("92233720368547758.07", money.CurrencyBRL)
	if err != nil {
		t.Fatalf("Parse do limite: %v", err)
	}

	if _, err := c.Creditar(deveID(t, transacaoA), limite, instanteFixo()); !errors.Is(err, money.ErrOverflow) {
		t.Errorf("devolveu %v, esperado money.ErrOverflow", err)
	}
}

// Credito de valor negativo viraria debito com outro nome, e o chamador teria
// dois jeitos de fazer a mesma coisa. Debito e credito sao metodos distintos
// porque a intencao e distinta, e o valor tem de concordar com a intencao.
func TestCreditoComValorNegativoRecusa(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	if _, err := c.Creditar(deveID(t, transacaoA), deveParse(t, "-25.00"), instanteFixo()); err == nil {
		t.Fatal("Creditar aceitou valor negativo")
	}
}

func TestOperacaoComValorNaoInicializadoRecusa(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	var naoInicializado money.Money

	if _, err := c.Debitar(deveID(t, transacaoA), naoInicializado, instanteFixo()); err == nil {
		t.Error("Debitar aceitou valor nao inicializado")
	}
	if _, err := c.Creditar(deveID(t, transacaoA), naoInicializado, instanteFixo()); err == nil {
		t.Error("Creditar aceitou valor nao inicializado")
	}
}

func TestOperacaoEmMoedaIncompativelRecusa(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	dolar, err := money.Parse("10.00", money.CurrencyUSD)
	if err != nil {
		t.Fatalf("Parse em USD: %v", err)
	}

	if _, err := c.Debitar(deveID(t, transacaoA), dolar, instanteFixo()); !errors.Is(err, ErrMoedaDaCarteira) {
		t.Errorf("devolveu %v, esperado ErrMoedaDaCarteira", err)
	}
	if _, err := c.Creditar(deveID(t, transacaoA), dolar, instanteFixo()); !errors.Is(err, ErrMoedaDaCarteira) {
		t.Errorf("devolveu %v, esperado ErrMoedaDaCarteira", err)
	}
}

func TestOperacaoComIdentificadorInvalidoRecusa(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	invalidos := map[string]Identificador{
		"nao inicializado": {},
		"vazio":            {},
	}

	for nome, id := range invalidos {
		t.Run(nome, func(t *testing.T) {
			if _, err := c.Debitar(id, deveParse(t, "10.00"), instanteFixo()); err == nil {
				t.Fatal("operacao com identificador invalido foi aceita")
			}
		})
	}
}

func TestIdentificadorDeRecusaTextoInvalido(t *testing.T) {
	invalidos := []string{
		"",
		"transacao-123",
		" 0192f298-345e-7e38-af88-e43f851a819d",
		"0192f298-345e-7e38-af88-e43f851a819",
		"0192f298345e7e38af88e43f851a819d",
		"0192f298-345e-7e38-af88-e43f851a819dd",
		"0192f298-345e-7e38-af88-e43f851a819g",
	}

	for _, texto := range invalidos {
		t.Run(texto, func(t *testing.T) {
			if id, err := IdentificadorDe(texto); err == nil {
				t.Errorf("IdentificadorDe(%q) aceitou e devolveu %q", texto, id)
			}
		})
	}
}

func TestIdentificadorDeAceitaUUIDValido(t *testing.T) {
	id := deveID(t, carteiraA)

	if id.String() != carteiraA {
		t.Errorf("String e %q, esperado %q", id, carteiraA)
	}
	if !id.Valida() {
		t.Error("UUID valido nao se diz valido")
	}
}
