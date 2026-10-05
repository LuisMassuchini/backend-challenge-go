// Package wallet e a carteira do jogador: a raiz do agregado financeiro.
//
// A carteira carrega identidade, jogador, moeda, saldo, versao e instantes, e
// nenhuma movimentacao existe fora dela. Debito e credito sao metodos que
// devolvem uma carteira nova: a entidade e imutavel, e o efeito de uma
// operacao so passa a valer quando o caso de uso decide persistir o valor
// devolvido, dentro da mesma transacao que grava o lancamento no ledger.
//
// A regra que a carteira garante e uma so, e ela e a mais importante do sistema:
// o saldo nunca fica negativo. Isso e garantido aqui, na constraint do banco e
// na operacao que escreve -- tres camadas, porque cada uma delas cobre a falha
// da anterior.
package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
)

// versaoInicial e a versao de uma carteira recem-criada.
//
// O enunciado fixa 1, e nao 0. A diferenca importa para update condicional: com
// versao 0, "nenhuma linha atualizada" e "atualizada de zero para zero" se
// confundem, e um lost update passa por sucesso.
const versaoInicial int64 = 1

// Erros da carteira.
var (
	// ErrSaldoInsuficiente cobre debito maior que o saldo.
	ErrSaldoInsuficiente = errors.New("wallet: saldo insuficiente")
	// ErrMoedaDaCarteira cobre movimentacao em moeda diferente da da carteira.
	ErrMoedaDaCarteira = errors.New("wallet: moeda diferente da da carteira")
	// ErrValorInvalido cobre valor nao inicializado, negativo onde nao cabe, ou
	// instante nao inicializado.
	ErrValorInvalido = errors.New("wallet: valor invalido")
	// ErrEstadoInvalido cobre reidratacao com estado que nao pode existir no banco.
	ErrEstadoInvalido = errors.New("wallet: estado invalido")
)

// Carteira e o agregado financeiro do jogador.
//
// Os campos sao privados e nao ha setter. Fora deste arquivo nao existe caminho
// que mude o saldo sem passar por Debitar ou Creditar, e portanto nao existe
// caminho que produza saldo negativo nem movimentacao sem lancamento.
type Carteira struct {
	id           Identificador
	jogador      Identificador
	saldo        money.Money
	versao       int64
	criadaEm     time.Time
	atualizadaEm time.Time
}

// Nova cria uma carteira com saldo inicial.
//
// O saldo inicial pode ser zero: o enunciado aceita zero na abertura. O que nao
// pode e saldo negativo, e o motivo e o enunciado listar "valor negativo" entre
// as entradas a rejeitar -- uma carteira com saldo negativo na abertura nao tem
// origem contabil.
func Nova(jogador Identificador, saldo money.Money, agora time.Time) (Carteira, error) {
	if !jogador.Valida() {
		return Carteira{}, fmt.Errorf("%w: jogador %v", ErrIdentificadorNaoInicializado, jogador)
	}
	if err := saldo.Validar(); err != nil {
		return Carteira{}, fmt.Errorf("saldo inicial: %w", err)
	}
	if saldo.IsNegative() {
		return Carteira{}, fmt.Errorf("%w: saldo inicial negativo %s", ErrValorInvalido, saldo)
	}
	if agora.IsZero() {
		return Carteira{}, fmt.Errorf("%w: instante de criacao ausente", ErrValorInvalido)
	}

	return Carteira{
		id:           NovoIdentificador(),
		jogador:      jogador,
		saldo:        saldo,
		versao:       versaoInicial,
		criadaEm:     agora,
		atualizadaEm: agora,
	}, nil
}

// Reidratar reconstroi a carteira a partir do que esta no banco.
//
// Nao gera identificador, nao gera versao e nao emite evento. A reidratacao que
// recalcula invariante e reemissao e dupla contagem: o lancamento ja esta no
// ledger, e regra de negocio reexecutada na leitura grava saldo que ninguem
// movementou.
func Reidratar(
	id Identificador,
	jogador Identificador,
	saldo money.Money,
	versao int64,
	criadaEm time.Time,
	atualizadaEm time.Time,
) (Carteira, error) {
	if !id.Valida() {
		return Carteira{}, fmt.Errorf("%w: id %v", ErrIdentificadorNaoInicializado, id)
	}
	if !jogador.Valida() {
		return Carteira{}, fmt.Errorf("%w: jogador %v", ErrIdentificadorNaoInicializado, jogador)
	}
	if err := saldo.Validar(); err != nil {
		return Carteira{}, fmt.Errorf("saldo: %w", err)
	}
	if saldo.IsNegative() {
		return Carteira{}, fmt.Errorf("%w: saldo negativo %s", ErrEstadoInvalido, saldo)
	}
	if versao < versaoInicial {
		return Carteira{}, fmt.Errorf("%w: versao %d", ErrEstadoInvalido, versao)
	}
	if criadaEm.IsZero() {
		return Carteira{}, fmt.Errorf("%w: criado em ausente", ErrEstadoInvalido)
	}
	if atualizadaEm.Before(criadaEm) {
		return Carteira{}, fmt.Errorf("%w: atualizado em %v antes de criado em %v", ErrEstadoInvalido, atualizadaEm, criadaEm)
	}

	return Carteira{
		id:           id,
		jogador:      jogador,
		saldo:        saldo,
		versao:       versao,
		criadaEm:     criadaEm,
		atualizadaEm: atualizadaEm,
	}, nil
}

