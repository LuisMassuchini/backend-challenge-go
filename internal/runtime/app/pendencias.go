package app

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"

	casos "github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pendencias"
)

// WorkerDePendencias embrulha o worker pelo mesmo motivo do consumidor de fila.
//
// O Fx trata ponteiro nulo devolvido por provider como "nao fornecido", e tentaria
// construir os hooks com um worker inexistente. Com um valor nunca nulo, quem decide
// o que fazer com a ausencia e o proprio hook.
type WorkerDePendencias struct {
	// Worker e o worker, ou nil quando o processo sobe sem pendencias.
	Worker *pendencias.Worker
}

// Ativa informa se o worker existe.
func (w WorkerDePendencias) Ativa() bool { return w.Worker != nil }

// construirWorkerDePendencias monta o worker de referencias pendentes.
//
// O worker existe sempre que o processo tem banco, e nao quando tem fila: a pendencia
// nasce de uma operacao que ja entrou, e a retomada dela e obrigatoria mesmo em uma
// implantacao que so recebe HTTP. Um worker conditioned a fila deixaria a pendencia
// esperando para sempre em qualquer ambiente sem consumidor.
func construirWorkerDePendencias(servicos casos.Servicos) WorkerDePendencias {
	return WorkerDePendencias{
		Worker: pendencias.Novo(pendencias.Dependencias{
			Servicos: servicos,
			Politica: casos.PoliticaPadrao(),
			Lote:     20,
			Ocioso:   time.Second,
		}),
	}
}

// registrarWorkerDePendencias sobe o worker e garante o encerramento.
//
// Registrado depois do consumidor de fila para que, no encerramento, o consumidor
// pare antes: enquanto o consumidor ainda aplica operacoes, o worker de pendencias
// precisa estar vivo, senao uma pendencia criada no ultimo segundo ficaria sem
// ninguem para retomar.
func registrarWorkerDePendencias(lc fx.Lifecycle, retrabalho WorkerDePendencias) {
	if !retrabalho.Ativa() {
		return
	}
	worker := retrabalho.Worker

	var cancelar func()
	var terminou chan struct{}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			contexto, fim := context.WithCancel(context.Background())
			cancelar = fim
			terminou = make(chan struct{})

			go func() {
				defer close(terminou)
				if err := worker.Rodar(contexto); err != nil {
					slog.Error("worker de pendencias encerrado com erro", "erro", err.Error())
				}
			}()
			return nil
		},

		OnStop: func(context.Context) error {
			if cancelar == nil {
				return nil
			}
			cancelar()

			seletor := time.NewTimer(5 * time.Second)
			defer seletor.Stop()

			select {
			case <-terminou:
			case <-seletor.C:
				slog.Warn("worker de pendencias nao encerrou dentro do prazo")
			}
			return nil
		},
	})
}
