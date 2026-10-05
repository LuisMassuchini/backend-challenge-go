package wagering

import (
	"errors"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// Estas sao as duas unicas operacoes que dependem de outra. A politica de
// reversao foi fechada antes de codar, e o registro da decisao esta em
// ARCHITECTURE.md: o resumo e que uma BET pode ser neutralizada uma unica vez,
// por REFUND ou por ROLLBACK, e nunca pelas duas.

// processadaDoTeste devolve uma transacao ja processada, que e o unico tipo que
// pode ser referenciado.
func processadaDoTeste(t *testing.T, tipo Tipo, valor string) Transacao {
	t.Helper()
	registro := registroDoTeste(t, tipo, valor)
	if tipo == TipoLOSS {
		registro.Valor = deveParse(t, "0.00")
	}

	tr, err := Registrar(registro)
	if err != nil {
		t.Fatalf("Registrar %s: %v", tipo, err)
	}

	processada, err := tr.Processar(deveParse(t, valor), instanteFixo())
	if err != nil {
		t.Fatalf("Processar %s: %v", tipo, err)
	}
	return processada
}

// referenciaResolvida liga a reversao a transacao referenciada, que e o que o
// worker de referencias faz quando encontra a referencia.
func referenciaResolvida(t *testing.T, reversao Transacao, referenciada Transacao) Transacao {
	t.Helper()
	resolvida, err := reversao.ResolverReferencia(referenciada.ID(), instanteFixo())
	if err != nil {
		t.Fatalf("ResolverReferencia: %v", err)
	}
	return resolvida
}

// REFUND e ROLLBACK sem referenceExternalTransactionId sao entrada invalida, e nao
// uma operacao a espera de referencia. Uma reversao sem referencia e uma operacao
// sem contra o que reverter.
func TestReversaoSemReferenciaRecusaNaEntrada(t *testing.T) {
	for _, tipo := range []Tipo{TipoREFUND, TipoROLLBACK} {
		t.Run(string(tipo), func(t *testing.T) {
			registro := registroDoTeste(t, tipo, "25.00")
			registro.Referencia = Referencia{}

			_, err := Registrar(registro)
			if err == nil {
				t.Fatal("reversao sem referencia foi aceita")
			}

			var recusada *FalhaDeRegra
			if !errors.As(err, &recusada) {
				t.Fatalf("devolveu %T, esperado *FalhaDeRegra", err)
			}
			if recusada.Codigo != CodigoFalhaReferenciaObrigatoria {
				t.Errorf("Codigo e %q, esperado %q", recusada.Codigo, CodigoFalhaReferenciaObrigatoria)
			}
		})
	}
}

// A referencia so pode ser usada depois de resolvida. Validar contra uma
// referencia que o codigo carrega sem ter procurado no banco e o caminho para
// reverter a aposta errada.
func TestReversaoComReferenciaNaoResolvidaRecusa(t *testing.T) {
	aposta := processadaDoTeste(t, TipoBET, "25.00")
	_ = aposta

	reversao := reversaoDoTeste(t, TipoROLLBACK)

	if err := reversao.ValidarReversao(reversao, nil); err == nil {
		t.Fatal("reversao sem referencia resolvida foi aceita")
	}
}

// Uma referencia que ainda nao terminou com sucesso nao pode ser revertida.
func TestReversaoDeReferenciaNaoProcessadaRecusa(t *testing.T) {
	pendente, err := Registrar(registroDoTeste(t, TipoBET, "25.00"))
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	reversao := referenciaResolvida(t, reversaoDoTeste(t, TipoROLLBACK), pendente)

	err = reversao.ValidarReversao(pendente, nil)
	if !erroDeRegraComCodigo(err, CodigoFalhaReferenciaNaoProcessada) {
		t.Fatalf("devolveu %v, esperado %q", err, CodigoFalhaReferenciaNaoProcessada)
	}

	rejeitada, err := pendente.Rejeitar(CodigoFalhaSemSaldo, instanteFixo())
	if err != nil {
		t.Fatalf("Rejeitar: %v", err)
	}
	if err := reversao.ValidarReversao(rejeitada, nil); !erroDeRegraComCodigo(err, CodigoFalhaReferenciaNaoProcessada) {
		t.Errorf("reversao de referencia rejeitada devolveu %v, esperado %q", err, CodigoFalhaReferenciaNaoProcessada)
	}
}

// Operacao e referencia precisam concordar em provedor, jogador, carteira, moeda e
// rodada. A rodada e a mais relevante: uma reversao que atravessa rodada devolve
// dinheiro de uma aposta para outra e o provedor nao consegue conferir.
func TestReversaoExigeConcordanciaEntreOperacaoEReferencia(t *testing.T) {
	base := processadaDoTeste(t, TipoBET, "25.00")
	reversao := reversaoDoTeste(t, TipoROLLBACK)

	divergencias := map[string]func(Transacao) Transacao{
		"provedor": func(tr Transacao) Transacao {
			outra, err := Reidratar(dadosDe(tr))
			if err != nil {
				t.Fatalf("Reidratar: %v", err)
			}
			alterada := outra
			alterada.provedor = Provedor("provider-b")
			return alterada
		},
		"moeda": func(tr Transacao) Transacao {
			return transacaoEmUSD(t, tr)
		},
	}

	for nome, divergir := range divergencias {
		t.Run(nome, func(t *testing.T) {
			referenciada := divergir(base)
			resolvida := referenciaResolvida(t, reversao, referenciada)

			err := resolvida.ValidarReversao(referenciada, nil)
			if !erroDeRegraComCodigo(err, CodigoFalhaReferenciaIncompativel) {
				t.Errorf("devolveu %v, esperado %q", err, CodigoFalhaReferenciaIncompativel)
			}
		})
	}
}

// O valor da reversao tem de ser igual ao valor referenciado. Reversao parcial
// esta fora do desafio, e aceita-la exigiria uma segunda regra que ninguem pediu:
// o que fazer com a parte nao devolvida.
func TestReversaoComValorDivergenteRecusa(t *testing.T) {
	aposta := processadaDoTeste(t, TipoBET, "25.00")
	reversao := referenciaResolvida(t, reversaoDoTeste(t, TipoROLLBACK), aposta)

	menor, err := Registrar(registroDoTeste(t, TipoROLLBACK, "10.00"))
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}
	menor = referenciaResolvida(t, menor, aposta)

	if err := menor.ValidarReversao(aposta, nil); !erroDeRegraComCodigo(err, CodigoFalhaValorDivergente) {
		t.Errorf("devolveu %v, esperado %q", err, CodigoFalhaValorDivergente)
	}

	if err := reversao.ValidarReversao(aposta, nil); err != nil {
		t.Errorf("reversao com valor igual foi recusada: %v", err)
	}
}

