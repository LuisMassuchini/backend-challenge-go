package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	casos "github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/outbox"
	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/config"
	"github.com/LuisMassuchini/backend-challenge-go/internal/sqs"
)

// RelayDaOutbox embrulha o relay pelo mesmo motivo do consumidor de fila.
//
// O Fx trata ponteiro nulo devolvido por provider como "nao fornecido", e tentaria
// montar os hooks com um relay inexistente. Com um valor nunca nulo, quem decide o
// que fazer com a ausencia e o proprio hook.
type RelayDaOutbox struct {
	// Relay e o relay, ou nil quando o processo sobe sem fila de saida.
	Relay *outbox.Relay
}

// Ativa informa se o relay existe.
func (r RelayDaOutbox) Ativa() bool { return r.Relay != nil }

// construirRelayDaOutbox monta o relay que publica os eventos.
//
// O relay existe quando ha SQS configurado, e nao quando ha banco: sem fila de saida
// nao existe para onde publicar, e um relay sem destino seria um worker que falha em
// laco contra a propria ausencia de destino.
func construirRelayDaOutbox(cfg config.Config, servicos casos.Servicos) (RelayDaOutbox, error) {
	if cfg.SQS.Endpoint == "" {
		return RelayDaOutbox{}, nil
	}

	// O contexto e o do processo e nao o de uma requisicao: o publicador e
	// construido uma vez, na subida.
	publicador, err := sqs.NovoPublicador(context.Background(), sqs.Opcoes{
		Endpoint:        cfg.SQS.Endpoint,
		Regiao:          regiaoPadrao,
		ChaveDeAcesso:   chaveDeAcessoPadrao,
		SegredoDeAcesso: segredoDeAcessoPadrao,
		FilaEventos:     cfg.SQS.FilaEventos,
	})
	if err != nil {
		return RelayDaOutbox{}, fmt.Errorf("publicador de eventos: %w", err)
	}

	// O nome do editor identifica esta instancia na reserva da outbox. O do processo
	// porque cada processo e um relay: dois relays com o mesmo nome em maquinas
	// diferentes deixariam o operador sem saber quem tem o registro preso.
	//
	// A referencia ao processo e o que responde "quem esta atrasado" quando o
	// operador olha a coluna leased_by de um registro que nao sai da fila.
	editor := fmt.Sprintf("%s-%s", hostname(), uuid.NewString()[:8])

	relay, err := outbox.Novo(outbox.Dependencias{
		Unidade:          servicos.Unidade,
		Outbox:           servicos.Outbox,
		Publicador:       publicador,
		Editor:           editor,
		Relogio:          casos.RelogioDeSistema{},
		Lote:             50,
		Ocioso:           time.Second,
		Janela:           30 * time.Second,
		Backoff:          2 * time.Second,
		MaximoTentativas: 12,
	})
	if err != nil {
		return RelayDaOutbox{}, fmt.Errorf("relay da outbox: %w", err)
	}
	return RelayDaOutbox{Relay: relay}, nil
}

// hostname devolve o nome da maquina, ou "desconhecido" quando nao ha.
//
// A falha de hostname nao pode derrubar a subida: o nome do editor e informacao de
// diagnostico, e um processo que sobe sem ela e melhor do que um processo que nao
// sobe.
func hostname() string {
	nome, err := os.Hostname()
	if err != nil || nome == "" {
		return "desconhecido"
	}
	return nome
}

// registrarRelayDaOutbox sobe o relay e garante o encerramento.
//
// Registrado depois do consumidor de fila e do worker de pendencias para que, no
// encerramento, ambos parem antes: enquanto ainda ha quem produza evento, o relay
// precisa estar vivo, senao o ultimo evento do processo ficaria sem publicacao. E
// registrado antes do servidor HTTP para que o servidor pare primeiro.
func registrarRelayDaOutbox(lc fx.Lifecycle, empacotado RelayDaOutbox, cfg config.Config) {
	if !empacotado.Ativa() {
		return
	}
	relay := empacotado.Relay

	var cancelar func()
	var terminou chan struct{}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			contexto, fim := context.WithCancel(context.Background())
			cancelar = fim
			terminou = make(chan struct{})

			go func() {
				defer close(terminou)
				if err := relay.Rodar(contexto); err != nil {
					slog.Error("relay da outbox encerrado com erro", "erro", err.Error())
				}
			}()
			return nil
		},

		OnStop: func(context.Context) error {
			if cancelar == nil {
				return nil
			}
			cancelar()

			// A espera e a mesma do consumidor de fila: um relay no meio de um ciclo
			// pode estar publicando, e sair sem esperar perderia a confirmacao do que
			// ja foi publicado -- o registro voltaria para a fila e seria republicado.
			// Republicar e seguro, e wasteful: o consumidor receberia o mesmo evento
			// duas vezes.
			prazo := cfg.HTTP.ShutdownTimeout / 2
			if prazo <= 0 {
				prazo = 5 * time.Second
			}

			seletor := time.NewTimer(prazo)
			defer seletor.Stop()

			select {
			case <-terminou:
				slog.Info("relay da outbox encerrado")
			case <-seletor.C:
				// Estourar o prazo e melhor do que travar o shutdown: o relay para no
				// proximo ciclo de qualquer jeito, e o registro que ele tinha fica com a
				// reserva vencida, que e o estado de recuperacao.
				slog.Warn("relay da outbox nao encerrou dentro do prazo",
					"prazo", prazo.String())
			}
			return nil
		},
	})
}
