//go:build integration

package repositorios

import (
	"errors"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/eventos"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// eventoValido cria um evento processado, o tipo mais comum na outbox.
func eventoValido(t *testing.T, transacao wagering.Transacao) eventos.Evento {
	t.Helper()

	e, err := eventos.NovaTransacaoProcessada(eventos.DadosTransacaoProcessada{
		Transacao:  transacao.ID(),
		Provedor:   "provider-a",
		Externo:    "transaction-123",
		Carteira:   transacao.Carteira(),
		Tipo:       "BET",
		Resultado:  dinheiro(t, 7500, "BRL"),
		Correlacao: "corr-123",
		OcorridoEm: agoraTeste(),
	})
	if err != nil {
		t.Fatalf("NovoTransacaoProcessada: %v", err)
	}
	return e
}

// Inbox e outbox compartilham a unidade de trabalho. E o que faz o registro da
// mensagem e a alteracao financeira serem confirmados juntos.
func TestInboxEOutboxNaMesmaUnidade(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	inbox := pg.NovaRepositorioInbox()
	outbox := pg.NovaRepositorioOutbox()
	transacoes := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	tr := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	evento := eventoValido(t, tr)

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := transacoes.Inserir(ctx, q, tr); err != nil {
			return err
		}
		if err := inbox.Registrar(ctx, q, "wager-consumer", "msg-1", "hash-msg", agoraTeste()); err != nil {
			return err
		}
		return outbox.Inserir(ctx, q, evento)
	}); err != nil {
		t.Fatalf("gravacao conjunta: %v", err)
	}

	// Tudo confirmado junto.
	var (
		concluida bool
		reservas  []pg.RegistroPendente
	)
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		concluida, err = inbox.JaConcluida(ctx, q, "wager-consumer", "msg-1")
		if err != nil {
			return err
		}
		reservas, err = outbox.Reservar(ctx, q, "publisher-1", 10, 30*time.Second, agoraTeste())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}
	if concluida {
		t.Error("a inbox foi concluida sem o tratamento: Registrar nao conclui")
	}
	if len(reservas) != 1 {
		t.Fatalf("eventos pendentes e %d, esperado 1", len(reservas))
	}
	if reservas[0].Evento.EventID() != evento.EventID() {
		t.Errorf("eventId e %s, esperado %s", reservas[0].Evento.EventID(), evento.EventID())
	}
}

// Se a unidade falhar, nem o registro da inbox nem o evento saem. E o que
// garante que uma mensagem reentregue nao produza efeito duplicado.
func TestFalhaNaUnidadeNaoDeixaNemInboxNemOutbox(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	inbox := pg.NovaRepositorioInbox()
	outbox := pg.NovaRepositorioOutbox()
	transacoes := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	tr := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	evento := eventoValido(t, tr)

	erroEsperado := errors.New("falha depois das escritas")
	err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := transacoes.Inserir(ctx, q, tr); err != nil {
			return err
		}
		if err := inbox.Registrar(ctx, q, "wager-consumer", "msg-1", "hash-msg", agoraTeste()); err != nil {
			return err
		}
		if err := outbox.Inserir(ctx, q, evento); err != nil {
			return err
		}
		return erroEsperado
	})
	if !errors.Is(err, erroEsperado) {
		t.Fatalf("devolveu %v, esperado o erro da funcao", err)
	}

	// Nada sobrou: nem a transacao, nem a inbox, nem o evento.
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		if _, err := transacoes.BuscarPorID(ctx, q, tr.ID()); err == nil {
			return errors.New("a transacao sobreviveu")
		}
		return nil
	}); err != nil {
		t.Errorf("%v", err)
	}

	var reservas []pg.RegistroPendente
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		reservas, err = outbox.Reservar(ctx, q, "publisher-1", 10, 30*time.Second, agoraTeste())
		return err
	}); err != nil {
		t.Fatalf("leitura da outbox: %v", err)
	}
	if len(reservas) != 0 {
		t.Errorf("a outbox tem %d eventos depois de uma unidade desfeita", len(reservas))
	}
}

// Reentrega da mesma mensagem nao e erro: e o caso comum do at-least-once. E o
// unico ON CONFLICT DO NOTHING do projeto, e o contraste com a transacao de apostas
// e deliberado.
func TestReentregaDaMesmaMensagemNaoEError(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	inbox := pg.NovaRepositorioInbox()
	ctx := contexto(t)

	for i := 0; i < 3; i++ {
		if err := unidade.Executar(ctx, func(q pg.Querente) error {
			return inbox.Registrar(ctx, q, "wager-consumer", "msg-1", "hash-msg", agoraTeste())
		}); err != nil {
			t.Fatalf("entrega %d: %v", i, err)
		}
	}

	// A mesma mensagem para outro consumidor e outra linha: a chave primaria e o
	// par consumidor e mensagem.
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return inbox.Registrar(ctx, q, "outro-consumidor", "msg-1", "hash-msg", agoraTeste())
	}); err != nil {
		t.Fatalf("outro consumidor: %v", err)
	}
}

