package pg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// RepositorioCarteira le e escreve a carteira.
//
// Nenhum metodo abre transacao. Todos recebem o Querente, que pode ser uma
// transacao ou o pool: quem decide a fronteira e o caso de uso, e e por isso que o
// SELECT FOR UPDATE e o INSERT do lancamento conseguem compartilhar um commit.
type RepositorioCarteira struct{}

// NovaRepositorioCarteira constroi o repositorio.
func NovaRepositorioCarteira() *RepositorioCarteira { return &RepositorioCarteira{} }

// sqlCarteira e a projecao usada em toda leitura. Fica em um lugar so para que a
// lista de colunas nao possa divergir entre duas leituras.
const sqlCarteira = `
	SELECT id, player_id, currency, balance, version, created_at, updated_at
	  FROM wallets`

// Inserir grava a carteira.
//
// A criacao e sempre parte de uma transacao do caso de uso que tambem grava o
// OPENING e o lancamento de credito: sao tres escritas que so fazem sentido
// juntas.
func (r RepositorioCarteira) Inserir(ctx context.Context, q Querente, c wallet.Carteira) error {
	if !c.ID().Valida() {
		return fmt.Errorf("pg: carteira sem id")
	}

	_, err := q.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.ID().UUID(), c.Jogador().UUID(), string(c.Saldo().Currency()),
		c.Saldo().Amount(), c.Versao(), c.CriadaEm(), c.AtualizadaEm(),
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: insercao da carteira: %w", err))
	}
	return nil
}

// Ler busca a carteira por identificador, sem lock.
func (r RepositorioCarteira) Ler(ctx context.Context, q Querente, id wallet.Identificador) (wallet.Carteira, error) {
	linha := q.QueryRow(ctx, sqlCarteira+` WHERE id = $1`, id.UUID())

	c, err := scannerCarteira(linha)
	if err != nil {
		return wallet.Carteira{}, err
	}
	return c, nil
}

// LerPorJogadorEMoeda busca a carteira pelo par que a identifica.
func (r RepositorioCarteira) LerPorJogadorEMoeda(
	ctx context.Context,
	q Querente,
	jogador wallet.Identificador,
	moeda money.Currency,
) (wallet.Carteira, error) {
	linha := q.QueryRow(ctx,
		sqlCarteira+` WHERE player_id = $1 AND currency = $2`,
		jogador.UUID(), string(moeda),
	)

	c, err := scannerCarteira(linha)
	if err != nil {
		return wallet.Carteira{}, err
	}
	return c, nil
}

// LerParaAtualizar busca a carteira com lock pessimista de linha.
//
// E o ponto de coordenacao do sistema inteiro, e a unica linha de todo o projeto
// que precisa dessa palavra.
//
// FOR UPDATE, e nao outra estrategia, por tres razoes que o enunciado exige e que
// nao se resolvem com update condicional sozinho:
//
//  1. A leitura e a escrita precisam ser decididas juntas. Com update condicional
//     sem lock, dois debitos de 80.00 sobre 100.00 podem ambos ler 100.00, ambos
//     escrever 20.00 e ambos terem sucesso -- o segundo escrever sobrescreve o
//     primeiro sem perder erro nenhum.
//  2. O lock e por linha de carteira, e nao global: carteiras diferentes avancam em
//     paralelo, que e o outro requisito.
//  3. A constraint CHECK (balance >= 0) do banco continua sendo a ultima linha. O
//     lock evita a corrida; a constraint garante o dado.
//
// A segunda leitura da mesma carteira em outra transacao fica esperando, e o
// lock_timeout do papel de runtime transforma a espera em erro em vez de espera
// indefinida.
func (r RepositorioCarteira) LerParaAtualizar(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
) (wallet.Carteira, error) {
	linha := q.QueryRow(ctx, sqlCarteira+` WHERE id = $1 FOR UPDATE`, id.UUID())

	// A leitura e classificada como a escrita. Quando outra transacao segura o
	// lock, o erro que chega e lock_not_available, e ele e transitorio por natureza:
	// o caso de uso tem de devolver indisponibilidade e deixar o provedor reenviar,
	// e nao recusar a operacao por regra de negocio.
	c, err := scannerCarteira(linha)
	if err != nil {
		return wallet.Carteira{}, classificarErroDeEscrita(err, err)
	}
	return c, nil
}