// REFUND devolve o valor de uma BET. Referenciar WIN ou LOSS nao faz sentido:
// nao ha o que devolver, e um REFUND de LOSS devolveria dinheiro por uma operacao
// que nunca movementou a carteira.
func TestRefundSoPodeReferenciarBet(t *testing.T) {
	aposta := processadaDoTeste(t, TipoBET, "25.00")

	reversao := referenciaResolvida(t, reversaoDoTeste(t, TipoREFUND), aposta)
	if err := reversao.ValidarReversao(aposta, nil); err != nil {
		t.Fatalf("REFUND de BET recusado: %v", err)
	}

	win := processadaDoTeste(t, TipoWIN, "25.00")
	refundDeWin := referenciaResolvida(t, reversaoDoTeste(t, TipoREFUND), win)
	if err := refundDeWin.ValidarReversao(win, nil); !erroDeRegraComCodigo(err, CodigoFalhaReferenciaIncompativel) {
		t.Errorf("devolveu %v, esperado %q", err, CodigoFalhaReferenciaIncompativel)
	}

	loss := processadaDoTeste(t, TipoLOSS, "0.00")
	refundDeLoss := referenciaResolvida(t, reversaoDoTeste(t, TipoREFUND), loss)
	if err := refundDeLoss.ValidarReversao(loss, nil); !erroDeRegraComCodigo(err, CodigoFalhaValorDivergente) {
		t.Errorf("devolveu %v, esperado %q: LOSS tem valor zero, e a divergencia de valor vem antes do tipo", err, CodigoFalhaValorDivergente)
	}
}

// ROLLBACK desfaz BET, WIN ou REFUND. Desfazer um REFUND e o caminho para anular
// uma devolucao, e e permitido: o credito devolvido e desfeito por credito inverso.
func TestRollbackPodeReferenciarBetWinOuRefund(t *testing.T) {
	permitidos := []Tipo{TipoBET, TipoWIN, TipoREFUND}

	for _, tipo := range permitidos {
		t.Run(string(tipo), func(t *testing.T) {
			referenciada := processadaDoTeste(t, tipo, "25.00")
			reversao := referenciaResolvida(t, reversaoDoTeste(t, TipoROLLBACK), referenciada)

			if err := reversao.ValidarReversao(referenciada, nil); err != nil {
				t.Errorf("ROLLBACK de %s recusado: %v", tipo, err)
			}
		})
	}
}

