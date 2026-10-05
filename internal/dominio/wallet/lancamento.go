package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
)

// Direcao e o sentido do lancamento no ledger.
//
// O dominio tem dois valores e nao uma string livre porque o ledger e
// append-only e a soma do ledger tem de voltar ao saldo. Uma terceira direcao
// -- "TRANSFER", por exemplo -- nao tem soma associada, e admiti-la faria o
// reconciliacao descobrir a soma a partir do texto.
type Direcao string

const (
	// DirecaoDebito reduz o saldo.
	DirecaoDebito Direcao = "DEBIT"
	// DirecaoCredito aumenta o saldo.
	DirecaoCredito Direcao = "CREDIT"
)

// Erros do lancamento.
var (
	// ErrDirecaoInvalida cobre direcao fora do conjunto de duas.
	ErrDirecaoInvalida = errors.New("wallet: direcao de lancamento invalida")
	// ErrSaldoPosteriorIncompativel cobre lancamento em que o saldo posterior
	// nao corresponde ao valor e a direcao.
	ErrSaldoPosteriorIncompativel = errors.New("wallet: saldo posterior incompativel com valor e direcao")
	// ErrMoedaDoLancamento cobre lancamento em que valor, saldo anterior e saldo
	// posterior nao tem a mesma moeda.
	ErrMoedaDoLancamento = errors.New("wallet: moedas divergentes no lancamento")
)

// Lancamento e uma linha do ledger da carteira.
//
// Imutavel por definicao: e append-only, e correcao financeira se faz com um
// novo lancamento, nunca editando este. O banco reforca isso com trigger; aqui a
// garantia e de construcao, porque nao ha setter e nao ha metodo que altere
// qualquer campo depois de criado.
type Lancamento struct {
	id             Identificador
	carteira       Identificador
	transacao      Identificador
	direcao        Direcao
	valor          money.Money
	saldoAnterior  money.Money
	saldoPosterior money.Money
	criadoEm       time.Time
}

// NovoLancamento constroi um lancamento e valida a relacao entre os saldos.
//
// A validacao central e saldoPosterior = saldoAnterior +- valor, conforme a
// direcao. Sem ela, um lancamento com saldo posterior errado passa, e a
// reconciliacao so sinaliza a divergencia depois que o dinheiro ja foi
// movimentado -- que e o oposto de detectar o defeito na origem.
func NovoLancamento(
	id Identificador,
	carteira Identificador,
	transacao Identificador,
	direcao Direcao,
	valor money.Money,
	saldoAnterior money.Money,
	saldoPosterior money.Money,
	criadoEm time.Time,
) (Lancamento, error) {
	if direcao != DirecaoDebito && direcao != DirecaoCredito {
		return Lancamento{}, fmt.Errorf("%w: %q", ErrDirecaoInvalida, direcao)
	}
	if !id.Valida() {
		return Lancamento{}, fmt.Errorf("%w: lancamento %v", ErrIdentificadorNaoInicializado, id)
	}
	if !carteira.Valida() {
		return Lancamento{}, fmt.Errorf("%w: carteira %v", ErrIdentificadorNaoInicializado, carteira)
	}
	if !transacao.Valida() {
		return Lancamento{}, fmt.Errorf("%w: transacao %v", ErrIdentificadorNaoInicializado, transacao)
	}
	if err := valor.Validar(); err != nil {
		return Lancamento{}, fmt.Errorf("valor: %w", err)
	}
	// Lancamento de valor zero nao pode existir: ele nao muda o saldo, mas conta
	// como movimentacao na soma do ledger, na contagem de lancamentos da
	// reconciliacao e em qualquer relatorio financeiro. LOSS e uma operacao sem
	// efeito e existe sem lancamento.
	if !valor.IsPositive() {
		return Lancamento{}, fmt.Errorf("%w: lancamento de %s", ErrValorInvalido, valor)
	}
	if err := saldoAnterior.Validar(); err != nil {
		return Lancamento{}, fmt.Errorf("saldo anterior: %w", err)
	}
	if err := saldoPosterior.Validar(); err != nil {
		return Lancamento{}, fmt.Errorf("saldo posterior: %w", err)
	}
	if criadoEm.IsZero() {
		return Lancamento{}, fmt.Errorf("%w: instante de criacao ausente", ErrValorInvalido)
	}
	if valor.Currency() != saldoAnterior.Currency() || valor.Currency() != saldoPosterior.Currency() {
		return Lancamento{}, fmt.Errorf(
			"%w: valor %s, saldo anterior %s e saldo posterior %s",
			ErrMoedaDoLancamento, valor, saldoAnterior, saldoPosterior,
		)
	}

	esperado, err := saldoEsperado(saldoAnterior, valor, direcao)
	if err != nil {
		return Lancamento{}, err
	}
	if !saldoPosterior.Equal(esperado) {
		return Lancamento{}, fmt.Errorf(
			"%w: %s %s sobre %s resulta em %s e nao em %s",
			ErrSaldoPosteriorIncompativel, direcao, valor, saldoAnterior, esperado, saldoPosterior,
		)
	}

	return Lancamento{
		id:             id,
		carteira:       carteira,
		transacao:      transacao,
		direcao:        direcao,
		valor:          valor,
		saldoAnterior:  saldoAnterior,
		saldoPosterior: saldoPosterior,
		criadoEm:       criadoEm,
	}, nil
}