// AtualizarSaldo grava o saldo com atualizacao condicional por versao.
//
// A clausula WHERE version = $n e a segunda metade da garantia de lost update.
// O lock protege a leitura; a versao protege a escrita. Quem escreve com uma
// versao velha recebe zero linhas e sabe que alguem mexeu na carteira entre a sua
// leitura e a sua escrita -- e pode repetir em vez de sobrescrever.
//
// Um UPDATE sem clausula de versao aqui seria seguro apenas enquanto o lock
// existisse, e o lock depende de todos os caminhos de escrita respeitarem o mesmo
// protocolo.
func (r RepositorioCarteira) AtualizarSaldo(ctx context.Context, q Querente, c wallet.Carteira) error {
	if !c.ID().Valida() {
		return fmt.Errorf("pg: carteira sem id")
	}

	// A versao esperada e a anterior: o agregado ja subiu a versao no dominio, e o
	// que o banco compara e o valor anterior, o que estava la antes desta escrita.
	esperada := c.Versao() - 1

	tag, err := q.Exec(ctx, `
		UPDATE wallets
		   SET balance = $2, version = $3, updated_at = $4
		 WHERE id = $1 AND version = $5`,
		c.ID().UUID(), c.Saldo().Amount(), c.Versao(), c.AtualizadaEm(), esperada,
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: atualizacao do saldo: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: carteira %s nao estava na versao %d", ErrConflitoDeVersao, c.ID(), esperada)
	}
	return nil
}

// formatarUUID devolve o texto canonico de um UUID lido do banco.
//
// O array de 16 bytes e o que o driver entrega para uuid do PostgreSQL. A
// conversao passa por wallet.IdentificadorDe de proposito: o dominio ja tem a
// regra de formato, e converter direto aqui criaria uma segunda regra que
// divergiria da primeira sem ninguem perceber.
func formatarUUID(bruto [16]byte) string {
	texto := make([]byte, 0, 36)
	hex := "0123456789abcdef"
	for i, b := range bruto {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			texto = append(texto, '-')
		}
		texto = append(texto, hex[b>>4], hex[b&0x0f])
	}
	return string(texto)
}

// formatarCentavos devolve o valor em unidade minima no formato decimal de Money.
func formatarCentavos(centavos int64) string {
	negativo := centavos < 0
	magnitude := centavos
	if negativo {
		magnitude = -magnitude
	}
	inteiro := magnitude / 100
	resto := magnitude % 100
	centena := "00"
	if resto < 10 {
		centena = "0"
	}
	base := strconv.FormatInt(inteiro, 10) + centena + strconv.FormatInt(resto, 10)
	if negativo {
		return "-" + base
	}
	return base
}

// scannerCarteira converte uma linha em agregado do dominio.
//
// A conversao falha com erro do proprio dominio quando o dado gravado nao pode ser
// reidratado. Isso e deliberado: um saldo negativo lido do banco e um dado
// corrompido ou uma constraint desligada, e os dois precisam aparecer como erro
// em vez de virar um Money invalido que falha mais adiante, em outra transacao.
func scannerCarteira(linha interface{ Scan(dest ...any) error }) (wallet.Carteira, error) {
	var (
		id           [16]byte
		jogador      [16]byte
		moeda        string
		saldo        int64
		versao       int64
		criadaEm     time.Time
		atualizadaEm time.Time
	)
	if err := linha.Scan(&id, &jogador, &moeda, &saldo, &versao, &criadaEm, &atualizadaEm); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return wallet.Carteira{}, fmt.Errorf("%w: carteira", ErrNaoEncontrado)
		}
		return wallet.Carteira{}, fmt.Errorf("pg: leitura da carteira: %w", err)
	}

	identificador, err := wallet.IdentificadorDe(formatarUUID(id))
	if err != nil {
		return wallet.Carteira{}, fmt.Errorf("pg: id de carteira invalido no banco: %w", err)
	}
	dono, err := wallet.IdentificadorDe(formatarUUID(jogador))
	if err != nil {
		return wallet.Carteira{}, fmt.Errorf("pg: id de jogador invalido no banco: %w", err)
	}

	valor, err := money.Parse(formatarCentavos(saldo), money.Currency(moeda))
	if err != nil {
		return wallet.Carteira{}, fmt.Errorf("pg: saldo invalido no banco: %w", err)
	}

	return wallet.Reidratar(identificador, dono, valor, versao, criadaEm, atualizadaEm)
}

// classificarErroDeEscrita traduz violacao de constraint em erro de dominio.
//
// Traduzir aqui e nao no caso de uso porque o caso de uso nao deveria conhecer
// codigo SQLSTATE: ele conhece "conflito de chave" e "saldo insuficiente", que sao
// as decisoes que ele precisa tomar.
func classificarErroDeEscrita(err error, contexto error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return contexto
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		return fmt.Errorf("%w: %w", ErrConflitoDeChave, contexto)
	case "23514": // check_violation
		return fmt.Errorf("%w: %w", ErrSaldosIncoerentes, contexto)
	case "40001": // serialization_failure
		return fmt.Errorf("%w: %w", ErrConflitoDeVersao, contexto)
	case "55P03": // lock_not_available
		return fmt.Errorf("%w: lock nao disponivel: %w", ErrConflitoDeVersao, contexto)
	}
	return contexto
}