func TestInboxConcluiEmissao(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	inbox := pg.NovaRepositorioInbox()
	ctx := contexto(t)

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := inbox.Registrar(ctx, q, "wager-consumer", "msg-1", "hash", agoraTeste()); err != nil {
			return err
		}
		return inbox.Concluir(ctx, q, "wager-consumer", "msg-1", agoraTeste())
	}); err != nil {
		t.Fatalf("conclusao: %v", err)
	}

	var concluida bool
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		concluida, err = inbox.JaConcluida(ctx, q, "wager-consumer", "msg-1")
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}
	if !concluida {
		t.Error("a inbox nao registrou a conclusao")
	}

	// Concluir o que nao existe e erro.
	err := unidade.Executar(ctx, func(q pg.Querente) error {
		return inbox.Concluir(ctx, q, "wager-consumer", "msg-inexistente", agoraTeste())
	})
	if !errors.Is(err, pg.ErrNaoEncontrado) {
		t.Errorf("devolveu %v, esperado ErrNaoEncontrado", err)
	}
}

// Dois publishers pegam conjuntos diferentes sem esperar um pelo outro. E o que
// FOR UPDATE SKIP LOCKED compra: com espera, um publisher lento serializaria todos
// os outros.
func TestDoisPublishersPegamRegistrosDiferentes(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	outbox := pg.NovaRepositorioOutbox()
	transacoes := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	// Quatro eventos pendentes.
	for i := 0; i < 4; i++ {
		tr := betValida(t, carteira.ID(), carteira.Jogador(),
			"transaction-"+string(rune('a'+i)), "chave-"+string(rune('a'+i)), "hash", "25.00")
		evento := eventoValido(t, tr)
		if err := unidade.Executar(ctx, func(q pg.Querente) error {
			if err := transacoes.Inserir(ctx, q, tr); err != nil {
				return err
			}
			return outbox.Inserir(ctx, q, evento)
		}); err != nil {
			t.Fatalf("evento %d: %v", i, err)
		}
	}

	var (
		primeiro []pg.RegistroPendente
		segundo  []pg.RegistroPendente
	)
	// A reserva do primeiro publisher fica com transacao aberta, e a do segundo
	// acontece antes de confirmar a primeira: e o scenario de dois relays vivos ao
	// mesmo tempo.
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		var err error
		primeiro, err = outbox.Reservar(ctx, q, "publisher-1", 2, 30*time.Second, agoraTeste())
		return err
	}); err != nil {
		t.Fatalf("reserva do primeiro: %v", err)
	}

	// O segundo publisher ve apenas o que sobrou, porque a reserva do primeiro
	// devient leased_until no futuro.
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		segundo, err = outbox.Reservar(ctx, q, "publisher-2", 10, 30*time.Second, agoraTeste())
		return err
	}); err != nil {
		t.Fatalf("reserva do segundo: %v", err)
	}

	if len(primeiro) != 2 {
		t.Errorf("o primeiro publisher pegou %d, esperado 2", len(primeiro))
	}
	if len(segundo) != 2 {
		t.Errorf("o segundo publisher pegou %d, esperado 2", len(segundo))
	}

	vistos := map[string]int{}
	for _, r := range append(primeiro, segundo...) {
		vistos[r.Evento.EventID().String()]++
	}
	if len(vistos) != 4 {
		t.Errorf("eventos distintos e %d, esperado 4: os publishers pegaram o mesmo registro", len(vistos))
	}
}

// Um publisher que morre depois de reservar nao pode deixar o registro preso para
// sempre. A janela de reserva e o que permite a recuperacao.
func TestReservaVencidaEhAssumidaPorOutro(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	outbox := pg.NovaRepositorioOutbox()
	transacoes := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	tr := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := transacoes.Inserir(ctx, q, tr); err != nil {
			return err
		}
		return outbox.Inserir(ctx, q, eventoValido(t, tr))
	}); err != nil {
		t.Fatalf("gravacao: %v", err)
	}

	// O publisher 1 reserva e morre sem confirmar.
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		_, err := outbox.Reservar(ctx, q, "publisher-1", 10, time.Second, agoraTeste())
		return err
	}); err != nil {
		t.Fatalf("reserva: %v", err)
	}

	// Antes do vencimento, ninguem pega.
	var antes []pg.RegistroPendente
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		antes, err = outbox.Reservar(ctx, q, "publisher-2", 10, time.Second, agoraTeste().Add(500*time.Millisecond))
		return err
	}); err != nil {
		t.Fatalf("leitura antes do vencimento: %v", err)
	}
	if len(antes) != 0 {
		t.Errorf("o publisher 2 pegou %d registros antes da reserva vencer", len(antes))
	}

	// Depois do vencimento, o registro e assumido.
	var depois []pg.RegistroPendente
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		depois, err = outbox.Reservar(ctx, q, "publisher-2", 10, time.Second, agoraTeste().Add(2*time.Second))
		return err
	}); err != nil {
		t.Fatalf("leitura depois do vencimento: %v", err)
	}
	if len(depois) != 1 {
		t.Fatalf("o publisher 2 pegou %d registros depois do vencimento, esperado 1", len(depois))
	}
}

