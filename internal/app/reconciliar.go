package app

import (
	"context"
	"fmt"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// RequisicaoReconciliacao pede a conferencia de uma carteira.
type RequisicaoReconciliacao struct {
	// Carteira e a carteira a reconciliar.
	Carteira wallet.Identificador
}

// RespostaReconciliacao e o resultado da conferencia.
//
// Carrega as duas fontes lado a lado de proposito: quem le o resultado precisa ver
// o saldo gravado e o saldo que o ledger implica, e nao apenas um veredito. Um
// veredito sem as duas fontes obriga quem investiga a reconciliacao a abrir o banco
// de novo.
type RespostaReconciliacao struct {
	// Carteira e a carteira conferida.
	Carteira wallet.Carteira
	// SaldoGravado e o saldo na tabela de carteiras.
	SaldoGravado money.Money
	// SaldoDoLedger e a soma dos lancamentos da carteira.
	SaldoDoLedger money.Money
	// Lancamentos e quantos lancamentos o ledger tem.
	Lancamentos int64
	// Divergente informa se as duas fontes nao concordam.
	Divergente bool
	// Diferenca e SaldoGravado menos SaldoDoLedger. Zero quando convergente.
	Diferenca money.Money
}

// Reconciliar compara o saldo gravado da carteira com a soma do ledger.
//
// E uma leitura, e nao um reparo. Reparar exigiria decidir de qual lado esta o
// erro, e essa decisao nao e do processo de reconciliacao: pode ser que o lancamento
// esteja errado, e nao o saldo. Um reconciliador que corrige sozinho pode aumentar
// um erro em vez de relatar o que encontrou.
func Reconciliar(ctx context.Context, s Servicos, ator Ator, req RequisicaoReconciliacao) (RespostaReconciliacao, error) {
	if err := s.verifica("unidade", "carteiras", "ledger"); err != nil {
		return RespostaReconciliacao{}, err
	}
	if !ator.Interno() {
		return RespostaReconciliacao{}, fmt.Errorf(
			"%w: reconciliacao e do cliente interno", ErrNaoAutorizado)
	}
	if !ator.TemEscopo(EscopoReconciliacao) {
		return RespostaReconciliacao{}, fmt.Errorf(
			"%w: escopo %s ausente", ErrNaoAutorizado, EscopoReconciliacao)
	}
	if !req.Carteira.Valida() {
		return RespostaReconciliacao{}, fmt.Errorf("%w: carteira ausente", ErrRequisicaoInvalida)
	}

	var resposta RespostaReconciliacao

	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		// A leitura e sem lock de proposito. A reconciliacao nao altera nada, e
		// segurar o lock da carteira transformaria um diagnostico em mais um
		// escritor na fila de quem quer debitar.
		carteira, err := s.Carteiras.Ler(ctx, q, req.Carteira)
		if err != nil {
			return fmt.Errorf("carteira: %w", err)
		}

		saldo, lancamentos, err := s.Ledger.SomarPorCarteira(ctx, q, req.Carteira)
		if err != nil {
			return err
		}

		resposta = RespostaReconciliacao{
			Carteira:     carteira,
			SaldoGravado: carteira.Saldo(),
			Lancamentos:  lancamentos,
		}

		// Sem lancamento, o ledger nao diz nada sobre o saldo. A carteira vale como
		// esta, e marcar divergencia aqui acusaria uma carteira recem-criada com saldo
		// zero de um erro que nao existe.
		if lancamentos == 0 {
			return nil
		}

		resposta.SaldoDoLedger = saldo
		if saldo.Equal(carteira.Saldo()) {
			return nil
		}

		resposta.Divergente = true
		// Sub devolve erro quando as moedas nao batem, e nesse caso a divergencia
		// e exatamente o achado: um lancamento em moeda errada na carteira.
		diferenca, err := carteira.Saldo().Sub(saldo)
		if err != nil {
			return fmt.Errorf("diferenca entre saldo e ledger: %w", err)
		}
		resposta.Diferenca = diferenca
		return nil
	})
	if err != nil {
		return RespostaReconciliacao{}, err
	}

	// A divergencia vai para o log como erro, e o enunciado pede isso: ela aparece na
	// resposta, no log e em uma metrica. Sem a linha de log, uma carteira corrompida
	// por fora do processo so apareceria para quem Lembrasse de rodar a reconciliacao.
	//
	// A diferenca entra como numero de centavos e nao como `Money`: o log nao carrega
	// valor monetario formatado, e o par de unidades minimas e o que o alerta precisa
	// para classificar a gravidade sem obriga quem le a converter.
	if resposta.Divergente {
		obs.Log(obs.De(ctx).ComCarteira(resposta.Carteira.ID().String())).
			Error("saldo da carteira diverge do ledger",
				"centavos_gravado", resposta.SaldoGravado.Amount(),
				"centavos_ledger", resposta.SaldoDoLedger.Amount(),
				"lancamentos", resposta.Lancamentos,
				"moeda", string(resposta.SaldoGravado.Currency()),
			)
	}

	return resposta, nil
}
