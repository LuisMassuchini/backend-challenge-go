package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
)

// A versao da carteira existe para duas coisas: update condicional no banco, que
// impede lost update, e leitura de quanto o agregado mudou. Por isso a regra
// importa: a versao so sobe quando o saldo muda de fato.

// These tests fixam a regra de versionamento. A consequencia no banco -- update
// condicional com WHERE version = $n -- vem na etapa de repositorios.

func TestDebitoIncrementaVersao(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	apos, _, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "25.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Debitar: %v", err)
	}

	if apos.Versao() != 2 {
		t.Errorf("Versao e %d, esperado 2", apos.Versao())
	}
	if c.Versao() != 1 {
		// A carteira original nao pode ter sido alterada: a versao 1 precisa
		// continuar valendo para quem ainda tem o valor antigo em maos.
		t.Errorf("a carteira original mudou de versao para %d", c.Versao())
	}
}

func TestCreditoIncrementaVersao(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	apos, _, err := c.Creditar(deveID(t, transacaoA), deveParse(t, "25.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Creditar: %v", err)
	}

	if apos.Versao() != 2 {
		t.Errorf("Versao e %d, esperado 2", apos.Versao())
	}
}

func TestVersaoSobeACadaMudanca(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	for i := 0; i < 3; i++ {
		var err error
		c, _, err = c.Debitar(deveID(t, transacaoA), deveParse(t, "10.00"), instanteFixo())
		if err != nil {
			t.Fatalf("debito %d: %v", i, err)
		}
	}

	if c.Versao() != 4 {
		t.Errorf("Versao e %d, esperado 4 (1 inicial mais 3 movimentos)", c.Versao())
	}
}

// LOSS e a operacao que o enunciado diz que nao altera o saldo e nao cria
// lancamento. Se ela subisse a versao, todo LOSS causaria reescrita da linha da
// carteira e perderia a garantia de update condicional: um escritor concorrente
// receberia conflito por uma operacao que nao mexeu em nada.
func TestOperacaoSemEfeitoNaoIncrementaVersao(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	apos := c.SemEfeito(instanteFixo())

	if apos.Versao() != c.Versao() {
		t.Errorf("Versao e %d, esperado %d: operacao sem efeito nao versiona", apos.Versao(), c.Versao())
	}
	if !apos.Saldo().Equal(c.Saldo()) {
		t.Errorf("Saldo e %s, esperado %s", apos.Saldo(), c.Saldo())
	}
	if !apos.AtualizadaEm().Equal(c.AtualizadaEm()) {
		t.Errorf("AtualizadaEm e %v, esperado %v: operacao sem efeito nao marca a carteira como alterada", apos.AtualizadaEm(), c.AtualizadaEm())
	}
	if !apos.ID().Equal(c.ID()) {
		t.Error("operacao sem efeito trocou a identidade da carteira")
	}
}

// A versao sobe junto com a escrita da linha. Uma versao que sobe sem persistir
// faz o update condicional seguinte errar em falso, e uma persistencia sem
// subir a versao faz duas escritas consecutivas sobrescreverem a primeira.
func TestMudancaDeSaldoAtualizaOInstanteDeAlteracao(t *testing.T) {
	agora := instanteFixo()
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), agora)
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	depois := agora.Add(5 * time.Minute)
	apos, _, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "25.00"), depois)
	if err != nil {
		t.Fatalf("Debitar: %v", err)
	}

	if !apos.AtualizadaEm().Equal(depois) {
		t.Errorf("AtualizadaEm e %v, esperado %v", apos.AtualizadaEm(), depois)
	}
	if !apos.CriadaEm().Equal(agora) {
		t.Errorf("CriadaEm mudou para %v: a criacao nao e reescrita a cada movimento", apos.CriadaEm())
	}
}

// Um instante de operacao anterior ao da ultima alteracao indica dado
// corrompido: a carteira nao pode ter sido alterada antes de existir. Aceitar
// faria a ordenacao por AtualizadaEm perder o sentido, e a leitura de "ultima
// mudanca" passar a devolver a mais antiga.
func TestOperacaoComInstanteAnteriorACriacaoRecusa(t *testing.T) {
	agora := instanteFixo()
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), agora)
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	antes := agora.Add(-time.Hour)

	if _, _, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "25.00"), antes); err == nil {
		t.Error("Debitar aceitou instante anterior ao da criacao")
	}
	if _, _, err := c.Creditar(deveID(t, transacaoA), deveParse(t, "25.00"), antes); err == nil {
		t.Error("Creditar aceitou instante anterior ao da criacao")
	}
}

