// Package app e o composition root da aplicacao.
//
// E o unico lugar que conhece o grafo completo de dependencias. Dominio, casos
// de uso e adaptadores nao importam Fx: cada um recebe o que precisa por
// construtor, e quem decide o que cada um recebe e este pacote.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/config"
)

// Evento marca uma transicao do ciclo de vida do processo.
type Evento string

const (
	// EventoSubida e registrado quando todos os hooks de start rodaram.
	EventoSubida Evento = "subida"
	// EventoEncerramento e registrado quando todos os hooks de stop rodaram.
	EventoEncerramento Evento = "encerramento"
)

// Eventos registra as transicoes do ciclo de vida.
//
// Existe para que o log de partida e o de encerramento contem o mesmo fato que
// o teste verifica. Sem ele, a unica prova de que o shutdown acontece e o log
// de uma maquina que ja foi desligada.
type Eventos struct {
	registrados []Evento
}

func (e *Eventos) registrar(evento Evento) {
	e.registrados = append(e.registrados, evento)
}

// Eventos devolve os eventos registrados, na ordem em que aconteceram.
func (e *Eventos) Eventos() []Evento {
	registrados := make([]Evento, len(e.registrados))
	copy(registrados, e.registrados)
	return registrados
}

// Subiu informa se a aplicacao chegou a subir.
func (e *Eventos) Subiu() bool { return e.registrou(EventoSubida) }

// Encerrou informa se a aplicacao chegou a encerrar.
func (e *Eventos) Encerrou() bool { return e.registrou(EventoEncerramento) }

func (e *Eventos) registrou(alvo Evento) bool {
	for _, evento := range e.registrados {
		if evento == alvo {
			return true
		}
	}
	return false
}

// app e o ciclo de vida da aplicacao.
//
// Fx entrega o grafo pronto em *fx.App, que e quem executa os hooks de start e
// de stop. Guardar o App aqui deixa Start e Stop com o mesmo nome e o mesmo
// contrato em todo o resto do codigo.
type app struct {
	interno      *fx.App
	eventos      *Eventos
	encerramento time.Duration

	// erroDePartida e a falha de configuracao que impede o processo de subir.
	//
	// Ela mora aqui e nao dentro do grafo porque o grafo do Fx reportaria a falha
	// com a cadeia de construcao por cima, e o operador veria "failed to build
	// arguments" em vez do nome da variavel que ele precisa arrumar. A validacao
	// acontece antes de fx.New, e o Start devolve este erro primeiro.
	erroDePartida error
}

// New monta a aplicacao a partir de uma configuracao ja validada.
//
// Configuracao entra, e nao ambiente: quem chama e que decide de onde a
// configuracao vem. E o que permite ao teste montar a aplicacao sem tocar no
// ambiente do processo.
func New(cfg config.Config, eventos *Eventos) *app {
	a := &app{
		eventos:       eventos,
		encerramento:  cfg.HTTP.ShutdownTimeout,
		erroDePartida: validarParaSubir(cfg),
	}

	a.interno = fx.New(
		fx.Supply(cfg, eventos),
		fx.Provide(
			construirPool,
			construirServicos,
			construirValidador,
			construirProntidao,
			construirServidorHTTP,
		),
		fx.Invoke(registrarCicloDeVida),
		// O grafo do Fx e preguicoso: um provider so e construido quando alguem
		// precisa do valor. Sem este Invoke, o servidor, o pool e o validador seriam
		// providers nunca pedidos, e a aplicacao subiria sem abrir porta nenhuma --
		// parecendo pronta e recusando conexao.
		fx.Invoke(ligarGrafo),
	)

	return a
}

// ligarGrafo existe so para forcar a construcao do grafo.
//
// O Fx so constroi o que alguem pede. Um provider registrado e nunca consumido nao
// e executado, e a aplicacao subiria sem abrir porta, sem pool e sem validador:
// pareceria pronta e recusaria conexao. Este Invoke pede o servidor, o que arrasta o
// pool, os casos de uso, o validador e a prontidao, e so entao o Start abre a porta.
func ligarGrafo(*http.Server) {}

// construirProntidao monta a checagem de readiness.
func construirProntidao(p *pgxpool.Pool) prontidaoE {
	return prontidaoE{p: p}
}

