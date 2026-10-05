package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/eventos"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// FalhaDeRegra e uma recusa com codigo estavel.
//
// Difere de pg.ErrInvarianteViolada porque e uma decisao de negocio, e nao um
// acidente. E o que o provedor recebe no corpo da resposta e o que a auditoria
// registra.
type FalhaDeRegra struct {
	// Codigo e o codigo estavel do dominio.
	Codigo wagering.CodigoFalha
	// Motivo explica a recusa em texto, para log.
	Motivo string
}

func (f *FalhaDeRegra) Error() string {
	return fmt.Sprintf("regra de negocio recusou a operacao: %s (%s)", f.Codigo, f.Motivo)
}

// Is compara a falha pelo codigo, e nao por ponteiro: o chamador quer saber qual
// codigo pode devolver na resposta, e nao se o erro e o mesmo objeto que o dominio
// construiu.
func (f *FalhaDeRegra) Is(alvo error) bool {
	outro, ok := alvo.(*FalhaDeRegra)
	return ok && outro.Codigo == f.Codigo
}

// traduzirFalhaDeRegra converte a recusa do dominio em FalhaDeRegra do caso de uso.
//
// A traducao e aqui, e nao no dominio, porque o dominio nao conhece o contrato de
// erro da camada de aplicacao. O que atravessa a fronteira e o codigo, que ja e
// estavel e documentado.
func traduzirFalhaDeRegra(err error) error {
	if err == nil {
		return nil
	}

	var recusada *wagering.FalhaDeRegra
	if errors.As(err, &recusada) {
		return &FalhaDeRegra{Codigo: recusada.Codigo, Motivo: recusada.Motivo}
	}

	// Saldo insuficiente do agregado da carteira vira recusa de regra com o codigo
	// de BET sem saldo, e nao erro de infraestrutura: e o provedor que pode corrigir
	// a operacao, esperando deposito.
	if errors.Is(err, wallet.ErrSaldoInsuficiente) {
		return &FalhaDeRegra{
			Codigo: wagering.CodigoFalhaSemSaldo,
			Motivo: "saldo insuficiente para a operacao",
		}
	}

	return err
}

// validarAtorOperacao confere a autorizacao e a identidade do comando.
//
// A segunda condicao e a que importa para o enunciado: a identidade autenticada
// determina o providerId autorizado, e um provedor nao escreve em nome de outro.
// Sem esta comparacao, um token valido de provider-a escreveria com
// providerId = provider-b, e o registro aceitaria.
func validarAtorOperacao(ator Ator, req RequisicaoOperacao) error {
	if !ator.EhProvedor() {
		return fmt.Errorf("%w: operacao externa exige provedor", ErrNaoAutorizado)
	}
	if !ator.TemEscopo(EscopoOperacoes) {
		return fmt.Errorf("%w: escopo %s ausente", ErrNaoAutorizado, EscopoOperacoes)
	}
	if req.Provedor.String() != ator.Provedor {
		return fmt.Errorf("%w: comando diz %q e o ator e %q", ErrProvedorDivergente, req.Provedor, ator.Provedor)
	}
	return nil
}

// validarOperacao confere o payload antes de tocar o banco.
func validarOperacao(req RequisicaoOperacao) error {
	if !req.Provedor.Valida() {
		return fmt.Errorf("%w: provedor ausente", ErrRequisicaoInvalida)
	}
	if !req.TransacaoExterna.Valida() {
		return fmt.Errorf("%w: transacao externa ausente", ErrRequisicaoInvalida)
	}
	if !req.Chave.Valida() {
		return fmt.Errorf("%w: chave de idempotencia ausente", ErrRequisicaoInvalida)
	}
	// O fingerprint e obrigatorio porque e ele que distingue replay de conflito.
	// Sem ele, a mesma chave com conteudo diferente passaria como replay.
	if !req.Fingerprint.Valida() {
		return fmt.Errorf("%w: fingerprint do conteudo ausente", ErrRequisicaoInvalida)
	}
	if !req.Carteira.Valida() {
		return fmt.Errorf("%w: carteira ausente", ErrRequisicaoInvalida)
	}
	if err := garantirJogador(req.Jogador); err != nil {
		return err
	}
	if !req.Rodada.Valida() {
		return fmt.Errorf("%w: rodada ausente", ErrRequisicaoInvalida)
	}
	if !req.Jogo.Valida() {
		return fmt.Errorf("%w: jogo ausente", ErrRequisicaoInvalida)
	}
	if !req.Tipo.Valido() {
		return fmt.Errorf("%w: tipo %q", ErrRequisicaoInvalida, req.Tipo)
	}
	if err := req.Valor.Validar(); err != nil {
		return fmt.Errorf("%w: valor: %v", ErrRequisicaoInvalida, err)
	}
	return nil
}

