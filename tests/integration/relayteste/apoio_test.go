//go:build integration

// Package relayteste exercita o relay da outbox contra o PostgreSQL e o LocalStack
// de verdade.
//
// O relay e a unica coisa entre o commit que grava o evento e o consumidor que o le.
// O que precisa ser provado -- nenhum evento publicado antes do commit, dois
// publishers sem publicar o mesmo evento, publicacao que sobrevive a uma queda entre
// publicar e confirmar -- existe no comportamento do banco e do broker ao mesmo
// tempo. Um substituto em memoria provaria que o substituto obedece.
package relayteste

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/eventos"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/internal/sqs"
)

// dsnRuntime e o papel de menor privilegio, o mesmo que a aplicacao usa.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// Fila de saida. O nome segue a convencao das filas de entrada.
const (
	filaEventos      = "wager-events.fifo"
	filaEventosDLQ   = "wager-events-dlq.fifo"
	filaOperacoes    = "wager-transactions.fifo"
	filaOperacoesDLQ = "wager-transactions-dlq.fifo"
)

// contexto devolve um contexto com prazo.
func contexto(t *testing.T) context.Context {
	t.Helper()
	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancelar)
	return ctx
}

// unidade abre um pool do papel de runtime e devolve a unidade sobre ele.
func unidade(t *testing.T) *pg.Unidade {
	t.Helper()
	aberto, err := pg.AbrirPool(contexto(t), dsnRuntime, pg.Opcoes{MaxConexoes: 8})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(aberto.Close)
	return pg.NovaUnidade(aberto)
}

// filaDeEventos monta um cliente apontado para a fila de saida.
//
// Cliente separado do de operacoes por um motivo concreto: o `Cliente` de sqs
// resolve o endereco de fila na construcao e guarda em campo, e um cliente so
// apontaria para uma das duas filas. O relay publica evento; o consumidor recebe
// operacao. Sao dois destinos com contratos diferentes.
func filaDeEventos(t *testing.T) *sqs.Cliente {
	t.Helper()
	cliente, err := sqs.Novo(contexto(t), sqs.Opcoes{
		Endpoint:        endpointDoSqs(),
		Regiao:          "us-east-1",
		ChaveDeAcesso:   "test",
		SegredoDeAcesso: "test",
		FilaOperacoes:   filaEventos,
		FilaDeadLetter:  filaEventosDLQ,
	})
	if err != nil {
		t.Fatalf("cliente da fila de eventos: %v", err)
	}
	return cliente
}

// endpointDoSqs devolve o endereco do LocalStack.
func endpointDoSqs() string {
	if valor := os.Getenv("WAGER_TEST_SQS_ENDPOINT"); valor != "" {
		return valor
	}
	return "http://localhost:4566"
}

// limpar esvazia a fila de saida.
//
// Esvaziar e o primeiro passo de cada teste: uma mensagem deixada pelo teste
// anterior seria lida aqui e o resultado passaria a depender da ordem de execucao.
func limpar(t *testing.T) {
	t.Helper()

	cliente, err := sqs.Novo(contexto(t), sqs.Opcoes{
		Endpoint:        endpointDoSqs(),
		Regiao:          "us-east-1",
		ChaveDeAcesso:   "test",
		SegredoDeAcesso: "test",
		FilaOperacoes:   filaOperacoes,
		FilaDeadLetter:  filaOperacoesDLQ,
	})
	if err != nil {
		t.Fatalf("cliente da fila de operacoes: %v", err)
	}

	saida := filaDeEventos(t)
	esvaziar(t, cliente)
	esvaziar(t, saida)
}

// esvaziar remove as mensagens visiveis de uma fila.
func esvaziar(t *testing.T, cliente *sqs.Cliente) {
	t.Helper()
	for i := 0; i < 40; i++ {
		mensagens, err := cliente.Receber(contexto(t), 10)
		if err != nil {
			t.Fatalf("recebimento na limpeza: %v", err)
		}
		if len(mensagens) == 0 {
			return
		}
		for _, mensagem := range mensagens {
			if err := cliente.Concluir(contexto(t), mensagem.ReceiptHandle); err != nil {
				t.Fatalf("limpeza: %v", err)
			}
		}
	}
}

