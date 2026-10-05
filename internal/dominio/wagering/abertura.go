package wagering

import (
	"errors"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// Abertura e o conjunto de dados da abertura interna de carteira.
type Abertura struct {
	// Carteira e a carteira que esta sendo aberta. A identidade interna e
	// estavel e ja existe quando a abertura acontece.
	Carteira wallet.Identificador
	// Jogador e o dono da carteira.
	Jogador wallet.Identificador
	// Valor e o saldo inicial, sempre maior que zero.
	Valor money.Money
	// CriadaEm e o instante unico da abertura, que tambem e o de atualizacao.
	CriadaEm time.Time
}

// Erros da origem interna.
var (
	// ErrAberturaSemSaldo cobre abertura com saldo zero.
	//
	// O enunciado e explicito: saldo inicial zero nao cria OPENING, nem lancamento,
	// nem evento. Criar uma transacao sem efeito aqui daria um OPENING por jogador
	// sem nenhum movimento por tras, e o ledger passaria a contar uma operacao que
	// nao aconteceu.
	ErrAberturaSemSaldo = errors.New("wagering: abertura exige saldo maior que zero")

	// ErrTipoNaoSuportado cobre OPENING recebido de origem externa.
	ErrTipoNaoSuportado = errors.New("wagering: tipo nao suportado na origem externa")
)

// FalhaDeRegra e uma recusa com codigo estavel.
//
// Existe separada do erro de validacao de entrada porque as duas coisas levam a
// respostas diferentes. FalhaDeRegra e uma decisao do dominio sobre uma operacao
// bem formada e recusada por regra; erro de validacao e entrada malformada, que o
// cliente corrige antes de tentar de novo.
type FalhaDeRegra struct {
	// Codigo e o codigo estavel, parte do contrato com o provedor.
	Codigo CodigoFalha
	// Motivo explica a recusa em texto, para log. Nao substitui o codigo.
	Motivo string
}

func (f *FalhaDeRegra) Error() string {
	return fmt.Sprintf("regra de negocio recusou a operacao: %s (%s)", f.Codigo, f.Motivo)
}

// Is permite comparar a falha pelo codigo.
//
// A comparacao e por codigo e nao por ponteiro: o chamador do caso de uso quer
// saber se pode devolver conflito com este codigo no corpo, e nao se o erro e o
// mesmo objeto que o dominio construiu.
func (f *FalhaDeRegra) Is(alvo error) bool {
	outro, ok := alvo.(*FalhaDeRegra)
	return ok && outro.Codigo == f.Codigo
}

// AbrirCarteira cria a transacao de abertura interna.
//
// A abertura nasce em PROCESSED, e nao em PENDING, porque nao ha nada a esperar:
// nao existe referencia externa e nao existe dependencia de infraestrutura alem da
// propria carteira. O enunciado pede que a abertura, o lancamento de credito e os
// eventos de origem interna nascem no mesmo commit da carteira.
//
// Criar em PENDING e depender de um segundo passo abriria uma janela em que a
// carteira existe sem a transacao que a originou -- e a reconciliacao, executada
// nesse intervalo, veria saldo sem lancamento.
func AbrirCarteira(a Abertura) (Transacao, error) {
	if !a.Carteira.Valida() {
		return Transacao{}, fmt.Errorf("%w: carteira ausente", ErrRegistroInvalido)
	}
	if !a.Jogador.Valida() {
		return Transacao{}, fmt.Errorf("%w: jogador ausente", ErrRegistroInvalido)
	}
	if err := a.Valor.Validar(); err != nil {
		return Transacao{}, fmt.Errorf("valor: %w", err)
	}
	if a.CriadaEm.IsZero() {
		return Transacao{}, fmt.Errorf("%w: instante de criacao ausente", ErrRegistroInvalido)
	}
	if !a.Valor.IsPositive() {
		return Transacao{}, fmt.Errorf("%w: %s", ErrAberturaSemSaldo, a.Valor)
	}

	return Transacao{
		id:           wallet.NovoIdentificador(),
		carteira:     a.Carteira,
		jogador:      a.Jogador,
		tipo:         TipoAbertura,
		valor:        a.Valor,
		estado:       EstadoProcessado,
		resultado:    a.Valor,
		criadaEm:     a.CriadaEm,
		atualizadaEm: a.CriadaEm,
	}, nil
}
