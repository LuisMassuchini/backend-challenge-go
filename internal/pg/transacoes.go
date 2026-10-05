package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// Erros de conclusao de transacao.
//
// ErrConclusaoImpossivel e o que distingue "a transacao ja estava terminal" de
// "a transacao nao existe". Os dois produzem UPDATE com zero linhas, e a diferenca
// importa: o primeiro e uma corrida benigna entre workers, e o segundo e um bug.
var (
	// ErrEstadoInvalido cobre estado ou resultado que nao pode ser gravado.
	ErrEstadoInvalido = errors.New("pg: estado ou resultado invalido para conclusao")
	// ErrConclusaoImpossivel cobre UPDATE com zero linhas em transacao terminal.
	ErrConclusaoImpossivel = errors.New("pg: conclusao impossivel, a transacao ja e terminal")
)

// RepositorioTransacoes le e escreve a transacao de apostas.
//
// E o repositorio da idempotencia. Toda a garantia de "a mesma operacao nao se
// aplica duas vezes" converge para os dois indices unicos desta tabela, e nao para
// uma verificacao no codigo: verificacao no codigo protege o caminho que a
// verificou.
type RepositorioTransacoes struct{}

// NovaRepositorioTransacoes constroi o repositorio.
func NovaRepositorioTransacoes() *RepositorioTransacoes { return &RepositorioTransacoes{} }

// sqlTransacao e a projecao da transacao.
const sqlTransacao = `
	SELECT id, provider_id, external_transaction_id, idempotency_key, content_hash,
	       wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
	       reference_external_id, reference_internal_id, state, failure_code,
	       result_amount, result_currency, created_at, updated_at
	  FROM wager_transactions`

