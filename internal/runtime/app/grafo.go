package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/google/uuid"

	casos "github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/auth"
	"github.com/LuisMassuchini/backend-challenge-go/internal/httpapi"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/buildinfo"
	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/config"
)

// Modulo de grafo.
//
// Tudo que depende de infraestrutura entra aqui: o pool, os casos de uso, o
// validador de token e o servidor HTTP. Dominio e casos de uso nao importam Fx, e
// nao sao alterados por causa deste arquivo.

// construirPool abre o pool de conexoes.
//
// O pool e construido aqui e nao dentro de um repositorio porque ele e um recurso do
// processo, com um ciclo de vida proprio, e nao um detalhe de um repositorio. E o
// ciclo de vida e registrado no Fx, o que garante que o pool fecha depois que todos
// os LeaveOnStop ja rodaram.
func construirPool(lc fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	if cfg.Postgres.DSN == "" {
		return nil, fmt.Errorf(
			"configuracao: %s e obrigatoria para servir requisicoes", config.ChavePostgresDSN)
	}

	aberto, err := pg.AbrirPool(context.Background(), cfg.Postgres.DSN, pg.Opcoes{
		MaxConexoes:    cfg.Postgres.MaxConexoes,
		TimeoutDeSaude: cfg.Postgres.TimeoutConexao,
	})
	if err != nil {
		return nil, fmt.Errorf("pool de postgres: %w", err)
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			// O ping no start e o que transforma "banco fora" de um erro por
			// requisicao em uma falha de subida com causa. Sem ele, o processo sobe,
			// passa no health check porque nao ha o que checar ainda, e falha no
			// primeiro cliente.
			if err := aberto.Ping(ctx); err != nil {
				return fmt.Errorf("pool de postgres: %w", err)
			}
			return nil
		},
		OnStop: func(context.Context) error {
			aberto.Close()
			return nil
		},
	})

	return aberto, nil
}

// construirRegistro monta o registro de metricas do processo.
//
// O registro e unico e compartilhado por construcao: o caso de uso, o relay, o
// consumidor e o servidor HTTP medem todos no mesmo Registro, e sao os valores que
// `/metrics` expoe. Um registro por componente daria quatro visoes que nenhum painel
// consegue junir, e o `wager_operacoes_total` do servidor nao bateria com o do caso
// de uso.
//
// O registro nasce no grafo e nao dentro de um worker porque `/metrics` e uma rota do
// servidor: se o registro morresse com o worker, a rota responderia vazio sem o
// processo estar com nada errado.
func construirRegistro() *obs.Registro {
	return obs.NovoRegistro()
}

// construirServicos monta os casos de uso.
func construirServicos(p *pgxpool.Pool, metricas *obs.Metricas) casos.Servicos {
	return casos.Servicos{
		Unidade:    pg.NovaUnidade(p),
		Carteiras:  pg.NovaRepositorioCarteira(),
		Ledger:     pg.NovaRepositorioLedger(),
		Transacoes: pg.NovaRepositorioTransacoes(),
		Inbox:      pg.NovaRepositorioInbox(),
		Outbox:     pg.NovaRepositorioOutbox(),
		Relogio:    relogioDoSistema{},
		Correlacao: func() string { return uuid.NewString() },
		Metricas:   metricas,
	}
}

// relogioDoSistema e o relogio de producao.
//
// Va para um tipo proprio em vez de time.Now direto no caso de uso porque o caso de
// uso nao deve saber de onde vem o instante: e o que permite um teste congelar o
// tempo eprovavel a ordem de insercao na outbox.
type relogioDoSistema struct{}

// Agora devolve o instante atual em UTC.
//
// UTC e explicito porque timestamptz depende do fuso quando o INSERT nao traz
// offset, e um fuso local mudaria o valor gravado entre maquinas.
func (relogioDoSistema) Agora() time.Time { return time.Now().UTC() }

// construirValidador monta o validador de token.
//
// Sem configuracao de OIDC, devolve nil e a aplicacao sobe sem autenticacao. E o
// estado das migrations e do primeiro boot; em qualquer outro estado a ausencia e
// erro, e quem decide e quem chama.
func construirValidador(cfg config.Config) (*auth.Validador, error) {
	if cfg.OIDC.Issuer == "" {
		return nil, nil
	}

	validador, err := auth.NovoValidador(auth.Config{
		Issuer:          cfg.OIDC.Issuer,
		Audience:        cfg.OIDC.Audience,
		URLJWKS:         cfg.OIDC.URLJWKS,
		CacheJWKS:       cfg.OIDC.CacheJWKS,
		MargemDeRelogio: cfg.OIDC.MargemDeRelogio,
		HTTPTimeout:     cfg.OIDC.HTTPTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("validador de token: %w", err)
	}
	return validador, nil
}

// prontidaoE o que o health check de readiness consulta.
//
// A checagem e um ping do pool, e nao uma consulta de negocio: o objetivo e saber
// se o processoconsegue falar com o banco, e nao se o schema esta atualizado.
// Consultar tabela de negocio aqui faria o processo ficar nao pronto por um motivo
// que ele nao pode corrigir.
type prontidaoE struct {
	p *pgxpool.Pool
}

// Verifica faz o ping.
func (p prontidaoE) Verifica(ctx context.Context) error {
	return p.p.Ping(ctx)
}

// construirServidorHTTP monta e registra o servidor.
func construirServidorHTTP(
	lc fx.Lifecycle,
	cfg config.Config,
	servicos casos.Servicos,
	validador *auth.Validador,
	pronto prontidaoE,
	registro *obs.Registro,
) *http.Server {

	deps := httpapi.Dependencias{
		Servicos: servicos,
		Pronto:   pronto.Verifica,
		// O registro vai para a borda porque `/metrics` e uma rota. Sem ele a rota
		// nao existe, e uma rota que nao existe devolve 404 -- que e uma resposta
		// diferente de "existe e esta vazia", e o Prometheus precisa da segunda.
		Metricas: registro,
	}
	// O validador entra como ponte, e nao como ponteiro, porque o roteador decide o
	// que fazer quando ele e nil e um ponteiro nil dentro de uma interface nao e
	// interface nil.
	if validador != nil {
		deps.Validador = validador
	}

	servidor := httpapi.NovoServidor(deps)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			// O listener e aberto antes de servir para que a falha de porta em uso
			// apareca no Start, e nao como uma requisicao que cai no chao depois que o
			// processo ja se anunciando como pronto.
			listener, err := net.Listen("tcp", cfg.HTTP.Address)
			if err != nil {
				return fmt.Errorf("escuta em %s: %w", cfg.HTTP.Address, err)
			}

			go func() {
				if err := servidor.Serve(listener); err != nil &&
					err != http.ErrServerClosed {
					slog.Error("servidor http encerrado com erro", "erro", err.Error())
				}
			}()

			slog.Info("servidor http no ar",
				"endereco", listener.Addr().String(),
				"versao", buildinfo.Current().Version,
				"go", buildinfo.Current().GoVersion,
				"autenticacao", validador != nil,
			)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			// O Shutdown espera as requisicoes em andamento e recusa as novas. E o que
			// segura o processo no ar enquanto a transacao que ja comecou termina e
			// confirma o commit.
			if err := servidor.Shutdown(ctx); err != nil {
				return fmt.Errorf("encerramento do servidor http: %w", err)
			}
			return nil
		},
	})

	return servidor
}