// A politica fechada para reversao: uma BET e neutralizada uma unica vez. Depois
// de um REFUND bem-sucedido, um ROLLBACK sobre a mesma BET seria um segundo
// credito do mesmo debito, e depois de um ROLLBACK, um REFUND seria a mesma coisa
// pelo outro caminho.
func TestBetNaoPodeSerNeutralizadaDuasVezes(t *testing.T) {
	aposta := processadaDoTeste(t, TipoBET, "25.00")

	casos := map[string]struct {
		reversao  Tipo
		aplicadas []Tipo
	}{
		"refund depois de refund":     {TipoREFUND, []Tipo{TipoREFUND}},
		"rollback depois de rollback": {TipoROLLBACK, []Tipo{TipoROLLBACK}},
		"rollback depois de refund":   {TipoROLLBACK, []Tipo{TipoREFUND}},
		"refund depois de rollback":   {TipoREFUND, []Tipo{TipoROLLBACK}},
	}

	for nome, c := range casos {
		t.Run(nome, func(t *testing.T) {
			reversao := referenciaResolvida(t, reversaoDoTeste(t, c.reversao), aposta)

			err := reversao.ValidarReversao(aposta, c.aplicadas)
			if !erroDeRegraComCodigo(err, CodigoFalhaReversaoJaAplicada) {
				t.Errorf("devolveu %v, esperado %q", err, CodigoFalhaReversaoJaAplicada)
			}
		})
	}
}

// Um WIN pode ser desfeito uma vez. O que nao pode e desfazer o ROLLBACK do WIN
// com um REFUND: REFUND so referencia BET.
func TestReversaoDeWinAceitaApenasUmRollback(t *testing.T) {
	win := processadaDoTeste(t, TipoWIN, "25.00")

	primeiro := referenciaResolvida(t, reversaoDoTeste(t, TipoROLLBACK), win)
	if err := primeiro.ValidarReversao(win, nil); err != nil {
		t.Fatalf("primeiro ROLLBACK de WIN recusado: %v", err)
	}

	segundo := referenciaResolvida(t, reversaoDoTeste(t, TipoROLLBACK), win)
	if err := segundo.ValidarReversao(win, []Tipo{TipoROLLBACK}); !erroDeRegraComCodigo(err, CodigoFalhaReversaoJaAplicada) {
		t.Errorf("devolveu %v, esperado %q", err, CodigoFalhaReversaoJaAplicada)
	}
}

// Uma transacao que nao e reversao nao tem politica de reversao. Aplicar a regra
// a um BET recusa um BET legitimo.
func TestOperacaoDiretaNaoTemPoliticaDeReversao(t *testing.T) {
	bet := externaDoTeste(t)

	if err := bet.ValidarReversao(bet, []Tipo{TipoREFUND, TipoROLLBACK}); err != nil {
		t.Errorf("BET foi afetado pela politica de reversao: %v", err)
	}
}

// erroDeRegraComCodigo evita repetir o bloco de errors.As em cada caso.
func erroDeRegraComCodigo(err error, codigo CodigoFalha) bool {
	var recusada *FalhaDeRegra
	return errors.As(err, &recusada) && recusada.Codigo == codigo
}

