package wagering

import "strings"

// Tipo e o tipo da operacao externa.
//
// O conjunto e fechado pelos tipos que o enunciado define. Tipo livre aqui
// seria tipo livre no schema, no contrato HTTP e no efeito financeiro, e cada um
// desses lugares teria de tratar o caso desconhecido.
type Tipo string

const (
	// TipoAbertura e a abertura interna de carteira. Nao vem de provedor: e
	// reservada ao servico interno.
	TipoAbertura Tipo = "OPENING"
	// TipoBET e a aposta: debito na carteira.
	TipoBET Tipo = "BET"
	// TipoWIN e o premio: credito na carteira.
	TipoWIN Tipo = "WIN"
	// TipoLOSS e a perda sem efeito financeiro: nao movimenta saldo, nao gera
	// lancamento e nao altera a versao da carteira.
	TipoLOSS Tipo = "LOSS"
	// TipoREFUND e a devolucao integral de uma aposta processada: credito.
	TipoREFUND Tipo = "REFUND"
	// TipoROLLBACK e o movimento contrario a uma operacao processada.
	TipoROLLBACK Tipo = "ROLLBACK"
)

// Valido informa se o tipo pertence ao conjunto conhecido.
func (t Tipo) Valido() bool {
	switch t {
	case TipoAbertura, TipoBET, TipoWIN, TipoLOSS, TipoREFUND, TipoROLLBACK:
		return true
	}
	return false
}

// ExternoValido informa se o tipo pode chegar de um provedor.
//
// Abertura e interna por definicao. O schema vai impedir a insercao com
// provedor e chave externa em um OPENING, e aqui a regra aparece antes, com
// mensagem que nomeia o tipo recusado.
func (t Tipo) ExternoValido() bool {
	return t.Valido() && t != TipoAbertura
}

// ExigeReferencia informa se o tipo so faz sentido com referenceExternalTransactionId.
//
// Vale para REFUND e ROLLBACK. Uma dessas operacoes sem referencia e uma
// operacao sem contra o que reverter, e tratar isso como "espere a referencia"
// esconderia entrada invalida atras de um estado de espera.
func (t Tipo) ExigeReferencia() bool {
	return t == TipoREFUND || t == TipoROLLBACK
}

// Valido informa se o codigo pertence ao conjunto conhecido.
//
// Um codigo que nao pertence ao conjunto e a mesma classe de defeito que um
// estado fora do conjunto: valor gravado que ninguem sabe interpretar.
func (c CodigoFalha) Valido() bool {
	switch c {
	case CodigoFalhaSemSaldo,
		CodigoFalhaReverSaoSemSaldo,
		CodigoFalhaValorNaoPositivo,
		CodigoFalhaValorDeZeroEsperado,
		CodigoFalhaReferenciaObrigatoria,
		CodigoFalhaReferenciaNaoEncontrada,
		CodigoFalhaReferenciaIncompativel,
		CodigoFalhaValorDivergente,
		CodigoFalhaReferenciaNaoProcessada,
		CodigoFalhaReversaoJaAplicada,
		CodigoFalhaTipoNaoSuportado,
		CodigoFalhaPersistencia:
		return true
	}
	return false
}

// Presente informa se ha codigo de falha associate a transacao.
//
// E distinto de Valido de proposito: a coluna no banco e nula quando nao ha
// falha, e string vazia e valor valido para o que representa -- ausencia. Sem
// os dois metodos, o codigo ausente seria "invalido" e o tratamento de erro
// confundiria dado faltando com dado errado.
func (c CodigoFalha) Presente() bool { return strings.TrimSpace(string(c)) != "" }
