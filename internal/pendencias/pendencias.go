// Package pendencias e o worker que retoma as reversoes que ficaram esperando.
//
// Ele existe porque "a referencia ainda nao chegou" e uma espera duravel, nao um
// erro. A transacao fica em PENDING_REFERENCE no banco, e este worker e quem volta
// nela ate a referencia aparecer ou a espera acabar.
package pendencias

import (
	"context"
	"log/slog"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
)

// Worker percorre as pendencias de referencia.
type Worker struct {
	// servicos sao os casos de uso.
	servicos app.Servicos

	// politica e a regra de backoff e expiracao.
	politica app.Politica

	// lote e quantas pendencias sao reservadas por ciclo.
	lote int

	// ocioso e quanto tempo se espera entre ciclos sem pendencia.
	//
	// A espera existe para que uma fila de espera vazia nao vire laco apertado. O
	// agendamento gravado no banco ja garante que uma pendencia nao seja procurada
	// antes da hora; este piso cobre apenas o caso de lote vazio.
	ocioso time.Duration
}

// Dependencias e o que o worker precisa.
type Dependencias struct {
	// Servicos sao os casos de uso.
	Servicos app.Servicos

	// Politica e a regra de backoff e expiracao.
	Politica app.Politica

	// Lote e quantas pendencias sao reservadas por ciclo.
	Lote int

	// Ocioso e a espera entre ciclos sem pendencia.
	Ocioso time.Duration
}

// Novo monta o worker.
func Novo(d Dependencias) *Worker {
	politica := d.Politica
	if politica.IntervaloInicial <= 0 {
		politica = app.PoliticaPadrao()
	}
	lote := d.Lote
	if lote <= 0 {
		lote = 20
	}
	ocioso := d.Ocioso
	if ocioso <= 0 {
		ocioso = time.Second
	}

	return &Worker{
		servicos: d.Servicos,
		politica: politica,
		lote:     lote,
		ocioso:   ocioso,
	}
}

// Rodar percorre as pendencias ate o contexto ser cancelado.
//
// O contexto e o unico sinal de parada, pelo mesmo motivo do consumidor de fila: duas
// fontes de verdade para a mesma decisao fariam o shutdown e o cancelamento divergirem
// sem que ninguem pudesse dizer qual dos dois aconteceu.
func (w *Worker) Rodar(ctx context.Context) error {
	slog.Info("worker de referencias pendentes no ar",
		"lote", w.lote,
		"ocioso", w.ocioso.String(),
		"tentativas_maximas", w.politica.MaximoDeTentativas,
	)

	for {
		if ctx.Err() != nil {
			return nil
		}

		tratadas, err := app.RetomarPendencias(ctx, w.servicos, w.politica, w.lote)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Falha de infraestrutura no laco. Voltar a tentar imediatamente
			// transformaria o banco fora em um laco apertado de log.
			slog.Error("falha no ciclo de pendencias", "erro", err.Error())
			if !dormir(ctx, w.ocioso) {
				return nil
			}
			continue
		}

		// Com trabalho feito o ciclo seguinte vem logo: ha pendencia esperando e cada
		// volta e uma chance de resolve-la. Sem trabalho, o worker espera.
		if tratadas == 0 {
			if !dormir(ctx, w.ocioso) {
				return nil
			}
		}
	}
}

// dormir espera o tempo indicado e devolve false quando o contexto terminou antes.
func dormir(ctx context.Context, duracao time.Duration) bool {
	caso := time.NewTimer(duracao)
	defer caso.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-caso.C:
		return true
	}
}
