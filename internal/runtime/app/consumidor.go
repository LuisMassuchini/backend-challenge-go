package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.uber.org/fx"

	casos "github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/consumidor"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/config"
	"github.com/LuisMassuchini/backend-challenge-go/internal/sqs"
)

// Credenciais do ambiente local de teste.
//
// Sao credenciais do LocalStack, e nao de conta nenhuma. Elas ficam em constante e
// nao em variavel de ambiente porque o endpoint ja e local e o par existe no
// compose; pedir duas variaveis a mais para um valor que nao autentica nada aqui
// seria configuracao sem efeito.
const (
	regiaoPadrao          = "us-east-1"
	chaveDeAcessoPadrao   = "test"
	segredoDeAcessoPadrao = "test"
)

// esperaMaximaDeRecebimento e o teto do long polling.
//
// O teto e metade do timeout de visibilidade. Se o long polling esperasse tanto
// quanto a visibilidade, uma mensagem entregue no fim da espera ja estaria perto de
// ser reentregue a outra instancia enquanto o consumidor ainda a processa.
const esperaMaximaDeRecebimento = 10 * time.Second

// FilaDeOperacoes embrulha o cliente SQS para o grafo.
//
// O embrulho existe porque o Fx trata um ponteiro nulo devolvido por um provider
// como "nao fornecido", e tentaria construir o consumidor sem a fila. Com um valor
// nunca nulo, o grafo monta e quem decide o que fazer com a ausencia e o proprio
// consumidor, que ja tem a checagem.
type FilaDeOperacoes struct {
	// Cliente e o cliente da fila, ou nil quando o processo sobe sem SQS.
	Cliente *sqs.Cliente
}

// Ativa informa se ha fila configurada.
func (f FilaDeOperacoes) Ativa() bool { return f.Cliente != nil }

// ConsumidorDeOperacoes embrulha o worker pelo mesmo motivo do embrulho da fila.
type ConsumidorDeOperacoes struct {
	// Worker e o worker da fila, ou nil quando o processo sobe sem SQS.
	Worker *consumidor.Worker
}

// Ativa informa se ha consumidor configurado.
func (c ConsumidorDeOperacoes) Ativa() bool { return c.Worker != nil }

// construirFila monta o cliente SQS.
//
// Sem configuracao de SQS, devolve um embrulho vazio e o processo sobe sem
// consumidor. E o estado em que as migrations e os testes de dominio rodam, e o
// estado em que o servidor HTTP pode atender sem fila nenhuma.
func construirFila(cfg config.Config) (FilaDeOperacoes, error) {
	if cfg.SQS.Endpoint == "" {
		return FilaDeOperacoes{}, nil
	}

	// O contexto e o do processo e nao o de uma requisicao: o cliente da fila e
	// construido uma vez, na subida, e precisa de um contexto para carregar a
	// configuracao da SDK.
	fila, err := sqs.Novo(context.Background(), sqs.Opcoes{
		Endpoint:        cfg.SQS.Endpoint,
		Regiao:          regiaoPadrao,
		ChaveDeAcesso:   chaveDeAcessoPadrao,
		SegredoDeAcesso: segredoDeAcessoPadrao,
		FilaOperacoes:   cfg.SQS.FilaOperacoes,
		FilaDeadLetter:  cfg.SQS.FilaDeadLetter,
		EsperaMaxima:    esperaMaximaDeRecebimento,
	})
	if err != nil {
		return FilaDeOperacoes{}, fmt.Errorf("fila: %w", err)
	}
	return FilaDeOperacoes{Cliente: fila}, nil
}

// construirConsumidor monta o worker da fila.
func construirConsumidor(
	fila FilaDeOperacoes,
	servicos casos.Servicos,
	metricas *obs.Metricas,
) ConsumidorDeOperacoes {
	if !fila.Ativa() {
		return ConsumidorDeOperacoes{}
	}

	return ConsumidorDeOperacoes{
		Worker: consumidor.Novo(consumidor.Dependencias{
			Fila:               fila.Cliente,
			Servicos:           servicos,
			Lote:               10,
			Ocioso:             500 * time.Millisecond,
			RenovaVisibilidade: 20 * time.Second,
			Metricas:           metricas,
		}),
	}
}

// registrarConsumidor sobe o worker e garante o encerramento.
//
// O worker para pelo contexto, e nao por um campo de parar: um worker com duas
// fontes de verdade para a mesma decisao para de um jeito no shutdown e de outro no
// cancelamento, sem que ninguem consiga dizer qual dos dois aconteceu.
//
// O cancelamento e o `terminou` sao variaveis do closure deste hook, e nao de
// pacote. Eles pertencem a esta execucao do ciclo de vida: guardar em variavel de
// pacote pareceria funcionar e falharia na segunda subida do processo, quando o
// OnStop cancelaria o contexto da execucao anterior.
func registrarConsumidor(lc fx.Lifecycle, consumido ConsumidorDeOperacoes, cfg config.Config) {
	if !consumido.Ativa() {
		return
	}
	worker := consumido.Worker

	var (
		cancelar func()
		terminou chan struct{}
	)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			contexto, fim := context.WithCancel(context.Background())
			cancelar = fim
			terminou = make(chan struct{})

			go func() {
				defer close(terminou)
				if err := worker.Rodar(contexto); err != nil {
					slog.Error("consumidor encerrado com erro", "erro", err.Error())
				}
			}()
			return nil
		},

		OnStop: func(context.Context) error {
			if cancelar == nil {
				return nil
			}
			cancelar()

			// O cancelamento e imediato, mas o goroutine ainda esta saindo e pode estar
			// no meio de uma transacao. Sair sem esperar perderia a confirmacao do
			// trabalho em andamento, e a mensagem voltaria para a fila.
			//
			// A espera e metade do prazo de shutdown do servidor HTTP: sobra tempo para
			// o servidor terminar as requisicoes que ainda tem.
			prazo := cfg.HTTP.ShutdownTimeout / 2
			if prazo <= 0 {
				prazo = 5 * time.Second
			}

			seletor := time.NewTimer(prazo)
			defer seletor.Stop()

			select {
			case <-terminou:
				slog.Info("consumidor de operacoes encerrado")
			case <-seletor.C:
				// Estourar o prazo aqui e melhor do que travar o shutdown: o worker
				// para no proximo ciclo de qualquer forma, e a transacao que estiver em
				// curso rola por falta de conexao, que e o desfecho correto de um
				// processo que esta morrendo.
				slog.Warn("consumidor nao encerrou dentro do prazo",
					"prazo", prazo.String())
			}
			return nil
		},
	})
}
