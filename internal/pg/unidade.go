package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Erros de persistencia que o caso de uso precisa classificar.
//
// Sao poucos e deliberadamente grossos: cada um corresponde a uma decisao do caso
// de uso -- repetir, recuar ou recusar -- e nao a um detalhe de SQL. Um
// repositorio que devolve o erro do driver entrega ao caso de uso a tarefa de
// adivinhar o que fazer com "duplicate key value violates unique constraint".
var (
	// ErrNaoEncontrado cobre leitura que nao devolveu linha.
	ErrNaoEncontrado = errors.New("pg: registro nao encontrado")
	// ErrConflitoDeVersao cobre atualizacao condicional que nao atingiu nenhuma
	// linha porque outra transacao mudou a carteira antes.
	ErrConflitoDeVersao = errors.New("pg: conflito de versao")
	// ErrConflitoDeChave cobre unicidade violada em chave de idempotencia ou em
	// par provedor e identificador externo.
	ErrConflitoDeChave = errors.New("pg: chave ja registrada")
	// ErrSaldosIncoerentes cobre lancamento que o trigger do banco recusou.
	ErrSaldosIncoerentes = errors.New("pg: lancamento incoerente com o saldo")
)

// ErrConflitoDeReserva cobre reserva de registro que outro publisher ja pegou.
//
// Nao e erro do relay: e corrida benigna entre publishers, e o relay simplesmente
// tenta de novo na proxima volta. Existe como erro proprio para que o log diga
// "outro publisher pegou antes" em vez de "falha ao reservar", que seria
// diagnostico errado.
var ErrConflitoDeReserva = errors.New("pg: registro reservado por outro publisher")

// isNenhumaLinha informa se o erro e ausencia de linha no QueryRow.
func isNenhumaLinha(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// Opcoes de pool sao os limites que importam para este sistema.
type Opcoes struct {
	// MaxConexoes e o limite de conexoes do pool. O valor e pequeno de proposito:
	// a contencao deste sistema e por carteira, e many conexoes nao ajudam a
	// resolver uma disputa de lock -- apenas acumulam espera.
	MaxConexoes int32
	// MinConexoes e o piso de conexoes mantidas abertas.
	MinConexoes int32
	// TempoMaximoDeUso e por quanto tempo uma conexao pode ser reciclada.
	//
	// Existe para que uma conexao que ficou com estado -- prepared statement de
	// uma versao antiga do codigo, por exemplo -- nao sobreviva a um deploy.
	TempoMaximoDeUso time.Duration
	// TempoMaximoDeVida e o tempo maximo de uma conexao, mesmo em uso.
	TempoMaximoDeVida time.Duration
	// TimeoutDeSaude e o prazo do health check de PostgreSQL.
	TimeoutDeSaude time.Duration
}

// valoresPadrao do pool.
//
// O pool e pequeno porque o gargalo do sistema e lock por carteira, e nao
// conexao. Um pool grande aqui aumenta o numero de instancias lendo a mesma
// carteira euraumentando a disputa, sem aumentar o paralelismo util -- que e o
// mesmo para carteiras diferentes.
const (
	maxConexoesPadrao    int32 = 8
	minConexoesPadrao    int32 = 1
	usoMaximoPadrao            = 30 * time.Minute
	vidaMaximaPadrao           = 60 * time.Minute
	timeoutDeSaudePadrao       = 2 * time.Second
)

// AbrirPool abre o pool de conexoes e verifica que o PostgreSQL responde.
//
// O ping no start e o que transforma "pool criado" em "banco alcancavel". Sem
// ele, a falha apareceria no primeiro atendimento, com a requisicao ja recebida.
func AbrirPool(ctx context.Context, dsn string, opcoes Opcoes) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, fmt.Errorf("pg: DSN ausente")
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: DSN invalido: %w", err)
	}

	if opcoes.MaxConexoes > 0 {
		config.MaxConns = opcoes.MaxConexoes
	} else {
		config.MaxConns = maxConexoesPadrao
	}
	if opcoes.MinConexoes > 0 {
		config.MinConns = opcoes.MinConexoes
	} else {
		config.MinConns = minConexoesPadrao
	}
	if opcoes.TempoMaximoDeUso > 0 {
		config.MaxConnLifetime = opcoes.TempoMaximoDeUso
	} else {
		config.MaxConnLifetime = usoMaximoPadrao
	}
	if opcoes.TempoMaximoDeVida > 0 {
		config.MaxConnIdleTime = opcoes.TempoMaximoDeVida
	} else {
		config.MaxConnIdleTime = vidaMaximaPadrao
	}
	if opcoes.TimeoutDeSaude > 0 {
		config.HealthCheckPeriod = opcoes.TimeoutDeSaude
	} else {
		config.HealthCheckPeriod = timeoutDeSaudePadrao
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("pg: criacao do pool: %w", err)
	}

	saude := opcoes.TimeoutDeSaude
	if saude <= 0 {
		saude = timeoutDeSaudePadrao
	}
	ctxSaude, cancelar := context.WithTimeout(ctx, saude)
	defer cancelar()

	if err := pool.Ping(ctxSaude); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: postgres indisponivel: %w", err)
	}

	return pool, nil
}

