package app

import (
	"context"
	"fmt"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// RequisicaoLedger pede uma pagina do ledger.
type RequisicaoLedger struct {
	// Carteira e a carteira cujo ledger sera lido.
	Carteira wallet.Identificador
}

// LerCarteira devolve a carteira.
//
// A leitura nao exige escopo proprio: qualquer ator autenticado que alcança a rota
// pode ver a carteira. O enunciado isola provedor por escopo de operacao, e nao
// por escopo de leitura, entao acrescentar um escopo aqui seria inventar uma
// restricao que o contrato nao pede.
func LerCarteira(ctx context.Context, s Servicos, _ Ator, id wallet.Identificador) (wallet.Carteira, error) {
	if err := s.verifica("unidade", "carteiras"); err != nil {
		return wallet.Carteira{}, err
	}
	if !id.Valida() {
		return wallet.Carteira{}, fmt.Errorf("%w: carteira ausente", ErrRequisicaoInvalida)
	}

	var carteira wallet.Carteira
	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		var err error
		carteira, err = s.Carteiras.Ler(ctx, q, id)
		return err
	})
	if err != nil {
		return wallet.Carteira{}, err
	}
	return carteira, nil
}

// ListarLedger devolve uma pagina do ledger da carteira.
func ListarLedger(
	ctx context.Context,
	s Servicos,
	_ Ator,
	req RequisicaoLedger,
	cursor pg.Cursor,
	limite int,
) (pg.PaginaE, error) {
	if err := s.verifica("unidade", "carteiras", "ledger"); err != nil {
		return pg.PaginaE{}, err
	}
	if !req.Carteira.Valida() {
		return pg.PaginaE{}, fmt.Errorf("%w: carteira ausente", ErrRequisicaoInvalida)
	}

	var pagina pg.PaginaE
	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		var err error
		pagina, err = s.Ledger.Listar(ctx, q, req.Carteira, cursor, limite)
		return err
	})
	if err != nil {
		return pg.PaginaE{}, err
	}
	return pagina, nil
}

// LerTransacao devolve a transacao pelo identificador interno.
func LerTransacao(
	ctx context.Context,
	s Servicos,
	_ Ator,
	id wallet.Identificador,
) (wagering.Transacao, error) {
	if err := s.verifica("unidade", "transacoes"); err != nil {
		return wagering.Transacao{}, err
	}
	if !id.Valida() {
		return wagering.Transacao{}, fmt.Errorf("%w: transacao ausente", ErrRequisicaoInvalida)
	}

	var transacao wagering.Transacao
	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		var err error
		transacao, err = s.Transacoes.BuscarPorID(ctx, q, id)
		return err
	})
	if err != nil {
		return wagering.Transacao{}, err
	}
	return transacao, nil
}

// LerTransacaoDoProvedor devolve a transacao pelo par provedor e identificador
// externo.
//
// O par e a identidade da operacao no provedor, e nao o identificador interno: e o
// que o provedor tem em maos quando quer saber o que aconteceu com o que enviou.
func LerTransacaoDoProvedor(
	ctx context.Context,
	s Servicos,
	_ Ator,
	provedor wagering.Provedor,
	externa wagering.Externo,
) (wagering.Transacao, error) {
	if err := s.verifica("unidade", "transacoes"); err != nil {
		return wagering.Transacao{}, err
	}
	if !provedor.Valida() {
		return wagering.Transacao{}, fmt.Errorf("%w: provedor ausente", ErrRequisicaoInvalida)
	}
	if !externa.Valida() {
		return wagering.Transacao{}, fmt.Errorf("%w: identificador externo ausente", ErrRequisicaoInvalida)
	}

	var transacao wagering.Transacao
	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		var err error
		transacao, err = s.Transacoes.BuscarPorProvedorEExterno(ctx, q, provedor, externa)
		return err
	})
	if err != nil {
		return wagering.Transacao{}, err
	}
	return transacao, nil
}