// Aplicar o efeito e o unico caminho que movimenta a carteira, e ele traduz o
// efeito do tipo em movimento concreto.
func TestAplicarEfeitoPorTipo(t *testing.T) {
	casos := []struct {
		nome     string
		tipo     Tipo
		valor    string
		saldo    string
		esperado string
		credito  bool
	}{
		{"bet debita", TipoBET, "25.00", "100.00", "75.00", false},
		{"win credita", TipoWIN, "25.00", "100.00", "125.00", true},
		{"abertura credita", TipoAbertura, "50.00", "0.00", "50.00", true},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var (
				tr  Transacao
				err error
			)
			if c.tipo == TipoAbertura {
				tr, err = AbrirCarteira(Abertura{
					Carteira: deveID(t, carteiraA),
					Jogador:  deveID(t, jogadorA),
					Valor:    deveParse(t, c.valor),
					CriadaEm: instanteFixo(),
				})
			} else {
				tr, err = Registrar(registroDoTeste(t, c.tipo, c.valor))
			}
			if err != nil {
				t.Fatalf("criar transacao: %v", err)
			}

			carteira := carteiraComSaldo(t, c.saldo)
			apos, lancamento, err := tr.Aplicar(carteira, nil, instanteFixo())
			if err != nil {
				t.Fatalf("Aplicar: %v", err)
			}

			if !apos.Saldo().Equal(deveParse(t, c.esperado)) {
				t.Errorf("saldo e %s, esperado %s", apos.Saldo(), c.esperado)
			}
			if !lancamento.Valida() {
				t.Fatal("operacao com efeito nao produziu lancamento")
			}
			credito := lancamento.Direcao() == wallet.DirecaoCredito
			if credito != c.credito {
				t.Errorf("direcao de %s: credito=%v, esperado %v", c.tipo, credito, c.credito)
			}
		})
	}
}

// LOSS processado nao movimenta saldo, nao produz lancamento e nao altera a
// versao. O enunciado pede exatamente isso, e e o unico tipo com efeito nenhum.
func TestAplicarLossNaoMoveNada(t *testing.T) {
	tr, err := Registrar(registroDoTeste(t, TipoLOSS, "0.00"))
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}

	carteira := carteiraComSaldo(t, "100.00")
	apos, lancamento, err := tr.Aplicar(carteira, nil, instanteFixo())
	if err != nil {
		t.Fatalf("Aplicar: %v", err)
	}

	if lancamento.Valida() {
		t.Error("LOSS produziu lancamento")
	}
	if !apos.Saldo().Equal(carteira.Saldo()) {
		t.Errorf("saldo e %s, esperado %s", apos.Saldo(), carteira.Saldo())
	}
	if apos.Versao() != carteira.Versao() {
		t.Errorf("versao e %d, esperado %d", apos.Versao(), carteira.Versao())
	}
}

// REFUND credita de volta o valor da aposta.
func TestAplicarRefundCreditaODebito(t *testing.T) {
	aposta := processadaDoTeste(t, TipoBET, "25.00")
	reversao := referenciaResolvida(t, reversaoDoTeste(t, TipoREFUND), aposta)

	carteira := carteiraComSaldo(t, "75.00")
	apos, lancamento, err := reversao.Aplicar(carteira, &aposta, instanteFixo())
	if err != nil {
		t.Fatalf("Aplicar: %v", err)
	}

	if !apos.Saldo().Equal(deveParse(t, "100.00")) {
		t.Errorf("saldo e %s, esperado 100.00", apos.Saldo())
	}
	if lancamento.Direcao() != wallet.DirecaoCredito {
		t.Errorf("direcao e %q, esperado %q", lancamento.Direcao(), wallet.DirecaoCredito)
	}
}

// ROLLBACK faz o contrario da referenciada: BET vira credito, WIN vira debito.
// E o que desfaz a operacao, e nao uma devolucao com outro nome.
func TestAplicarRollbackEhOContrarioDaReferencia(t *testing.T) {
	casos := []struct {
		nome         string
		referenciada Tipo
		saldoInicial string
		esperado     string
		credito      bool
	}{
		{"rollback de bet credita", TipoBET, "75.00", "100.00", true},
		{"rollback de win debita", TipoWIN, "125.00", "100.00", false},
		{"rollback de refund debita", TipoREFUND, "125.00", "100.00", false},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			referenciada := processadaDoTeste(t, c.referenciada, "25.00")
			reversao := referenciaResolvida(t, reversaoDoTeste(t, TipoROLLBACK), referenciada)

			carteira := carteiraComSaldo(t, c.saldoInicial)
			apos, lancamento, err := reversao.Aplicar(carteira, &referenciada, instanteFixo())
			if err != nil {
				t.Fatalf("Aplicar: %v", err)
			}

			if !apos.Saldo().Equal(deveParse(t, c.esperado)) {
				t.Errorf("saldo e %s, esperado %s", apos.Saldo(), c.esperado)
			}
			credito := lancamento.Direcao() == wallet.DirecaoCredito
			if credito != c.credito {
				t.Errorf("credito=%v, esperado %v", credito, c.credito)
			}
		})
	}
}