// Inserir grava a transacao.
//
// O INSERT e um INSERT comum, sem ON CONFLICT: quem chama precisa saber que houve
// conflito, e ON CONFLICT DO NOTHING esconde o conflito devolvendo "nada
// aconteceu". A traducao para ErrConflitoDeChave acontece aqui, e nao no chamador.
func (r RepositorioTransacoes) Inserir(ctx context.Context, q Querente, t wagering.Transacao) error {
	if !t.Valida() {
		return fmt.Errorf("pg: transacao invalida")
	}

	_, err := q.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, provider_id, external_transaction_id, idempotency_key, content_hash,
			wallet_id, player_id, round_id, game_id, kind, money_amount, currency,
			reference_external_id, state, failure_code,
			result_amount, result_currency, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $18)`,
		t.ID().UUID(),
		ouNulo(t.Provedor().Valida(), t.Provedor()),
		ouNulo(t.TransacaoExterna().Valida(), t.TransacaoExterna()),
		ouNulo(t.ChaveIdempotencia().Valida(), t.ChaveIdempotencia()),
		ouNulo(t.HashConteudo().Valida(), t.HashConteudo()),
		t.Carteira().UUID(),
		t.Jogador().UUID(),
		ouNulo(t.Rodada().Valida(), t.Rodada()),
		ouNulo(t.Jogo().Valida(), t.Jogo()),
		string(t.Tipo()),
		t.Valor().Amount(),
		string(t.Valor().Currency()),
		ouNulo(t.Referencia().Externa.Valida(), t.Referencia().Externa),
		string(t.Estado()),
		ouNuloTexto(t.CodigoFalha().Presente(), string(t.CodigoFalha())),
		resultadoAmount(t),
		resultadoMoeda(t),
		t.CriadaEm(),
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: insercao da transacao: %w", err))
	}
	return nil
}

// resultadoAmount devolve o resultado persistido, ou nil quando a transacao ainda
// nao terminou.
//
// A coluna e NULL em estado nao terminal porque o CHECK de coerencia exige
// resultado apenas em PROCESSED. E o que permite gravar uma abertura -- que ja nasce
// PROCESSED, com o resultado -- em um unico INSERT, como o enunciado pede.
func resultadoAmount(t wagering.Transacao) any {
	if t.Estado() != wagering.EstadoProcessado {
		return nil
	}
	if !t.Resultado().Valida() {
		return nil
	}
	return t.Resultado().Amount()
}

// resultadoMoeda devolve a moeda do resultado, ou nil quando nao ha resultado.
func resultadoMoeda(t wagering.Transacao) any {
	if t.Estado() != wagering.EstadoProcessado {
		return nil
	}
	if !t.Resultado().Valida() {
		return nil
	}
	return string(t.Resultado().Currency())
}

// BuscarPorChave devolve a transacao com aquela chave de idempotencia.
func (r RepositorioTransacoes) BuscarPorChave(
	ctx context.Context,
	q Querente,
	chave wagering.Chave,
) (wagering.Transacao, error) {
	return r.buscar(ctx, q, sqlTransacao+` WHERE idempotency_key = $1`, chave)
}

// BuscarPorProvedorEExterno devolve a transacao pelo par que o provedor usa para
// identificar a operacao.
//
// E o segundo indice de idempotencia, e ele pega o caso que o primeiro nao pega: a
// mesma operacao financeira reenviada com outra chave.
func (r RepositorioTransacoes) BuscarPorProvedorEExterno(
	ctx context.Context,
	q Querente,
	provedor wagering.Provedor,
	externo wagering.Externo,
) (wagering.Transacao, error) {
	return r.buscar(ctx, q,
		sqlTransacao+` WHERE provider_id = $1 AND external_transaction_id = $2`,
		provedor, externo)
}

// BuscarPorID devolve a transacao pelo identificador interno.
func (r RepositorioTransacoes) BuscarPorID(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
) (wagering.Transacao, error) {
	return r.buscar(ctx, q, sqlTransacao+` WHERE id = $1`, id.UUID())
}

// buscar executa a consulta e converte a linha.
func (r RepositorioTransacoes) buscar(
	ctx context.Context,
	q Querente,
	consulta string,
	args ...any,
) (wagering.Transacao, error) {
	linha := q.QueryRow(ctx, consulta, args...)
	t, err := scannerTransacao(linha)
	if err != nil {
		return wagering.Transacao{}, err
	}
	return t, nil
}

// ResultadoGravado e o que o replay devolve.
//
// E o resultado persistido, e nao o saldo atual da carteira. Recalcular o saldo no
// replay devolveria a resposta de hoje para uma operacao de ontem, e o provedor
// veria um numero diferente do que viu na primeira vez.
type ResultadoGravado struct {
	// Estado e o estado terminal ja persistido.
	Estado wagering.Estado
	// Resultado e o saldo devolvido no processamento original, se houver.
	Resultado money.Money
	// CodigoFalha e o motivo da rejeicao ou da falha, se houver.
	CodigoFalha wagering.CodigoFalha
}

// Concluir grava o estado terminal e o resultado original.
//
// Estado terminal nao volta: a clausula WHERE state IN ('PENDING',
// 'PENDING_REFERENCE') faz a propria base recusar a segunda conclusao, e o UPDATE
// que nao atinge linha devolve ErrEstadoTerminalConclusao. E o que impede que um
// worker reprocesse uma transacao que ja terminou, que e o caminho mais curto
// para movimentacao duplicada.
func (r RepositorioTransacoes) Concluir(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
	resultado ResultadoGravado,
) error {
	if !id.Valida() {
		return fmt.Errorf("pg: transacao sem id")
	}
	if !resultado.Estado.Valido() || !resultado.Estado.Terminal() {
		return fmt.Errorf("%w: %q nao e terminal", ErrEstadoInvalido, resultado.Estado)
	}
	if resultado.Estado == wagering.EstadoProcessado && !resultado.Resultado.Valida() {
		return fmt.Errorf("%w: processada sem resultado", ErrEstadoInvalido)
	}
	if resultado.Estado != wagering.EstadoProcessado && !resultado.CodigoFalha.Presente() {
		return fmt.Errorf("%w: terminal sem codigo de falha", ErrEstadoInvalido)
	}

	var (
		amount any
		moeda  any
		codigo any
	)
	if resultado.Estado == wagering.EstadoProcessado {
		amount = resultado.Resultado.Amount()
		moeda = string(resultado.Resultado.Currency())
	}
	if resultado.CodigoFalha.Presente() {
		codigo = string(resultado.CodigoFalha)
	}

	tag, err := q.Exec(ctx, `
		UPDATE wager_transactions
		   SET state = $2,
		       result_amount = $3,
		       result_currency = $4,
		       failure_code = $5,
		       updated_at = $6
		 WHERE id = $1
		   AND state IN ('PENDING', 'PENDING_REFERENCE')`,
		id.UUID(), string(resultado.Estado), amount, moeda, codigo, time.Now().UTC(),
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: conclusao da transacao: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: transacao %s nao esta em estado nao terminal", ErrConclusaoImpossivel, id)
	}
	return nil
}

// MarcarPendentePorReferencia leva a transacao a PENDING_REFERENCE.
//
// A tentativa e um numero, e nao um instante: o worker de referencias conta
// tentativas e o TTL sao contados a partir delas, e um instante de ultima tentativa
// nao responde "quantas vezes ja tentamos" sem uma segunda coluna.
func (r RepositorioTransacoes) MarcarPendentePorReferencia(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
) error {
	tag, err := q.Exec(ctx, `
		UPDATE wager_transactions
		   SET state = 'PENDING_REFERENCE', updated_at = $2
		 WHERE id = $1 AND state = 'PENDING'`,
		id.UUID(), time.Now().UTC(),
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: marcacao de referencia pendente: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: transacao %s nao esta pendente", ErrConclusaoImpossivel, id)
	}
	return nil
}

// ResolverReferencia grava a transacao referenciada que foi encontrada.
func (r RepositorioTransacoes) ResolverReferencia(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
	referenciada wallet.Identificador,
) error {
	tag, err := q.Exec(ctx, `
		UPDATE wager_transactions
		   SET reference_internal_id = $2, updated_at = $3
		 WHERE id = $1`,
		id.UUID(), referenciada.UUID(), time.Now().UTC(),
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: resolucao da referencia: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: transacao %s", ErrNaoEncontrado, id)
	}
	return nil
}

// scannerTransacao converte a linha em agregado do dominio.
//
// Toda conversao falha alto: se o dado gravado nao pode virar um Transacao valido,
// o erro aparece aqui, na leitura, e nao mais adiante em outra transacao.
func scannerTransacao(linha interface{ Scan(dest ...any) error }) (wagering.Transacao, error) {
	var (
		id, carteira, jogador            uuid.UUID
		referenciaInterna                *uuid.UUID
		provedor, externo, chave, hash   *string
		referenciaExterna                *string
		rodada, jogo                     *string
		tipo, moeda, estado, codigoFalha *string
		moedaResultado                   *string
		valor, resultado                 *int64
		criadaEm, atualizadaEm           time.Time
	)

	if err := linha.Scan(
		&id, &provedor, &externo, &chave, &hash,
		&carteira, &jogador, &rodada, &jogo, &tipo, &valor, &moeda,
		&referenciaExterna, &referenciaInterna, &estado, &codigoFalha,
		&resultado, &moedaResultado, &criadaEm, &atualizadaEm,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return wagering.Transacao{}, fmt.Errorf("%w: transacao", ErrNaoEncontrado)
		}
		return wagering.Transacao{}, fmt.Errorf("pg: leitura da transacao: %w", err)
	}

	identificador, err := wallet.IdentificadorDe(formatarUUID(id))
	if err != nil {
		return wagering.Transacao{}, fmt.Errorf("pg: id de transacao invalido no banco: %w", err)
	}
	carteiraID, err := wallet.IdentificadorDe(formatarUUID(carteira))
	if err != nil {
		return wagering.Transacao{}, fmt.Errorf("pg: id de carteira invalido no banco: %w", err)
	}
	jogadorID, err := wallet.IdentificadorDe(formatarUUID(jogador))
	if err != nil {
		return wagering.Transacao{}, fmt.Errorf("pg: id de jogador invalido no banco: %w", err)
	}

	valorMoney, err := money.Parse(formatarCentavos(valorDe(valor)), moedaDe(moeda))
	if err != nil {
		return wagering.Transacao{}, fmt.Errorf("pg: valor de transacao invalido no banco: %w", err)
	}

	var resultadoMoney money.Money
	if resultado != nil {
		resultadoMoney, err = money.Parse(formatarCentavos(*resultado), moedaDe(moedaResultado))
		if err != nil {
			return wagering.Transacao{}, fmt.Errorf("pg: resultado invalido no banco: %w", err)
		}
	}

	var referencia wallet.Identificador
	if referenciaInterna != nil {
		referencia, err = wallet.IdentificadorDe(formatarUUID(*referenciaInterna))
		if err != nil {
			return wagering.Transacao{}, fmt.Errorf("pg: referencia interna invalida no banco: %w", err)
		}
	}

	dados := wagering.Dados{
		ID:                identificador,
		Provedor:          wagering.Provedor(textoDe(provedor)),
		TransacaoExterna:  wagering.Externo(textoDe(externo)),
		ChaveIdempotencia: wagering.Chave(textoDe(chave)),
		HashConteudo:      wagering.Hash(textoDe(hash)),
		Carteira:          carteiraID,
		Jogador:           jogadorID,
		Rodada:            wagering.Rodada(textoDe(rodada)),
		Jogo:              wagering.Jogo(textoDe(jogo)),
		Tipo:              wagering.Tipo(textoDe(tipo)),
		Valor:             valorMoney,
		Estado:            wagering.Estado(textoDe(estado)),
		CodigoFalha:       wagering.CodigoFalha(textoDe(codigoFalha)),
		ReferenciaInterna: referencia,
		Resultado:         resultadoMoney,
		CriadaEm:          criadaEm,
		AtualizadaEm:      atualizadaEm,
	}
	if ref := textoDe(referenciaExterna); ref != "" {
		dados.Referencia = wagering.Referencia{Externa: wagering.Externo(ref)}
	}

	tr, err := wagering.Reidratar(dados)
	if err != nil {
		return wagering.Transacao{}, fmt.Errorf("pg: transacao no banco viola a invariante: %w", err)
	}
	return tr, nil
}

// valorDe desreferencia um bigint possivelmente nulo.
func valorDe(valor *int64) int64 {
	if valor == nil {
		return 0
	}
	return *valor
}

// moedaDe desreferencia a moeda, removendo o preenchimento de CHAR.
//
// A coluna e char(3) e o PostgreSQL devolve preenchida com espaco quando o valor
// gravado e mais curto. BRL nao sofre com isso, mas o preenchimento apareceria em
// qualquer codigo de duas letras, e o erro apareceria como "moeda invalida" em um
// valor que o cliente gravou corretamente.
func moedaDe(moeda *string) money.Currency {
	return money.Currency(strings.TrimSpace(textoDe(moeda)))
}

// textoDe desreferencia um texto possivelmente nulo.
func textoDe(texto *string) string {
	if texto == nil {
		return ""
	}
	return *texto
}

// ouNuloTexto e ouNulo para valores que sao string tipada e nao implementam
// fmt.Stringer.
func ouNuloTexto(condicao bool, valor string) any {
	if !condicao {
		return nil
	}
	return valor
}

// ouNulo devolve nil quando a condicao e falsa.
//
// A coluna de identidade externa e NULL em OPENING, e e o que o CHECK de origem
// exige. Passar string vazia seria o mesmo que NULL em valores, mas nao em
// constraints que comparam com IS NULL.
func ouNulo(condicao bool, valor fmt.Stringer) any {
	if !condicao {
		return nil
	}
	return valor.String()
}