// registrarEventoProcessada grava o evento de operacao concluida.
func registrarEventoProcessada(
	ctx context.Context,
	s Servicos,
	q pg.Querente,
	transacao wagering.Transacao,
	resultado money.Money,
	correlacao string,
	agora time.Time,
) error {
	// Provedor e identificador externo vem da TRANSACAO, e nao do comando. A
	// retomada de uma pendencia nao tem comando: a linha ja estava no banco, e o
	// evento precisa da mesma identidade que a transacao gravou. Ler do comando
	// deixaria o evento sem provedor e a operacao inteira falhando por um envelope.
	evento, err := eventos.NovaTransacaoProcessada(eventos.DadosTransacaoProcessada{
		Transacao:  transacao.ID(),
		Provedor:   string(transacao.Provedor()),
		Externo:    string(transacao.TransacaoExterna()),
		Carteira:   transacao.Carteira(),
		Tipo:       string(transacao.Tipo()),
		Resultado:  resultado,
		Correlacao: correlacao,
		OcorridoEm: agora,
	})
	if err != nil {
		return fmt.Errorf("evento de operacao processada: %w", err)
	}
	return s.Outbox.Inserir(ctx, q, evento)
}

// registrarEventoRejeicao grava o evento de operacao recusada.
func registrarEventoRejeicao(
	ctx context.Context,
	s Servicos,
	q pg.Querente,
	transacao wagering.Transacao,
	correlacao string,
	agora time.Time,
) error {
	evento, err := eventos.NovaTransacaoRejeitada(eventos.DadosTransacaoRejeitada{
		Transacao:  transacao.ID(),
		Provedor:   string(transacao.Provedor()),
		Externo:    string(transacao.TransacaoExterna()),
		Carteira:   transacao.Carteira(),
		Tipo:       string(transacao.Tipo()),
		Valor:      transacao.Valor(),
		Falha:      string(transacao.CodigoFalha()),
		Correlacao: correlacao,
		OcorridoEm: agora,
	})
	if err != nil {
		return fmt.Errorf("evento de operacao rejeitada: %w", err)
	}
	return s.Outbox.Inserir(ctx, q, evento)
}

// registrarEventoDeSaldo grava o evento de mudanca efetiva de saldo.
//
// Nao e gravado para LOSS: nao houve mudanca, e o enunciado e explicito que LOSS
// produz WagerTransactionProcessed sem WalletBalanceChanged.
func registrarEventoDeSaldo(
	ctx context.Context,
	s Servicos,
	q pg.Querente,
	lancamento wallet.Lancamento,
	transacao wagering.Transacao,
	versaoCarteira int64,
	correlacao string,
	agora time.Time,
) error {
	evento, err := eventos.NovaSaldoAlterado(eventos.DadosSaldoAlterado{
		Transacao:      transacao.ID(),
		Carteira:       lancamento.Carteira(),
		Direcao:        string(lancamento.Direcao()),
		Valor:          lancamento.Valor(),
		SaldoAnterior:  lancamento.SaldoAnterior(),
		SaldoPosterior: lancamento.SaldoPosterior(),
		VersaoCarteira: versaoCarteira,
		Correlacao:     correlacao,
		Causa:          transacao.ID(),
		OcorridoEm:     agora,
	})
	if err != nil {
		return fmt.Errorf("evento de mudanca de saldo: %w", err)
	}
	return s.Outbox.Inserir(ctx, q, evento)
}