// eventoParaGravar monta um evento valido sem grava-lo.
//
// Separar a construcao da gravacao existe porque o teste do relay precisa do
// registro antes de confirma-lo, para provar que o relay nao ve o que ainda esta
// dentro de uma transacao aberta.
func eventoParaGravar(t *testing.T) eventos.Evento {
	t.Helper()

	valor, err := money.Parse("25.00", money.CurrencyBRL)
	if err != nil {
		t.Fatalf("money.Parse: %v", err)
	}

	evento, err := eventos.NovaTransacaoProcessada(eventos.DadosTransacaoProcessada{
		Transacao:  idValido(t, uuid.NewString()),
		Provedor:   "provider-a",
		Externo:    uuid.NewString(),
		Carteira:   idValido(t, uuid.NewString()),
		Tipo:       "BET",
		Resultado:  valor,
		Correlacao: uuid.NewString(),
		OcorridoEm: agoraTeste(),
	})
	if err != nil {
		t.Fatalf("NovoTransacaoProcessada: %v", err)
	}
	return evento
}

// gravarEvento grava um evento na outbox em uma unidade que confirma.
//
// E o teste que escreve direto na outbox, sem passar pelo caso de uso, porque o que
// esta em prova e o relay e nao a operacao que originou o evento.
func gravarEvento(t *testing.T, u *pg.Unidade) eventos.Evento {
	t.Helper()

	evento := eventoParaGravar(t)
	if err := u.Executar(contexto(t), func(q pg.Querente) error {
		return pg.NovaRepositorioOutbox().Inserir(contexto(t), q, evento)
	}); err != nil {
		t.Fatalf("insercao na outbox: %v", err)
	}
	return evento
}

// agoraTeste e um instante fixo.
func agoraTeste() time.Time {
	return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
}

// idValido converte texto em identificador.
func idValido(t *testing.T, texto string) wallet.Identificador {
	t.Helper()
	id, err := wallet.IdentificadorDe(texto)
	if err != nil {
		t.Fatalf("IdentificadorDe(%q): %v", texto, err)
	}
	return id
}

// dinheiro devolve um Money a partir de centavos.
func dinheiro(t *testing.T, centavos int64, moeda money.Currency) money.Money {
	t.Helper()
	texto := fmt.Sprintf("%d.%02d", centavos/100, centavos%100)
	valor, err := money.Parse(texto, moeda)
	if err != nil {
		t.Fatalf("money.Parse(%q, %q): %v", texto, moeda, err)
	}
	return valor
}

// garantirFilaDeEventos verifica que a fila de saida existe e Skip se faltar.
//
// O Skip e deliberado: a fila de saida e provisionada por codigo no init do
// LocalStack, e um ambiente provisionado antes da E15 nao a tem. Falhar aqui seria
// falhar por causa do ambiente e nao por causa do relay, que e exatamente o que o
// Makefile proibiu com o `-p 1`.
func garantirFilaDeEventos(t *testing.T) {
	t.Helper()

	_, err := sqs.Novo(contexto(t), sqs.Opcoes{
		Endpoint:        endpointDoSqs(),
		Regiao:          "us-east-1",
		ChaveDeAcesso:   "test",
		SegredoDeAcesso: "test",
		FilaOperacoes:   filaEventos,
		FilaDeadLetter:  filaEventosDLQ,
	})
	if err == nil {
		return
	}
	if !errors.Is(err, sqs.ErrFilaInexistente) {
		t.Fatalf("verificacao da fila de eventos: %v", err)
	}
	t.Skipf("a fila %s nao esta provisionada no ambiente: %v", filaEventos, err)
}