// Republicar preserva o eventId. E o que permite ao consumidor reconhecer que e o
// mesmo evento e nao um novo.
func TestRepublicacaoPreservaOEventID(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	outbox := pg.NovaRepositorioOutbox()
	transacoes := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	tr := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash", "25.00")
	evento := eventoValido(t, tr)
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := transacoes.Inserir(ctx, q, tr); err != nil {
			return err
		}
		return outbox.Inserir(ctx, q, evento)
	}); err != nil {
		t.Fatalf("gravacao: %v", err)
	}

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		lista, err := outbox.Reservar(ctx, q, "publisher-1", 10, time.Second, agoraTeste())
		if err != nil {
			return err
		}
		if len(lista) != 1 {
			return errors.New("reserva do primeiro publisher nao devolveu exatamente um registro")
		}
		return nil
	}); err != nil {
		t.Fatalf("reserva: %v", err)
	}

	// O relay publicou e morreu antes de confirmar. O registro volta para a fila.
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return outbox.Reprogramar(ctx, q, evento.EventID(), agoraTeste())
	}); err != nil {
		t.Fatalf("reprogramacao: %v", err)
	}

	var republicado pg.RegistroPendente
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		lista, err := outbox.Reservar(ctx, q, "publisher-2", 10, time.Second, agoraTeste())
		if err != nil {
			return err
		}
		republicado = lista[0]
		return nil
	}); err != nil {
		t.Fatalf("reserva do republicado: %v", err)
	}
	if republicado.Evento.EventID() != evento.EventID() {
		t.Errorf("eventId mudou na republicacao: %s virou %s",
			evento.EventID(), republicado.Evento.EventID())
	}
	if republicado.Evento.Tipo() != evento.Tipo() {
		t.Errorf("tipo mudou: %q virou %q", evento.Tipo(), republicado.Evento.Tipo())
	}
	if republicado.Tentativas != 1 {
		t.Errorf("tentativas e %d, esperado 1", republicado.Tentativas)
	}
}

// Confirmar publica e marca. O registro so e confirmado depois da publicacao,
// nunca antes: confirmar antes e o caminho para perder o evento.
func TestConfirmarPublicacaoMarcaORegistro(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	outbox := pg.NovaRepositorioOutbox()
	transacoes := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	tr := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash", "25.00")
	evento := eventoValido(t, tr)
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := transacoes.Inserir(ctx, q, tr); err != nil {
			return err
		}
		return outbox.Inserir(ctx, q, evento)
	}); err != nil {
		t.Fatalf("gravacao: %v", err)
	}

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if _, err := outbox.Reservar(ctx, q, "publisher-1", 10, time.Second, agoraTeste()); err != nil {
			return err
		}
		return outbox.ConfirmarPublicacao(ctx, q, evento.EventID(), agoraTeste())
	}); err != nil {
		t.Fatalf("confirmacao: %v", err)
	}

	// Confirmado nao volta para a fila.
	var pendentes []pg.RegistroPendente
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		pendentes, err = outbox.Reservar(ctx, q, "publisher-2", 10, time.Second, agoraTeste())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}
	if len(pendentes) != 0 {
		t.Errorf("a outbox tem %d pendentes depois de confirmar", len(pendentes))
	}

	// Confirmar duas vezes e erro: o registro ja saiu da fila.
	err := unidade.Executar(ctx, func(q pg.Querente) error {
		return outbox.ConfirmarPublicacao(ctx, q, evento.EventID(), agoraTeste())
	})
	if !errors.Is(err, pg.ErrNaoEncontrado) {
		t.Errorf("devolveu %v, esperado ErrNaoEncontrado", err)
	}
}

// Um evento invalido nao entra na outbox. Evento com payload que nao serializa
// ficaria na fila e falharia no relay, que e o pior lugar para descobrir.
func TestOutboxRecusaEventoInvalido(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	outbox := pg.NovaRepositorioOutbox()
	ctx := contexto(t)

	err := unidade.Executar(ctx, func(q pg.Querente) error {
		return outbox.Inserir(ctx, q, eventos.Evento{})
	})
	if err == nil {
		t.Fatal("a outbox aceitou evento invalido")
	}

}