// A reidratacao nao versiona. Repor uma versao verificada no banco e o que
// permite retomar apos restart sem perder o controle de concorrencia, e
// reidratacao que somasse um mudaria a versao em cada leitura.
func TestReidratacaoNaoVersiona(t *testing.T) {
	criadaEm := instanteFixo()

	c, err := Reidratar(
		deveID(t, carteiraA),
		deveID(t, jogadorA),
		deveParse(t, "20.00"),
		7,
		criadaEm,
		criadaEm,
	)
	if err != nil {
		t.Fatalf("Reidratar: %v", err)
	}

	apos, _, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "5.00"), criadaEm.Add(time.Minute))
	if err != nil {
		t.Fatalf("Debitar: %v", err)
	}

	if apos.Versao() != 8 {
		t.Errorf("Versao e %d, esperado 8 (7 reidratados mais um movimento)", apos.Versao())
	}
}

// Saldo e valor do lancamento precisam concordar no tipo tambem: um lancamento
// com valor negativo nunca deveria existir, e o saldo resultante de um debito
// tem de ser derivavel do saldo anterior somado ao valor com o sinal da
// direcao. E a propriedade que a reconciliacao vai conferir linha a linha.
func TestLancamentoPermiteReconstruirSaldo(t *testing.T) {
	casos := []struct {
		nome      string
		direcao   Direcao
		posterior string
	}{
		{"debito", DirecaoDebito, "75.00"},
		{"credito", DirecaoCredito, "125.00"},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			l, err := NovoLancamento(
				deveID(t, idLancamento),
				deveID(t, carteiraA),
				deveID(t, transacaoA),
				c.direcao,
				deveParse(t, "25.00"),
				deveParse(t, "100.00"),
				deveParse(t, c.posterior),
				instanteFixo(),
			)
			if err != nil {
				t.Fatalf("NovoLancamento: %v", err)
			}

			var reconstruido money.Money
			if l.Direcao() == DirecaoCredito {
				reconstruido, err = l.SaldoAnterior().Add(l.Valor())
			} else {
				reconstruido, err = l.SaldoAnterior().Sub(l.Valor())
			}
			if err != nil {
				t.Fatalf("reconstrucao: %v", err)
			}
			if !reconstruido.Equal(l.SaldoPosterior()) {
				t.Errorf("reconstruido %s difere do saldo posterior %s", reconstruido, l.SaldoPosterior())
			}
		})
	}
}

// Erros de versionamento nao existem como categoria propria, e a assertao e
// sobre isso: um debito recusado por saldo insuficiente devolve a carteira
// original, com a versao intacta. Sem isso, o update condicional gravaria uma
// linha que nao mudou e o escritor concorrente receberia conflito sem motivo.
func TestOperacaoRecusadaNaoVersiona(t *testing.T) {
	c, err := Nova(deveID(t, jogadorA), deveParse(t, "100.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Nova: %v", err)
	}

	c, _, err = c.Debitar(deveID(t, transacaoA), deveParse(t, "90.00"), instanteFixo())
	if err != nil {
		t.Fatalf("primeiro debito: %v", err)
	}

	apos, _, err := c.Debitar(deveID(t, transacaoA), deveParse(t, "50.00"), instanteFixo())
	if !errors.Is(err, ErrSaldoInsuficiente) {
		t.Fatalf("devolveu %v, esperado ErrSaldoInsuficiente", err)
	}
	if apos.Versao() != c.Versao() {
		t.Errorf("Versao e %d, esperado %d: operacao recusada nao versiona", apos.Versao(), c.Versao())
	}

	var naoInicializada money.Money
	invalida, _, err := c.Creditar(deveID(t, transacaoA), naoInicializada, instanteFixo())
	if err == nil {
		t.Fatal("Creditar aceitou valor nao inicializado")
	}
	if invalida.Versao() != c.Versao() {
		t.Errorf("operacao com valor invalido versionou a carteira para %d", invalida.Versao())
	}
}
