package wagering

import (
	"errors"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// ValidarReversao confere a reversao contra a transacao que ela reverte.
//
// A ordem das conferencias e fixa, e a ordem faz parte do contrato: valor antes
// de tipo, referencia resolvida antes de estado. Um provedor que envia um LOSS com
// valor 25.00 como reversao recebe VALOR_DIVERGENTE_DA_REFERENCIA, e nao
// REFERENCIA_INCOMPATIVEL -- porque o problema visivel e o valor.
//
// Os tipos ja aplicados sao as reversoes ja concluidas sobre a MESMA referencia.
// Vem do caso de uso, que le do banco; o dominio decide.
func (t Transacao) ValidarReversao(referenciada Transacao, tiposAplicados []Tipo) error {
	if !t.tipo.ExigeReferencia() {
		// Operacao direta nao tem politica de reversao. Aplicar a regra a um BET
		// recusaria um BET legitimo.
		return nil
	}
	if !t.Valida() {
		return fmt.Errorf("%w: transacao nao inicializada", ErrRegistroInvalido)
	}

	if !t.referencia.Externa.Valida() {
		return regra(CodigoFalhaReferenciaObrigatoria,
			fmt.Sprintf("%s sem referenceExternalTransactionId", t.tipo))
	}
	if !t.referenciaInterna.Valida() {
		return regra(CodigoFalhaReferenciaNaoEncontrada,
			fmt.Sprintf("%s com referencia %q ainda nao resolvida", t.tipo, t.referencia.Externa))
	}
	if t.referenciaInterna != referenciada.ID() {
		return regra(CodigoFalhaReferenciaIncompativel,
			fmt.Sprintf("%s aponta para %s e foi resolvida para %s",
				t.tipo, t.referenciaInterna, referenciada.ID()))
	}

	if referenciada.Estado() != EstadoProcessado {
		return regra(CodigoFalhaReferenciaNaoProcessada,
			fmt.Sprintf("%s referencia %s em %s", t.tipo, t.referencia.Externa, referenciada.Estado()))
	}

	if !t.compativelCom(referenciada) {
		return regra(CodigoFalhaReferenciaIncompativel,
			fmt.Sprintf("%s nao concorda com %s em provedor, jogador, carteira, moeda ou rodada", t.tipo, t.referencia.Externa))
	}
	if !t.valor.Equal(referenciada.valor) {
		return regra(CodigoFalhaValorDivergente,
			fmt.Sprintf("%s de %s contra %s de %s", t.tipo, t.valor, t.referencia.Externa, referenciada.valor))
	}
	if !t.tipo.reverte(referenciada.tipo) {
		return regra(CodigoFalhaReferenciaIncompativel,
			fmt.Sprintf("%s nao reverte %s", t.tipo, referenciada.tipo))
	}

	return t.validarPoliticaDeReversao(referenciada, tiposAplicados)
}

// reverte informa se o tipo de reversao faz sentido sobre o tipo referenciado.
//
// REFUND devolve o debito de uma aposta e nada mais: nao ha o que devolver em um
// WIN ou em um LOSS. ROLLBACK desfaz qualquer operacao que tenha mexido no saldo,
// inclusive um REFUND -- e e o caminho para anular uma devolucao.
func (t Tipo) reverte(referenciado Tipo) bool {
	switch t {
	case TipoREFUND:
		return referenciado == TipoBET
	case TipoROLLBACK:
		return referenciado == TipoBET || referenciado == TipoWIN || referenciado == TipoREFUND
	}
	return false
}

// validarPoliticaDeReversao e a decisao fechada para combinacoes de reversao.
//
// Uma BET e neutralizada uma unica vez: ou por REFUND, ou por ROLLBACK, nunca
// pelas duas. As duas devolvem o mesmo debito, e aceita-las em sequencia creditaria
// duas vezes o mesmo dinheiro.
//
// Um WIN e desfeito uma unica vez, por ROLLBACK. Um REFUND tambem e desfeito uma
// unica vez, por ROLLBACK. E nao existe caminho para desfazer um ROLLBACK: a
// operacao ja e o movimento contrario, e desfazer o contrario e refazer a
// operacao original, que o provedor deve enviar de novo com a chave dele.
func (t Transacao) validarPoliticaDeReversao(referenciada Transacao, tiposAplicados []Tipo) error {
	for _, aplicado := range tiposAplicados {
		if aplicado == t.tipo {
			return regra(CodigoFalhaReversaoJaAplicada,
				fmt.Sprintf("%s ja aplicado sobre %s", aplicado, t.referencia.Externa))
		}
		if referenciada.tipo == TipoBET && t.neutralizaBET(aplicado) {
			return regra(CodigoFalhaReversaoJaAplicada,
				fmt.Sprintf("a aposta %s ja foi neutralizada por %s", t.referencia.Externa, aplicado))
		}
	}
	return nil
}

// neutralizaBET informa se um tipo de reversao ja aprovado zera o efeito de uma
// BET. Ambos devolvem o mesmo debito, entao qualquer um deles impede o outro.
func (Transacao) neutralizaBET(aplicado Tipo) bool {
	return aplicado == TipoREFUND || aplicado == TipoROLLBACK
}

// compativelCom exige que operacao e referencia concordem em provedor, jogador,
// carteira, moeda e rodada.
//
// A rodada e a mais relevante: uma reversao que atravessa rodada devolve dinheiro
// de uma aposta para outra, e o provedor nao tem como conferir.
func (t Transacao) compativelCom(referenciada Transacao) bool {
	return t.provedor == referenciada.provedor &&
		t.jogador == referenciada.jogador &&
		t.carteira == referenciada.carteira &&
		t.rodada == referenciada.rodada &&
		t.valor.Currency() == referenciada.valor.Currency()
}

// Aplicar executa o efeito financeiro do tipo sobre a carteira.
//
// E o unico caminho que movimenta a carteira a partir de uma transacao. Devolve a
// carteira e o lancamento juntos porque os dois precisam ser confirmados na mesma
// transacao SQL: um lancamento sem saldo mudado, ou saldo mudado sem lancamento,
// quebra a reconciliacao.
//
// O valor devolvido em caso de erro e a carteira de entrada, sem alteracao. E o
// lancamento e invalido.
func (t Transacao) Aplicar(
	c wallet.Carteira,
	referenciada *Transacao,
	agora time.Time,
) (wallet.Carteira, wallet.Lancamento, error) {
	if !t.Valida() {
		return c, wallet.Lancamento{}, fmt.Errorf("%w: transacao nao inicializada", ErrRegistroInvalido)
	}
	if err := t.ValidarMoedaDaCarteira(c); err != nil {
		return c, wallet.Lancamento{}, err
	}

	efeito := t.tipo.Efeito()
	if efeito == EfeitoNenhum {
		// LOSS e a unica operacao sem efeito. Ela existe, e confirmada e
		// auditada, e nao produz lancamento nem altera a versao da carteira.
		return c.SemEfeito(agora), wallet.Lancamento{}, nil
	}

	if efeito == EfeitoInverso {
		if referenciada == nil {
			return c, wallet.Lancamento{}, fmt.Errorf(
				"%w: %s sem a operacao referenciada", ErrRegistroInvalido, t.tipo,
			)
		}
		efeito = tipoInverso(referenciada.Tipo()).Efeito()
	}

	if efeito == EfeitoDebito {
		depois, lancamento, err := c.Debitar(t.id, t.valor, agora)
		if err != nil {
			if errors.Is(err, wallet.ErrSaldoInsuficiente) && t.tipo == TipoROLLBACK {
				// O erro carrega as duas informacoes de proposito. A causa
				// wallet.ErrSaldoInsuficiente deixa o caso de uso tratar saldo
				// insuficiente como saldo insuficiente, e o codigo
				// ROLLBACK_SEM_SALDO distingue "faltava saldo para a aposta" de
				// "faltava saldo para desfazer o premio". Sao problemas
				// contabilmente diferentes e o provedor trata os dois de formas
				// diferentes: no primeiro ele espera deposito, no segundo ele
				// nao pode fazer nada.
				return c, wallet.Lancamento{}, fmt.Errorf("%w: %w",
					wallet.ErrSaldoInsuficiente,
					regra(CodigoFalhaReverSaoSemSaldo,
						fmt.Sprintf("ROLLBACK de %s sobre saldo de %s", t.valor, c.Saldo())),
				)
			}
			return c, wallet.Lancamento{}, err
		}
		return depois, lancamento, nil
	}

	depois, lancamento, err := c.Creditar(t.id, t.valor, agora)
	if err != nil {
		return c, wallet.Lancamento{}, err
	}
	return depois, lancamento, nil
}

// tipoInverso devolve um tipo cujo efeito e o contrario do informado.
//
// O inverso e decidido pelo efeito, e nao pelo tipo: BET e debito, e o contrario de
// um debito e WIN; WIN e REFUND sao credito, e o contrario de um credito e BET.
// Decidir pelo tipo erra no REFUND, que e credito e nao debito -- e um ROLLBACK de
// REFUND que credita em vez de debitar devolve o dinheiro duas vezes.
func tipoInverso(referenciado Tipo) Tipo {
	if referenciado.Efeito() == EfeitoCredito {
		return TipoBET
	}
	return TipoWIN
}

// regra monta uma recusa com codigo estavel.
func regra(codigo CodigoFalha, motivo string) error {
	return &FalhaDeRegra{Codigo: codigo, Motivo: motivo}
}
