package pg

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// RepositorioLedger grava e le o ledger da carteira.
type RepositorioLedger struct{}

// NovaRepositorioLedger constroi o repositorio.
func NovaRepositorioLedger() *RepositorioLedger { return &RepositorioLedger{} }

// sqlLancamento e a projecao do ledger.
const sqlLancamento = `
	SELECT id, wallet_id, transaction_id, direction, money_amount, currency,
	       balance_before, balance_after, created_at
	  FROM wallet_ledger_entries`

// Inserir grava o lancamento.
//
// Nao ha UPSERT e nao ha atualizacao: o ledger e append-only, e a garantia e do
// banco -- o trigger recusa UPDATE e DELETE e o papel de runtime nao tem DELETE.
func (r RepositorioLedger) Inserir(ctx context.Context, q Querente, l wallet.Lancamento) error {
	if !l.Valida() {
		return fmt.Errorf("pg: lancamento invalido")
	}

	_, err := q.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, money_amount, currency,
			balance_before, balance_after, created_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		l.ID().UUID(), l.Carteira().UUID(), l.Transacao().UUID(), string(l.Direcao()),
		l.Valor().Amount(), string(l.Valor().Currency()),
		l.SaldoAnterior().Amount(), l.SaldoPosterior().Amount(), l.CriadoEm(),
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: insercao do lancamento: %w", err))
	}
	return nil
}

// ErrCursorInvalido cobre cursor que o cliente enviou e nao pode ser lido.
//
// Erro de entrada e nao pagina vazia: devolver vazio para um cursor quebrado faria
// o cliente receber a primeira pagina sem saber que o resultado estava errado.
var ErrCursorInvalido = errors.New("pg: cursor de paginacao invalido")

// LimitePadraoDePagina e quantos lancamentos vem numa pagina quando o cliente nao
// pede outro numero.
const LimitePadraoDePagina = 50

// limiteMaximoDePagina e o teto aceito.
//
// Existe porque o parametro vem do cliente. Sem teto, `limit=1000000` em uma
// carteira com muito movimento consome a memoria do processo e, com o locktimeout
// de sessao, derruba a requisicao em vez de recusar um numero absurdo.
const limiteMaximoDePagina = 500

// Cursor e a posicao opaca da paginacao.
//
// Opaco por decisao: um cliente nao deve conseguir montar o cursor a mao nem deduzir
// o que ha depois. O conteudo tambem impede que o cliente mude a ordem ou o filtro sem que
// o servidor perceba.
type Cursor struct {
	// Instante do ultimo lancamento da pagina anterior.
	instante time.Time
	// ID do ultimo lancamento da pagina anterior. Entra porque varios lancamentos
	// podem ter o mesmo instante, e sem ele a paginacao pula ou repete linha.
	identificador wallet.Identificador
}

// CursorDe monta um cursor a partir de instante e identificador.
//
// E exportado para que um teste e o proprio repositorio possam montar um cursor
// sem reimplementar a serializacao.
func CursorDe(instante time.Time, identificador wallet.Identificador) Cursor {
	return Cursor{instante: instante, identificador: identificador}
}

// Instante devolve o instante do cursor.
func (c Cursor) Instante() time.Time { return c.instante }

// Identificador devolve o identificador do cursor.
func (c Cursor) Identificador() wallet.Identificador { return c.identificador }

// CursorNulo devolve o cursor inicial, o da primeira pagina.
func CursorNulo() Cursor { return Cursor{} }

// CursorAnterior devolve o cursor que aponta para a posicao do lancamento, para uso
// por quem quiser retomar de uma posicao conhecida.
func CursorAnterior(l wallet.Lancamento) Cursor {
	return Cursor{instante: l.CriadoEm(), identificador: l.ID()}
}

// Texto devolve a representacao opaca do cursor.
func (c Cursor) Texto() string {
	if c.instante.IsZero() && !c.identificador.Valida() {
		return ""
	}
	bruto := fmt.Sprintf("%d|%s", c.instante.UnixNano(), c.identificador)
	return base64.RawURLEncoding.EncodeToString([]byte(bruto))
}