// Querente e o que um repositorio precisa para falar com o banco.
//
// Existe como interface pequena e fechada de proposito: e o que permite que o
// mesmo repositorio funcione dentro de uma transacao e fora dela, sem duplicar
// codigo e sem que o repositorio saiba qual dos dois e.
//
// `*pgxpool.Pool` e `pgx.Tx` implementam este conjunto. Mais metodo que este nao
// e necessario, e metodo a mais aqui seria metodo a mais no contrato do que o
// repositorio realmente usa.
type Querente interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Unidade e a fronteira da transacao SQL.
//
// A fronteira e do caso de uso, e nao do repositorio. Um repositorio que abre a
// propria transacao impede que o SELECT FOR UPDATE da carteira, o INSERT do
// lancamento, o UPDATE condicional do saldo, a inbox e a outbox compartilhem um
// unico commit -- que e o que torna a operacao atomica.
//
// O nome e Unidade e nao Transacao porque e ela que delimita a unidade de trabalho
// inteira, e nao apenas uma transacao SQL qualquer.
type Unidade struct {
	pool *pgxpool.Pool
}

// NovaUnidade constroi a unidade sobre o pool.
func NovaUnidade(pool *pgxpool.Pool) *Unidade {
	return &Unidade{pool: pool}
}

// Executar roda a funcao dentro de uma transacao e confirma no fim.
//
// A transacao e confirmada apenas se a funcao retorna nil. Qualquer erro desfaz
// tudo, inclusive o que ja tinha sido escrito antes do erro -- e por isso que o
// caso de uso nao deve escribir em pedacos separados: cada chamada a Executar e uma
// fronteira de atomicidade.
//
// O contexto entra em toda operacao de I/O e e o que respeita cancelamento e
// timeout. Um contexto sem deadline travaria a transacao ate o lock_timeout do
// papel, e o operador veria lentidao em vez de erro.
func (u *Unidade) Executar(ctx context.Context, fn func(Querente) error) error {
	if fn == nil {
		return fmt.Errorf("pg: funcao da unidade ausente")
	}

	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("pg: inicio da transacao: %w", err)
	}

	// Rollback depois de um commit confirmado devolve ErrTxClosed, e esse erro e
	// descartado de proposito: o commit ja teve seu resultado.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: confirmacao da transacao: %w", err)
	}
	return nil
}

// Ler roda a funcao fora de transacao.
//
// Existe para leitura que nao precisa de consistencia entre varias linhas. Uma
// leitura fora de transacao nao gasta um snapshot do banco, e num banco com varias
// instancias esse snapshot e o recurso que trava o VACUUM.
func (u *Unidade) Ler(ctx context.Context, fn func(Querente) error) error {
	if fn == nil {
		return fmt.Errorf("pg: funcao da leitura ausente")
	}
	return fn(u.pool)
}