// saldoEsperado calcula o saldo posterior a partir do anterior, do valor e da
// direcao.
//
// Retorna erro quando o resultado seria negativo: um debito que termina em saldo
// negativo nao pode existir, e o erro de construcao e melhor do que um lancamento
// invalido esperando pela constraint.
func saldoEsperado(saldoAnterior money.Money, valor money.Money, direcao Direcao) (money.Money, error) {
	var (
		esperado money.Money
		err      error
	)
	if direcao == DirecaoCredito {
		esperado, err = saldoAnterior.Add(valor)
	} else {
		esperado, err = saldoAnterior.Sub(valor)
	}
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: %w", ErrSaldoPosteriorIncompativel, err)
	}
	if esperado.IsNegative() {
		return money.Money{}, fmt.Errorf(
			"%w: %s %s sobre %s resulta em %s",
			ErrSaldoPosteriorIncompativel, direcao, valor, saldoAnterior, esperado,
		)
	}
	return esperado, nil
}

// ID devolve o identificador do lancamento.
func (l Lancamento) ID() Identificador { return l.id }

// Carteira devolve a carteira do lancamento.
func (l Lancamento) Carteira() Identificador { return l.carteira }

// Transacao devolve a transacao que originou o movimento.
func (l Lancamento) Transacao() Identificador { return l.transacao }

// Direcao devolve o sentido do movimento.
func (l Lancamento) Direcao() Direcao { return l.direcao }

// Valor devolve o valor movimentado.
func (l Lancamento) Valor() money.Money { return l.valor }

// SaldoAnterior devolve o saldo antes do movimento.
func (l Lancamento) SaldoAnterior() money.Money { return l.saldoAnterior }

// SaldoPosterior devolve o saldo depois do movimento.
func (l Lancamento) SaldoPosterior() money.Money { return l.saldoPosterior }

// CriadoEm devolve o instante do lancamento.
func (l Lancamento) CriadoEm() time.Time { return l.criadoEm }

// Valida informa se o lancamento existe.
//
// O valor zero nao e um lancamento. Devolve-lo como valor em vez de erro seria
// a mesma armadilha do Money: um consumidor que ignora o erro terminaria
// gravando uma linha de ledger com valor zero.
func (l Lancamento) Valida() bool {
	return l.id.Valida() && l.carteira.Valida() && l.transacao.Valida() &&
		l.valor.Valida() && l.saldoAnterior.Valida() && l.saldoPosterior.Valida() &&
		(l.direcao == DirecaoDebito || l.direcao == DirecaoCredito) && !l.criadoEm.IsZero()
}

// Sinal devolve +1 para credito e -1 para debito.
//
// Existe para que a soma do ledger e a checagem de reconciliacao usem a direcao
// como numero, e nao como texto comparado em if.
func (l Direcao) Sinal() int64 {
	if l == DirecaoCredito {
		return 1
	}
	return -1
}