// DecodificarCursor le um cursor recebido do cliente.
//
// Cursor invalido e erro de entrada, e nao pagina vazia: devolver vazio para um
// cursor quebrado faria o cliente receber a primeira pagina sem saber que o
// resultado estava errado.
func DecodificarCursor(texto string) (Cursor, error) {
	if texto == "" {
		return Cursor{}, nil
	}

	bruto, err := base64.RawURLEncoding.DecodeString(texto)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: cursor nao e base64: %v", ErrCursorInvalido, err)
	}

	textoInstante, textoID, achou := cortar(string(bruto), '|')
	if !achou {
		return Cursor{}, fmt.Errorf("%w: cursor sem separador", ErrCursorInvalido)
	}

	nanos, err := time.ParseDuration(textoInstante + "ns")
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: cursor com instante invalido: %v", ErrCursorInvalido, err)
	}

	id, err := wallet.IdentificadorDe(textoID)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: cursor com id invalido: %v", ErrCursorInvalido, err)
	}

	return Cursor{instante: time.Unix(0, int64(nanos)).UTC(), identificador: id}, nil
}

// cortar divide o texto na primeira ocorrencia do separador.
func cortar(texto string, separador byte) (string, string, bool) {
	for i := 0; i < len(texto); i++ {
		if texto[i] == separador {
			return texto[:i], texto[i+1:], true
		}
	}
	return "", "", false
}

// PaginaE o resultado de uma pagina do ledger.
type PaginaE struct {
	// Lancamentos e a pagina, em ordem cronologica decrescente.
	Lancamentos []wallet.Lancamento
	// ProximoCursor e o cursor da proxima pagina, vazio quando acabou.
	ProximoCursor Cursor
	// TemMais informa se existe pagina seguinte.
	TemMais bool
}

// Listar devolve os lancamentos da carteira em ordem cronologica decrescente.
//
// A ordem e decrescente e o cursor compara o par (created_at, id). Um cursor so por
// instante nao basta: dois lancamentos com o mesmo instante -- o que acontece
// quando a abertura e o primeiro debito acontecem no mesmo tick -- fariam a pagina
// seguinte repetir ou pular a linha do limite.
//
// A consulta soma um registro ao limite para saber se existe proxima pagina, em vez
// de fazer uma segunda contagem: e uma leitura a menos por pagina, e a resposta
// fica coerente com os dados devolvidos.
func (r RepositorioLedger) Listar(
	ctx context.Context,
	q Querente,
	carteira wallet.Identificador,
	cursor Cursor,
	limite int,
) (PaginaE, error) {
	if !carteira.Valida() {
		return PaginaE{}, fmt.Errorf("pg: carteira invalida")
	}

	if limite <= 0 {
		limite = LimitePadraoDePagina
	}
	if limite > limiteMaximoDePagina {
		limite = limiteMaximoDePagina
	}

	var lancamentos []struct {
		id          [16]byte
		carteira    [16]byte
		transacao   [16]byte
		direcao     string
		valor       int64
		moeda       string
		saldoAntes  int64
		saldoDepois int64
		criadoEm    time.Time
	}

	consulta := sqlLancamento + `
		WHERE wallet_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`

	args := []any{carteira.UUID(), int64(limite + 1)}
	if !cursor.instante.IsZero() {
		consulta = sqlLancamento + `
			WHERE wallet_id = $1 AND (created_at, id) < ($2, $3)
			ORDER BY created_at DESC, id DESC
			LIMIT $4`
		args = []any{carteira.UUID(), cursor.instante, cursor.identificador.UUID(), int64(limite + 1)}
	}

	registros, err := q.Query(ctx, consulta, args...)
	if err != nil {
		return PaginaE{}, fmt.Errorf("pg: leitura do ledger: %w", err)
	}
	defer registros.Close()

	for registros.Next() {
		var linha struct {
			id          [16]byte
			carteira    [16]byte
			transacao   [16]byte
			direcao     string
			valor       int64
			moeda       string
			saldoAntes  int64
			saldoDepois int64
			criadoEm    time.Time
		}
		if err := registros.Scan(
			&linha.id, &linha.carteira, &linha.transacao, &linha.direcao, &linha.valor, &linha.moeda,
			&linha.saldoAntes, &linha.saldoDepois, &linha.criadoEm,
		); err != nil {
			return PaginaE{}, fmt.Errorf("pg: varredura do ledger: %w", err)
		}
		lancamentos = append(lancamentos, linha)
	}
	if err := registros.Err(); err != nil {
		return PaginaE{}, fmt.Errorf("pg: varredura do ledger: %w", err)
	}

	temMais := len(lancamentos) > limite
	if temMais {
		lancamentos = lancamentos[:limite]
	}

	pagina := PaginaE{Lancamentos: make([]wallet.Lancamento, 0, len(lancamentos))}
	for _, linha := range lancamentos {
		l, err := montarLancamento(linha.id, linha.carteira, linha.transacao, linha.direcao,
			linha.valor, linha.moeda, linha.saldoAntes, linha.saldoDepois, linha.criadoEm)
		if err != nil {
			return PaginaE{}, err
		}
		pagina.Lancamentos = append(pagina.Lancamentos, l)
	}

	if temMais && len(pagina.Lancamentos) > 0 {
		ultimo := pagina.Lancamentos[len(pagina.Lancamentos)-1]
		pagina.ProximoCursor = CursorAnterior(ultimo)
		pagina.TemMais = true
	}

	return pagina, nil
}