// NewFromEnv carrega e valida a configuracao antes de montar o grafo.
//
// A configuracao e lida antes de fx.New, e nao como um provider. A diferenca
// importa: com um provider, uma configuracao invalida so falha no primeiro
// Start, quando a maquina ja subiu e outros recursos ja foram abertos.
func NewFromEnv(getenv func(string) (string, bool), eventos *Eventos) (*app, error) {
	cfg, err := config.FromEnv(getenv)
	if err != nil {
		return nil, fmt.Errorf("configuracao: %w", err)
	}
	return New(cfg, eventos), nil
}

// validarParaSubir confere o que o processo precisa para atender.
//
// A funcao existe para o erro de configuracao chegar ao operador como ele pode
// usar. Quem sobe o processo precisa saber qual variavel arrumar, e nao em que
// provider do grafo a construcao quebrou.
func validarParaSubir(cfg config.Config) error {
	if cfg.Postgres.DSN == "" {
		return fmt.Errorf(
			"%w: %s e obrigatoria para servir requisicoes", errConfiguracao, config.ChavePostgresDSN)
	}
	return nil
}

// Start sobe a aplicacao.
func (a *app) Start(ctx context.Context) error {
	// A configuracao e conferida antes do grafo para que o erro de partida seja o
	// erro de configuracao, e nao a cadeia de construcao do Fx por cima dele.
	if a.erroDePartida != nil {
		return a.erroDePartida
	}

	if err := a.interno.Start(ctx); err != nil {
		return fmt.Errorf("subida da aplicacao: %w", err)
	}
	return nil
}

// Stop encerra a aplicacao.
//
// O contexto recebido tem o prazo de shutdown da configuracao: e ele que segura
// o processo no ar enquanto o trabalho em andamento termina.
func (a *app) Stop(ctx context.Context) error {
	if err := a.interno.Stop(ctx); err != nil {
		return fmt.Errorf("encerramento da aplicacao: %w", err)
	}
	return nil
}

// errConfiguracao marca falha de configuracao na partida.
//
// E um tipo proprio para que o chamador consiga separar "o operador tem que
// arrumar uma variavel" de "o processo encontrou um defeito", que e o que o
// conteudo do erro ja diz.
var errConfiguracao = errors.New("runtime: configuracao invalida")

// ConfiguracaoInvalida diz se o erro de partida e de configuracao.
func ConfiguracaoInvalida(err error) bool {
	return errors.Is(err, errConfiguracao)
}

// Run monta a aplicacao a partir do ambiente real, sobe e espera por sinal de
// encerramento do sistema operacional.
func Run() error {
	cfg, err := config.FromEnv(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("configuracao: %w", err)
	}

	configurarLog(cfg.Runtime.LogLevel)

	a := New(cfg, &Eventos{})

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		return err
	}

	sinal := make(chan os.Signal, 1)
	signal.Notify(sinal, os.Interrupt, syscall.SIGTERM)
	<-sinal

	// O prazo de shutdown vem da configuracao: e ele que limita quanto tempo o
	// processo fica no ar depois do SIGTERM.
	ctxEncerramento, cancelar := context.WithTimeout(ctx, a.encerramento)
	defer cancelar()

	return a.Stop(ctxEncerramento)
}

// configurarLog instala o log estruturado.
//
// O formato e JSON e nao texto, porque o que vai consumir essas linhas depois e um
// agregador de logs, e parsear texto com espacos dentro da mensagem e trabalho inutil.
// O nivel vem da configuracao e o destino e a saida padrao, que em container e o que
// o orquestrador coleta.
func configurarLog(nivel config.LogLevel) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: nivel.Slog(),
	})))
}

// registrarCicloDeVida registra as transicoes do processo.
//
// Fx executa os OnStop na ordem inversa de registro. Por isso, quando os workers e o
// servidor HTTP entrarem, os hooks que marcam a transicao terao de ser
// anexados antes dos hooks que param, para que a marcacao rode depois de todo mundo
// ter parado.
//
// Esta ordem importa em E12. Os hooks sao anexados na ordem em que sao registrados, e
// o OnStop roda de tras para frente: quem para por ultimo foi registrado por ultimo.
// O ciclo de vida e registrado primeiro, entao a marcacao de encerramento roda por
// ultimo de todos, depois que o servidor HTTP, os workers e o pool ja pararam.
func registrarCicloDeVida(lc fx.Lifecycle, eventos *Eventos) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			eventos.registrar(EventoSubida)
			return nil
		},
		OnStop: func(context.Context) error {
			eventos.registrar(EventoEncerramento)
			return nil
		},
	})
}