// Um ROLLBACK que precisaria debitar mais que o saldo e recusado, e com codigo
// diferente do de uma aposta sem saldo. No primeiro o jogador nao tem saldo para
// o movimento inverso; no segundo nao tinha saldo para a aposta. Tratar os dois
// como o mesmo motivo esconde um problema contabil.
func TestRollbackQueEstourariaSaldoRecusaComCodigoProprio(t *testing.T) {
	reversao := referenciaResolvida(t, reversaoDoTeste(t, TipoROLLBACK), processadaDoTeste(t, TipoWIN, "25.00"))

	// O jogador gastou o premio em outra coisa: sobrou menos do que o WIN.
	carteira := carteiraComSaldo(t, "10.00")

	_, _, err := reversao.Aplicar(carteira, ptr(processadaDoTeste(t, TipoWIN, "25.00")), instanteFixo())
	if err == nil {
		t.Fatal("ROLLBACK que debitava mais que o saldo foi aceito")
	}
	if !errors.Is(err, wallet.ErrSaldoInsuficiente) {
		t.Fatalf("devolveu %v, esperado wallet.ErrSaldoInsuficiente", err)
	}

	var recusada *FalhaDeRegra
	if !errors.As(err, &recusada) {
		t.Fatalf("devolveu %T, esperado *FalhaDeRegra", err)
	}
	if recusada.Codigo != CodigoFalhaReverSaoSemSaldo {
		t.Errorf("Codigo e %q, esperado %q", recusada.Codigo, CodigoFalhaReverSaoSemSaldo)
	}
	if recusada.Codigo == CodigoFalhaSemSaldo {
		t.Error("ROLLBACK sem saldo nao pode usar o codigo de BET sem saldo")
	}
}

// ROLLBACK sem a referencia resolvida nao pode ser aplicado. Aplicar o contrario
// sem saber o que foi feito antes e a operacao que produz saldo errado.
func TestAplicarReversaoSemReferenciaRecusa(t *testing.T) {
	reversao := reversaoDoTeste(t, TipoROLLBACK)
	carteira := carteiraComSaldo(t, "100.00")

	if _, _, err := reversao.Aplicar(carteira, nil, instanteFixo()); err == nil {
		t.Fatal("ROLLBACK foi aplicado sem referencia")
	}
}

// Transacao invalida nao movimenta carteira nenhuma.
func TestAplicarEmTransacaoInvalidaRecusa(t *testing.T) {
	var tr Transacao
	carteira := carteiraComSaldo(t, "100.00")

	if _, _, err := tr.Aplicar(carteira, nil, instanteFixo()); err == nil {
		t.Fatal("transacao nao inicializada foi aplicada")
	}
}

// dadosDe reconstroi os dados de reidratacao de uma transacao, para que o teste
// possa alterar um campo e reidratar.
func dadosDe(tr Transacao) Dados {
	return Dados{
		ID:                tr.ID(),
		Provedor:          tr.Provedor(),
		TransacaoExterna:  tr.TransacaoExterna(),
		ChaveIdempotencia: tr.ChaveIdempotencia(),
		HashConteudo:      tr.HashConteudo(),
		Carteira:          tr.Carteira(),
		Jogador:           tr.Jogador(),
		Rodada:            tr.Rodada(),
		Jogo:              tr.Jogo(),
		Tipo:              tr.Tipo(),
		Valor:             tr.Valor(),
		Referencia:        tr.Referencia(),
		Estado:            tr.Estado(),
		CodigoFalha:       tr.CodigoFalha(),
		ReferenciaInterna: tr.ReferenciaInterna(),
		Resultado:         tr.Resultado(),
		CriadaEm:          tr.CriadaEm(),
		AtualizadaEm:      tr.AtualizadaEm(),
	}
}

// transacaoEmUSD devolve a mesma transacao com o valor em outra moeda.
func transacaoEmUSD(t *testing.T, tr Transacao) Transacao {
	t.Helper()
	valor := tr.Valor().Decimal()
	if tr.Tipo() == TipoLOSS {
		valor = "0.00"
	}
	emUSD, err := money.Parse(valor, money.CurrencyUSD)
	if err != nil {
		t.Fatalf("money.Parse(%q, USD): %v", valor, err)
	}

	dados := dadosDe(tr)
	dados.Valor = emUSD

	alterada, err := Reidratar(dados)
	if err != nil {
		t.Fatalf("Reidratar em USD: %v", err)
	}
	return alterada
}

func ptr(tr Transacao) *Transacao { return &tr }