// SomarPorCarteira devolve o saldo que o ledger implica para a carteira.
//
// A reconciliacao compara este valor com o saldo gravado na carteira. A soma e feita
// no banco e nao percorrendo as linhas, porque uma carteira com muito movimento
// nao cabe em memoria, e o objetivo e reconciliar sem custo proporcional ao
// historico.
//
// O resultado e sempre derivado das linhas existentes, nunca do saldo da carteira:
// e justamente a comparacao entre as duas fontes que detecta divergencia.
func (r RepositorioLedger) SomarPorCarteira(
	ctx context.Context,
	q Querente,
	carteira wallet.Identificador,
) (money.Money, int64, error) {
	if !carteira.Valida() {
		return money.Money{}, 0, fmt.Errorf("pg: carteira invalida")
	}

	var (
		creditos int64
		debitos  int64
		total    int
		moeda    string
	)

	// Os dois lados sao somados no servidor e trazidos de volta ja somados: trazer as
	// linhas para o processo seria copiar o historico inteiro da carteira.
	linha := q.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN money_amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN direction = 'DEBIT'  THEN money_amount ELSE 0 END), 0),
			COUNT(*),
			COALESCE(MIN(currency), '')
		FROM wallet_ledger_entries
		WHERE wallet_id = $1`,
		carteira.UUID(),
	)

	if err := linha.Scan(&creditos, &debitos, &total, &moeda); err != nil {
		return money.Money{}, 0, fmt.Errorf("pg: soma do ledger: %w", err)
	}

	// Sem lancamento, a soma e zero e nao ha moeda de referencia. O chamador
	// descobre a moeda pela carteira, que e a unica fonte valida nesse caso.
	if total == 0 {
		return money.Money{}, 0, nil
	}

	saldo, err := money.Parse(formatarCentavos(creditos-debitos), money.Currency(moeda))
	if err != nil {
		return money.Money{}, 0, fmt.Errorf("pg: soma do ledger viola a invariante: %w", err)
	}
	return saldo, int64(total), nil
}

// montarLancamento converte a linha em valor de dominio.
func montarLancamento(
	idBruto, carteiraBruta, transacaoBruta [16]byte,
	direcao string,
	valor int64,
	moeda string,
	saldoAntes int64,
	saldoDepois int64,
	criadoEm time.Time,
) (wallet.Lancamento, error) {
	id, err := wallet.IdentificadorDe(formatarUUID(idBruto))
	if err != nil {
		return wallet.Lancamento{}, fmt.Errorf("pg: id de lancamento invalido no banco: %w", err)
	}
	carteira, err := wallet.IdentificadorDe(formatarUUID(carteiraBruta))
	if err != nil {
		return wallet.Lancamento{}, fmt.Errorf("pg: id de carteira invalido no banco: %w", err)
	}
	transacao, err := wallet.IdentificadorDe(formatarUUID(transacaoBruta))
	if err != nil {
		return wallet.Lancamento{}, fmt.Errorf("pg: id de transacao invalido no banco: %w", err)
	}

	moedaValor := money.Currency(moeda)
	valorMoney, err := money.Parse(formatarCentavos(valor), moedaValor)
	if err != nil {
		return wallet.Lancamento{}, fmt.Errorf("pg: valor de lancamento invalido no banco: %w", err)
	}
	antes, err := money.Parse(formatarCentavos(saldoAntes), moedaValor)
	if err != nil {
		return wallet.Lancamento{}, fmt.Errorf("pg: saldo anterior invalido no banco: %w", err)
	}
	depois, err := money.Parse(formatarCentavos(saldoDepois), moedaValor)
	if err != nil {
		return wallet.Lancamento{}, fmt.Errorf("pg: saldo posterior invalido no banco: %w", err)
	}

	l, err := wallet.NovoLancamento(id, carteira, transacao, wallet.Direcao(direcao),
		valorMoney, antes, depois, criadoEm)
	if err != nil {
		return wallet.Lancamento{}, fmt.Errorf("pg: lancamento no banco viola a invariante: %w", err)
	}
	return l, nil
}
