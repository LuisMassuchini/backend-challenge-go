// Package app e o composition root da aplicacao.
//
// E o unico lugar que conhece o grafo completo de dependencias. Dominio, casos
// de uso e adaptadores nao importam Fx: cada um recebe o que precisa por
// construtor, e quem decide o que cada um recebe e este pacote.
package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

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
}

// New monta a aplicacao a partir de uma configuracao ja validada.
//
// Configuracao entra, e nao ambiente: quem chama e que decide de onde a
// configuracao vem. E o que permite ao teste montar a aplicacao sem tocar no
// ambiente do processo.
func New(cfg config.Config, eventos *Eventos) *app {
	a := &app{
		eventos:      eventos,
		encerramento: cfg.HTTP.ShutdownTimeout,
	}

	a.interno = fx.New(
		fx.Supply(cfg, eventos),
		fx.Invoke(registrarCicloDeVida),
	)

	return a
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

// Start sobe a aplicacao.
func (a *app) Start(ctx context.Context) error {
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

// Run monta a aplicacao a partir do ambiente real, sobe e espera por sinal de
// encerramento do sistema operacional.
func Run() error {
	a, err := NewFromEnv(os.LookupEnv, &Eventos{})
	if err != nil {
		return err
	}

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

// registrarCicloDeVida registra as transicoes do processo.
//
// Fx executa os OnStop na ordem inversa de registro. Por isso, quando os
// workers e o servidor HTTP entrarem, os hooks que marcam a transicao terao de
// ser anexados antes dos hooks que param, para que a marcacao rode depois de
// todo mundo ter parado.
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