// ID devolve o identificador da carteira.
func (c Carteira) ID() Identificador { return c.id }

// Jogador devolve o jogador dono da carteira.
func (c Carteira) Jogador() Identificador { return c.jogador }

// Saldo devolve o saldo atual.
func (c Carteira) Saldo() money.Money { return c.saldo }

// Versao devolve a versao do agregado.
func (c Carteira) Versao() int64 { return c.versao }

// CriadaEm devolve o instante de criacao.
func (c Carteira) CriadaEm() time.Time { return c.criadaEm }

// AtualizadaEm devolve o instante da ultima mudanca de saldo.
func (c Carteira) AtualizadaEm() time.Time { return c.atualizadaEm }

// Debitar desconta um valor da carteira e devolve o lancamento correspondente.
//
// O valor devolvido em caso de erro e a carteira de entrada, sem alteracao, e o
// lancamento e o valor zero, que nao existe. Nao e um detalhe: um caso de uso que
// erre o tratamento do erro e persista o resultado assim mesmo gravaria um debito
// sem lancamento, que e exatamente o tipo de movimentacao que o ledger existe
// para impedir -- e a reconciliacao so descobriria a divergencia depois que o
// dinheiro ja tinha sido movimentado.
func (c Carteira) Debitar(transacao Identificador, valor money.Money, agora time.Time) (Carteira, Lancamento, error) {
	if err := c.validarOperacao(transacao, valor, agora); err != nil {
		return c, Lancamento{}, err
	}
	if !valor.IsPositive() {
		return c, Lancamento{}, fmt.Errorf("%w: debito de %s", ErrValorInvalido, valor)
	}
	if valor.Currency() != c.saldo.Currency() {
		return c, Lancamento{}, fmt.Errorf("%w: debito em %s sobre carteira em %s", ErrMoedaDaCarteira, valor, c.saldo.Currency())
	}

	novoSaldo, err := c.saldo.Sub(valor)
	if err != nil {
		return c, Lancamento{}, fmt.Errorf("debito: %w", err)
	}
	// A subtracao acima ja devolveu erro em caso de transbordo. A checagem
	// abaixo cobre o outro lado: um debito que cabe em int64 mas nao cabe no
	// saldo, e que produziria saldo negativo.
	if novoSaldo.IsNegative() {
		return c, Lancamento{}, fmt.Errorf("%w: debito de %s sobre saldo de %s", ErrSaldoInsuficiente, valor, c.saldo)
	}

	lancamento, err := NovoLancamento(
		NovoIdentificador(), c.id, transacao, DirecaoDebito,
		valor, c.saldo, novoSaldo, agora,
	)
	if err != nil {
		return c, Lancamento{}, fmt.Errorf("lancamento do debito: %w", err)
	}

	return c.comSaldo(novoSaldo, agora), lancamento, nil
}

// Creditar soma um valor na carteira e devolve o lancamento correspondente.
//
// Credito de valor negativo e recusado. Ele seria um debito escrito de outro
// jeito, e ter dois jeitos de mover dinheiro para o mesmo lado e o caminho mais
// curto para um lancamento com direcao errada no ledger.
func (c Carteira) Creditar(transacao Identificador, valor money.Money, agora time.Time) (Carteira, Lancamento, error) {
	if err := c.validarOperacao(transacao, valor, agora); err != nil {
		return c, Lancamento{}, err
	}
	if !valor.IsPositive() {
		return c, Lancamento{}, fmt.Errorf("%w: credito de %s", ErrValorInvalido, valor)
	}
	if valor.Currency() != c.saldo.Currency() {
		return c, Lancamento{}, fmt.Errorf("%w: credito em %s sobre carteira em %s", ErrMoedaDaCarteira, valor, c.saldo.Currency())
	}

	novoSaldo, err := c.saldo.Add(valor)
	if err != nil {
		return c, Lancamento{}, fmt.Errorf("credito: %w", err)
	}

	lancamento, err := NovoLancamento(
		NovoIdentificador(), c.id, transacao, DirecaoCredito,
		valor, c.saldo, novoSaldo, agora,
	)
	if err != nil {
		return c, Lancamento{}, fmt.Errorf("lancamento do credito: %w", err)
	}

	return c.comSaldo(novoSaldo, agora), lancamento, nil
}

// validarOperacao confere o que toda movimentacao precisa ter.
func (c Carteira) validarOperacao(transacao Identificador, valor money.Money, agora time.Time) error {
	if !c.id.Valida() {
		return fmt.Errorf("%w: carteira %v", ErrIdentificadorNaoInicializado, c.id)
	}
	if !transacao.Valida() {
		return fmt.Errorf("%w: transacao %v", ErrIdentificadorNaoInicializado, transacao)
	}
	if err := valor.Validar(); err != nil {
		return fmt.Errorf("valor: %w", err)
	}
	if agora.IsZero() {
		return fmt.Errorf("%w: instante da operacao ausente", ErrValorInvalido)
	}
	return nil
}

// comSaldo devolve a carteira com o novo saldo.
//
// A versao ainda nao e incrementada aqui: quem decide a versao e o commit que
// acompanha a regra de versionamento, e um incremento cego em todo metodo de
// movimentacao faria uma operacao sem efeito financeiro -- como LOSS -- subir a
// versao sozinha.
func (c Carteira) comSaldo(saldo money.Money, agora time.Time) Carteira {
	alterada := c
	alterada.saldo = saldo
	alterada.atualizadaEm = agora
	return alterada
}
