package wagering

import (
	"errors"
	"fmt"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// Efeito e o que a operacao faz com o saldo da carteira.
//
// E uma decisao do dominio e nao do caso de uso porque ela depende apenas do
// tipo: BET debita, WIN credita, LOSS nao faz nada, REFUND credita, ROLLBACK faz
// o contrario da referenciada. Deixar essa tabela na camada de aplicacao
// significaria duas copias da regra -- uma em Go e outra em migracao -- e o
// primeiro lugar a divergir seria o ledger.
type Efeito string

const (
	// EfeitoDebito desconta o valor da carteira.
	EfeitoDebito Efeito = "DEBIT"
	// EfeitoCredito soma o valor na carteira.
	EfeitoCredito Efeito = "CREDIT"
	// EfeitoNenhum nao movimenta a carteira. E o caso de LOSS: a operacao
	// existe, e confirmada e auditada, mas nao produz lancamento e nao altera a
	// versao da carteira.
	EfeitoNenhum Efeito = "NONE"
	// EfeitoInverso desfaz integralmente a operacao referenciada. So ROLLBACK o
	// produz, e o sentido concreto depende do tipo da referencia: o contrario de
	// um BET e debito, e o contrario de um WIN e credito.
	EfeitoInverso Efeito = "INVERSE"
)

// Efeito devolve o efeito financeiro do tipo.
func (t Tipo) Efeito() Efeito {
	switch t {
	case TipoBET:
		return EfeitoDebito
	case TipoWIN, TipoREFUND, TipoAbertura:
		return EfeitoCredito
	case TipoLOSS:
		return EfeitoNenhum
	case TipoROLLBACK:
		return EfeitoInverso
	}
	return EfeitoNenhum
}

// ErrValorInvalido cobre valor que viola a politica de zero do tipo.
//
// Separate de FalhaDeRegra porque nao e escolha do dominio: e o dado enviado que
// nao pode ser aceito. Ainda assim carrega um CodigoFalha, porque a resposta ao
// provedor precisa ser estavel tambem nesse caso, e o provedor corrige o valor e
// reenvia.
var ErrValorInvalido = errors.New("wagering: valor viola a politica do tipo")

// ErrMoedaDaCarteira cobre valor em moeda diferente da da carteira.
//
// Reaproveita o erro do agregado da carteira em vez de criar um igual: quem
// trata o erro na borda precisa tratar uma coisa so.
var ErrMoedaDaCarteira = wallet.ErrMoedaDaCarteira

// ValidarValor aplica a politica de valor do tipo.
func (t Transacao) ValidarValor() error {
	return validarPoliticaDeValor(t.tipo, t.valor)
}

// validarPoliticaDeValor e a politica de zero por tipo.
//
// A politica e oposta em LOSS: BET, WIN, REFUND e ROLLBACK exigem valor maior que
// zero, e LOSS exige zero exato. Um LOSS com valor maior que zero seria um
// credito sem lancamento; uma aposta com valor zero seria um lancamento de valor
// zero, que o dominio da carteira recusa.
//
// O valor negativo tambem e recusado aqui. Money aceita negativo porque o dominio
// precisa de diferenca, e a entrada externa nao e diferenca.
//
// A politica e aplicada na entrada, e nao depois. Operacao com valor invalido
// recusada tarde deixa de ser problema de dado e vira registro duravel em PENDING
// que outra instancia precisa rejeitar -- e o provedor recebe o codigo de falha
// tarde demais para distinguir entrada incorreta de indisponibilidade.
func validarPoliticaDeValor(tipo Tipo, valor money.Money) error {
	if !valor.Valida() {
		return fmt.Errorf("%w: valor nao inicializado", ErrValorInvalido)
	}

	if tipo == TipoLOSS {
		if !valor.IsZero() {
			return fmt.Errorf("%w: %w", ErrValorInvalido, &FalhaDeRegra{
				Codigo: CodigoFalhaValorDeZeroEsperado,
				Motivo: fmt.Sprintf("LOSS exige valor 0.00 e recebeu %s", valor),
			})
		}
		return nil
	}

	if !valor.IsPositive() {
		return fmt.Errorf("%w: %w", ErrValorInvalido, &FalhaDeRegra{
			Codigo: CodigoFalhaValorNaoPositivo,
			Motivo: fmt.Sprintf("%s exige valor maior que zero e recebeu %s", tipo, valor),
		})
	}
	return nil
}

// ValidarMoedaDaCarteira exige que o valor esteja na moeda da carteira.
//
// Vale inclusive para LOSS, que nao movimenta saldo. Um LOSS em moeda que a
// carteira nao tem ficaria registrado com uma moeda inexistente e, como nao ha
// lancamento, nada denunciaria a inconsistencia.
func (t Transacao) ValidarMoedaDaCarteira(c wallet.Carteira) error {
	if err := c.Saldo().Validar(); err != nil {
		return fmt.Errorf("carteira: %w", err)
	}
	if !t.valor.Valida() {
		return fmt.Errorf("%w: valor nao inicializado", ErrValorInvalido)
	}
	if t.valor.Currency() != c.Saldo().Currency() {
		return fmt.Errorf(
			"%w: %s em %s sobre carteira em %s",
			ErrMoedaDaCarteira, t.tipo, t.valor.Currency(), c.Saldo().Currency(),
		)
	}
	return nil
}
