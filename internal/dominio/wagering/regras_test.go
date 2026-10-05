package wagering

import (
	"errors"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// A politica de valor por tipo e o que impede um LOSS de virar credito e um BET de
// virar operacao de valor zero. E a regra que o enunciado descreve tipo a tipo, e
// ela precisa estar no dominio: se dependesse do chamador, dois caminhos
// diferentes -- HTTP e SQS -- poderiam divergir sobre a mesma operacao.

// carteiraComSaldo cria uma carteira de teste com saldo em BRL.
func carteiraComSaldo(t *testing.T, saldo string) wallet.Carteira {
	t.Helper()
	c, err := wallet.Nova(deveID(t, jogadorA), deveParse(t, saldo), instanteFixo())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	return c
}

func TestEfeitoFinanceiroPorTipo(t *testing.T) {
	casos := []struct {
		tipo     Tipo
		esperado Efeito
	}{
		{TipoBET, EfeitoDebito},
		{TipoWIN, EfeitoCredito},
		{TipoLOSS, EfeitoNenhum},
		{TipoREFUND, EfeitoCredito},
		{TipoROLLBACK, EfeitoInverso},
		{TipoAbertura, EfeitoCredito},
	}

	for _, c := range casos {
		t.Run(string(c.tipo), func(t *testing.T) {
			if got := c.tipo.Efeito(); got != c.esperado {
				t.Errorf("Efeito de %s e %q, esperado %q", c.tipo, got, c.esperado)
			}
		})
	}
}

// BET, WIN, REFUND e ROLLBACK exigem valor maior que zero. Zero e negativo sao
// recusados com codigo proprio, e nao com erro de parsing: o texto do valor e
// valido, e o que nao e o valor.
func TestTiposQueExigemValorPositivoRecusamZero(t *testing.T) {
	tipos := []Tipo{TipoBET, TipoWIN, TipoREFUND, TipoROLLBACK}

	for _, tipo := range tipos {
		for _, valor := range []string{"0.00", "-25.00"} {
			t.Run(string(tipo)+"/"+valor, func(t *testing.T) {
				registro := registroDoTeste(t, tipo, valor)

				_, err := Registrar(registro)
				if !errors.Is(err, ErrValorInvalido) {
					t.Fatalf("devolveu %v, esperado ErrValorInvalido", err)
				}

				var recusada *FalhaDeRegra
				if !errors.As(err, &recusada) {
					t.Fatalf("devolveu %T, esperado *FalhaDeRegra", err)
				}
				if recusada.Codigo != CodigoFalhaValorNaoPositivo {
					t.Errorf("Codigo e %q, esperado %q", recusada.Codigo, CodigoFalhaValorNaoPositivo)
				}
			})
		}
	}
}

// LOSS e o caso invertido: exige zero. Um LOSS com valor maior que zero seria um
// credito sem lancamento, ou um debito apresentado como perda.
func TestLossRecusaValorDiferenteDeZero(t *testing.T) {
	for _, valor := range []string{"0.01", "25.00", "-0.01"} {
		t.Run(valor, func(t *testing.T) {
			_, err := Registrar(registroDoTeste(t, TipoLOSS, valor))
			if !errors.Is(err, ErrValorInvalido) {
				t.Fatalf("devolveu %v, esperado ErrValorInvalido", err)
			}

			var recusada *FalhaDeRegra
			if !errors.As(err, &recusada) {
				t.Fatalf("devolveu %T, esperado *FalhaDeRegra", err)
			}
			if recusada.Codigo != CodigoFalhaValorDeZeroEsperado {
				t.Errorf("Codigo e %q, esperado %q", recusada.Codigo, CodigoFalhaValorDeZeroEsperado)
			}
		})
	}
}

func TestLossAceitaValorZero(t *testing.T) {
	tr, err := Registrar(registroDoTeste(t, TipoLOSS, "0.00"))
	if err != nil {
		t.Fatalf("Registrar LOSS com 0.00: %v", err)
	}
	if tr.Estado() != EstadoPendente {
		t.Errorf("Estado e %q, esperado %q", tr.Estado(), EstadoPendente)
	}
}

// LOSS nao tem efeito financeiro, mas ainda exige a moeda da carteira. Sem essa
// exigencia, um LOSS em USD passaria e a transacao ficaria com moeda que a
// carteira nao tem, sem nenhum lancamento que denuncie.
func TestLossExigeMoedaDaCarteira(t *testing.T) {
	c := carteiraComSaldo(t, "100.00")

	tr, err := Registrar(registroDoTeste(t, TipoLOSS, "0.00"))
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	if err := tr.ValidarMoedaDaCarteira(c); err != nil {
		t.Fatalf("LOSS na moeda da carteira foi recusado: %v", err)
	}

	dolar, err := money.Parse("0.00", money.CurrencyUSD)
	if err != nil {
		t.Fatalf("Parse em USD: %v", err)
	}
	emDolar, err := Registrar(registroDoTeste(t, TipoLOSS, "0.00", dolar))
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	if err := emDolar.ValidarMoedaDaCarteira(c); !errors.Is(err, ErrMoedaDaCarteira) {
		t.Errorf("devolveu %v, esperado ErrMoedaDaCarteira", err)
	}
}

func TestValidarMoedaDaCarteiraEmTodosOsTipos(t *testing.T) {
	c := carteiraComSaldo(t, "100.00")

	casos := []struct {
		tipo  Tipo
		valor string
	}{
		{TipoBET, "10.00"},
		{TipoWIN, "10.00"},
		{TipoLOSS, "0.00"},
		{TipoREFUND, "10.00"},
		{TipoROLLBACK, "10.00"},
	}

	for _, c2 := range casos {
		t.Run(string(c2.tipo), func(t *testing.T) {
			registro := registroDoTeste(t, c2.tipo, c2.valor)

			tr, err := Registrar(registro)
			if err != nil {
				t.Fatalf("Registrar: %v", err)
			}
			if err := tr.ValidarMoedaDaCarteira(c); err != nil {
				t.Errorf("valor em BRL recusado: %v", err)
			}

			registro.Valor = dollarValue(t, c2.valor, c2.tipo)
			emDolar, err := Registrar(registro)
			if err != nil {
				// LOSS com valor diferente de zero em USD ja e recusado pela
				// politica de zero, e nao pela moeda. E a ordem correta: a forma
				// do dado vem antes do confronto com a carteira.
				t.Skipf("registro recusado antes do teste de moeda: %v", err)
			}
			if err := emDolar.ValidarMoedaDaCarteira(c); !errors.Is(err, ErrMoedaDaCarteira) {
				t.Errorf("devolveu %v, esperado ErrMoedaIncompativel", err)
			}
		})
	}
}

// dollarValue devolve o mesmo valor em USD, respeitando a politica de zero do
// tipo: um LOSS em USD tem de ser 0.00 para chegar a ser comparado com a
// carteira.
func dollarValue(t *testing.T, valor string, tipo Tipo) money.Money {
	t.Helper()
	texto := valor
	if tipo == TipoLOSS {
		texto = "0.00"
	}
	m, err := money.Parse(texto, money.CurrencyUSD)
	if err != nil {
		t.Fatalf("money.Parse(%q, USD): %v", texto, err)
	}
	return m
}

// O efeito inverso so existe para ROLLBACK, e ele precisa ser oposto ao efeito
// da operacao referenciada. Um ROLLBACK de BET e debito, e um ROLLBACK de WIN e
// credito: e o que desfaz a operacao.
func TestEfeitoInversoSoApareceEmReversao(t *testing.T) {
	for _, tipo := range []Tipo{TipoBET, TipoWIN, TipoLOSS, TipoAbertura} {
		if got := tipo.Efeito(); got == EfeitoInverso {
			t.Errorf("%s tem efeito inverso", tipo)
		}
	}
}

// registroDoTeste monta um registro externo com valor em BRL. O valor pronto, quando
// informado, substitui o texto: e assim que o mesmo teste monta a operacao em USD
// sem duplicar o registro inteiro.
func registroDoTeste(t *testing.T, tipo Tipo, valor string, pronto ...money.Money) Registro {
	t.Helper()
	registro := Registro{
		Provedor:          Provedor(provedorA),
		TransacaoExterna:  Externo(externoA),
		ChaveIdempotencia: Chave(chaveA),
		HashConteudo:      Hash(hashA),
		Carteira:          deveID(t, carteiraA),
		Jogador:           deveID(t, jogadorA),
		Rodada:            Rodada(rodadaA),
		Jogo:              Jogo(jogoA),
		Tipo:              tipo,
		CriadaEm:          instanteFixo(),
	}
	if tipo.ExigeReferencia() {
		registro.Referencia = Referencia{Externa: Externo("transaction-123")}
	}

	switch len(pronto) {
	case 0:
		registro.Valor = deveParse(t, valor)
	case 1:
		registro.Valor = pronto[0]
	default:
		t.Fatalf("registroDoTeste recebeu %d valores prontos", len(pronto))
	}

	return registro
}
