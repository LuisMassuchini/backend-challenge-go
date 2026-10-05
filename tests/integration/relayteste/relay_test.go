//go:build integration

package relayteste

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/outbox"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// novoRelay monta um relay com o publicador informado.
func novoRelay(t *testing.T, u *pg.Unidade, publicador outbox.Publicador, editor string) *outbox.Relay {
	t.Helper()
	return comOpcoes(t, u, publicador, editor, 10, time.Second, 5, nil)
}

// comOpcoes monta um relay com os ajustes pedidos pelo teste.
func comOpcoes(
	t *testing.T,
	u *pg.Unidade,
	publicador outbox.Publicador,
	editor string,
	lote int,
	backoff time.Duration,
	tentativas int,
	relogio outbox.Relogio,
) *outbox.Relay {
	t.Helper()

	relay, err := outbox.Novo(outbox.Dependencias{
		Unidade:          u,
		Outbox:           pg.NovaRepositorioOutbox(),
		Publicador:       publicador,
		Editor:           editor,
		Lote:             lote,
		Ocioso:           100 * time.Millisecond,
		Janela:           30 * time.Second,
		Backoff:          backoff,
		MaximoTentativas: tentativas,
		Relogio:          relogio,
	})
	if err != nil {
		t.Fatalf("montagem do relay: %v", err)
	}
	return relay
}

// publicadorFalso registra o que o relay mandou publicar, na ordem.
type publicadorFalso struct {
	// mensagens sao as chamadas, no formato chave de particao, corpo.
	mensagens []mensagemPublicada

	// falha faz toda publicacao falhar, como broker fora.
	falha error
}

// mensagemPublicada e uma chamada registrada.
type mensagemPublicada struct {
	chaveDeParticao string
	corpo           string
}

// Publicar registra ou falha.
func (p *publicadorFalso) Publicar(_ context.Context, chave, corpo string) error {
	if p.falha != nil {
		return p.falha
	}
	p.mensagens = append(p.mensagens, mensagemPublicada{chaveDeParticao: chave, corpo: corpo})
	return nil
}

// IDs devolve os eventIds publicados, na ordem.
func (p *publicadorFalso) IDs(t *testing.T) []string {
	t.Helper()
	ids := make([]string, 0, len(p.mensagens))
	for _, mensagem := range p.mensagens {
		var envelope map[string]any
		if err := json.Unmarshal([]byte(mensagem.corpo), &envelope); err != nil {
			t.Fatalf("corpo publicado nao e JSON: %v", err)
		}
		id, ok := envelope["eventId"].(string)
		if !ok {
			t.Fatalf("o envelope publicado nao tem eventId: %v", envelope)
		}
		ids = append(ids, id)
	}
	return ids
}

// relogioAdiantado devolve um instante fixo no futuro.
//
// Existe para o teste da reserva vencida: o relay so reassume um registro cuja
// janela de reserva ja passou, e a janela e medida contra o relogio do relay. Sem
// poder adiantar o tempo, o teste teria de esperar a janela inteira.
type relogioAdiantado struct{ delta time.Duration }

// Agora devolve o instante adiantado.
func (r relogioAdiantado) Agora() time.Time {
	return agoraTeste().Add(r.delta)
}

// O relay publica o que foi gravado no commit e so entao marca como publicado.
//
// E a propriedade central da transactional outbox: o evento esta em disco antes de
// qualquer chamada ao broker, e o registro so sai da fila depois da publicacao.
func TestRelayPublicaEConfirma(t *testing.T) {
	dbtest.Limpa(t)
	garantirFilaDeEventos(t)

	u := unidade(t)
	publicador := &publicadorFalso{}
	evento := gravarEvento(t, u)

	publicados, err := novoRelay(t, u, publicador, "publisher-1").Ciclo(contexto(t))
	if err != nil {
		t.Fatalf("ciclo do relay: %v", err)
	}
	if publicados != 1 {
		t.Fatalf("o relay publicou %d eventos, esperado 1", publicados)
	}

	ids := publicador.IDs(t)
	if len(ids) != 1 {
		t.Fatalf("o publicador recebeu %d mensagens, esperado 1", len(ids))
	}
	if ids[0] != evento.EventID().String() {
		t.Errorf("eventId publicado e %s, esperado %s", ids[0], evento.EventID())
	}

	// O corpo publicado e o envelope gravado: o relay publica o snapshot, e nao um
	// evento remontado na hora da publicacao.
	var envelope map[string]any
	if err := json.Unmarshal([]byte(publicador.mensagens[0].corpo), &envelope); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	for _, campo := range []string{"eventId", "eventType", "aggregateId", "correlationId", "occurredAt", "version", "data"} {
		if _, existe := envelope[campo]; !existe {
			t.Errorf("o envelope publicado nao tem o campo %s", campo)
		}
	}

	// Confirmado no banco: o registro nao volta para a fila.
	var publicado *time.Time
	if err := u.Ler(contexto(t), func(q pg.Querente) error {
		return q.QueryRow(contexto(t),
			"SELECT published_at FROM outbox_events WHERE id = $1", evento.EventID().UUID(),
		).Scan(&publicado)
	}); err != nil {
		t.Fatalf("leitura do registro publicado: %v", err)
	}
	if publicado == nil {
		t.Error("published_at continua vazio depois de confirmar a publicacao")
	}

	var pendentes int
	if err := u.Ler(contexto(t), func(q pg.Querente) error {
		return q.QueryRow(contexto(t),
			"SELECT count(*) FROM outbox_events WHERE published_at IS NULL",
		).Scan(&pendentes)
	}); err != nil {
		t.Fatalf("contagem de pendentes: %v", err)
	}
	if pendentes != 0 {
		t.Errorf("a outbox tem %d pendentes depois de confirmar", pendentes)
	}
}

// Nenhum evento e publicado antes do commit que o originou.
//
// O evento so passa a existir na outbox quando a unidade confirma, entao a garantia
// vem da estrutura. O que o teste prova e o outro lado, que e o que importa com o
// relay em outra conexao: um relay que varre a outbox enquanto a transacao financeira
// ainda esta aberta nao ve nada.
func TestRelayNaoPublicaAntesDoCommit(t *testing.T) {
	dbtest.Limpa(t)
	garantirFilaDeEventos(t)

	u := unidade(t)
	publicador := &publicadorFalso{}

	// Grava o evento e falha no fim da unidade: nada e confirmado.
	erroEsperado := errors.New("falha depois da gravacao do evento")
	repositorio := pg.NovaRepositorioOutbox()
	evento := eventoParaGravar(t)

	err := u.Executar(contexto(t), func(q pg.Querente) error {
		if err := repositorio.Inserir(contexto(t), q, evento); err != nil {
			return err
		}
		return erroEsperado
	})
	if !errors.Is(err, erroEsperado) {
		t.Fatalf("a unidade devolveu %v, esperado o erro da funcao", err)
	}

	publicados, err := novoRelay(t, u, publicador, "publisher-1").Ciclo(contexto(t))
	if err != nil {
		t.Fatalf("ciclo do relay: %v", err)
	}
	if publicados != 0 {
		t.Fatalf("o relay publicou %d eventos de uma unidade desfeita", publicados)
	}
	if len(publicador.mensagens) != 0 {
		t.Errorf("o publicador recebeu %d mensagens de uma unidade desfeita", len(publicador.mensagens))
	}
}

// Falha de publicacao programa a proxima tentativa e nao marca como publicado. E o
// que faz o relay recuperar de broker fora sem perder o evento.
func TestFalhaNaPublicacaoReprograma(t *testing.T) {
	dbtest.Limpa(t)
	garantirFilaDeEventos(t)

	u := unidade(t)
	evento := gravarEvento(t, u)
	publicador := &publicadorFalso{falha: errors.New("broker indisponivel")}

	if _, err := novoRelay(t, u, publicador, "publisher-1").Ciclo(contexto(t)); err != nil {
		t.Fatalf("ciclo do relay: %v", err)
	}

	var (
		publicado  *time.Time
		tentativas int
		proxima    time.Time
	)
	if err := u.Ler(contexto(t), func(q pg.Querente) error {
		return q.QueryRow(contexto(t),
			`SELECT published_at, attempts, next_attempt_at FROM outbox_events WHERE id = $1`,
			evento.EventID().UUID(),
		).Scan(&publicado, &tentativas, &proxima)
	}); err != nil {
		t.Fatalf("leitura do registro: %v", err)
	}

	if publicado != nil {
		t.Errorf("o evento foi marcado como publicado apesar da falha: %v", publicado)
	}
	if tentativas != 1 {
		t.Errorf("tentativas e %d, esperado 1", tentativas)
	}
	if !proxima.After(agoraTeste()) {
		t.Errorf("next_attempt_at e %v, que nao esta no futuro", proxima)
	}
}

// Quando as tentativas acabam, o evento para de ser retomado e o motivo fica
// gravado para o operador. Sem isso, um evento que o broker recusa para sempre seria
// reentregado sem fim, e o defeito ficaria invisivel.
func TestTentativasEsgotadasParamARetomada(t *testing.T) {
	dbtest.Limpa(t)
	garantirFilaDeEventos(t)

	u := unidade(t)
	evento := gravarEvento(t, u)
	publicador := &publicadorFalso{falha: errors.New("broker recusa o evento")}

	relay := comOpcoes(t, u, publicador, "publisher-1", 10, time.Nanosecond, 1, nil)

	// Duas voltas: a primeira conta a tentativa, a segunda esgota o limite.
	for i := 0; i < 2; i++ {
		if _, err := relay.Ciclo(contexto(t)); err != nil {
			t.Fatalf("ciclo %d: %v", i, err)
		}
	}

	var motivo *string
	if err := u.Ler(contexto(t), func(q pg.Querente) error {
		return q.QueryRow(contexto(t),
			"SELECT failure_reason FROM outbox_events WHERE id = $1", evento.EventID().UUID(),
		).Scan(&motivo)
	}); err != nil {
		t.Fatalf("leitura do motivo: %v", err)
	}
	if motivo == nil || *motivo == "" {
		t.Fatal("a falha permanente nao foi registrada")
	}
	if !strings.Contains(*motivo, "broker recusa o evento") {
		t.Errorf("o motivo registrado e %q, esperado que citasse a falha do broker", *motivo)
	}

	// Uma terceira volta nao tenta mais nada.
	publicados, err := relay.Ciclo(contexto(t))
	if err != nil {
		t.Fatalf("terceira volta: %v", err)
	}
	if publicados != 0 {
		t.Errorf("o relay ainda tentou %d eventos depois de esgotar as tentativas", publicados)
	}
}

// Dois relays vivos pegam registros distintos e nenhum evento e publicado duas vezes.
// E o que o FOR UPDATE SKIP LOCKED compra.
func TestDoisRelaysNaoPublicamODuplicado(t *testing.T) {
	dbtest.Limpa(t)
	garantirFilaDeEventos(t)

	u := unidade(t)
	esperados := map[string]bool{}
	for i := 0; i < 4; i++ {
		esperados[gravarEvento(t, u).EventID().String()] = true
	}

	p1 := &publicadorFalso{}
	p2 := &publicadorFalso{}

	// Cada relay com sua propria unidade: e a condicao real de tres instancias, cada
	// uma com suas conexoes e sua memoria.
	relay1 := comOpcoes(t, u, p1, "publisher-1", 2, time.Second, 5, nil)
	relay2 := comOpcoes(t, unidade(t), p2, "publisher-2", 2, time.Second, 5, nil)

	n1, err := relay1.Ciclo(contexto(t))
	if err != nil {
		t.Fatalf("ciclo do primeiro: %v", err)
	}
	n2, err := relay2.Ciclo(contexto(t))
	if err != nil {
		t.Fatalf("ciclo do segundo: %v", err)
	}
	if n1 != 2 || n2 != 2 {
		t.Fatalf("os relays publicaram %d e %d, esperado 2 e 2", n1, n2)
	}

	vistos := map[string]int{}
	for _, publicador := range []*publicadorFalso{p1, p2} {
		for _, id := range publicador.IDs(t) {
			vistos[id]++
			if !esperados[id] {
				t.Errorf("o relay publicou o evento %s, que nao estava na outbox", id)
			}
		}
	}
	if len(vistos) != 4 {
		t.Errorf("eventos distintos publicados e %d, esperado 4", len(vistos))
	}
	for id, quantas := range vistos {
		if quantas > 1 {
			t.Errorf("o evento %s foi publicado %d vezes", id, quantas)
		}
	}
}

// A chave de particao e o agregado do evento. E o que preserva a ordem por agregado
// na fila de saida, pelo mesmo motivo da fila de entrada: duas operacoes da mesma
// carteira sao relacionadas, e uma reversao depende da aposta.
func TestChaveDeParticaoEoAgregado(t *testing.T) {
	dbtest.Limpa(t)
	garantirFilaDeEventos(t)

	u := unidade(t)
	publicador := &publicadorFalso{}
	evento := gravarEvento(t, u)

	if _, err := novoRelay(t, u, publicador, "publisher-1").Ciclo(contexto(t)); err != nil {
		t.Fatalf("ciclo do relay: %v", err)
	}
	if len(publicador.mensagens) != 1 {
		t.Fatalf("o publicador recebeu %d mensagens", len(publicador.mensagens))
	}

	grupo := publicador.mensagens[0].chaveDeParticao
	if grupo != evento.AggregateID().String() {
		t.Errorf("chave de particao e %s, esperado o agregado %s", grupo, evento.AggregateID())
	}
}

// O relay assume o trabalho abandonado por outro publisher que morreu depois de
// reservar. E o que impede que um evento fique preso para sempre.
//
// A reserva vencida e testada reserving pela metade: o teste reserva direto, sem
// publicar nem confirmar, que e exatamente o estado em que um publisher deixa a
// linha quando o processo morre entre reservar e publicar.
func TestRelayAssumeReservaVencida(t *testing.T) {
	dbtest.Limpa(t)
	garantirFilaDeEventos(t)

	u := unidade(t)
	evento := gravarEvento(t, u)

	// O publisher 1 reserva e morre sem publicar nem confirmar.
	if err := u.Executar(contexto(t), func(q pg.Querente) error {
		_, err := pg.NovaRepositorioOutbox().Reservar(contexto(t), q, "publisher-1", 10, 10*time.Second, agoraTeste())
		return err
	}); err != nil {
		t.Fatalf("reserva do publisher que morre: %v", err)
	}

	// Antes do vencimento, ninguem pega.
	//
	// Os dois relays usam relogio proprio em vez do relogio do sistema, porque a
	// janela de reserva e medida contra o instante da reserva, que aqui e o instante
	// fixo do teste. Com o relogio do sistema a janela ja teria vencido antes do
	// primeiro ciclo, e o teste mediria a outra coisa.
	cedo := comOpcoes(t, u, &publicadorFalso{}, "publisher-2", 10, time.Second, 5,
		relogioAdiantado{5 * time.Second})
	if _, err := cedo.Ciclo(contexto(t)); err != nil {
		t.Fatalf("ciclo antes do vencimento: %v", err)
	}

	// Depois do vencimento, outro relay assume e publica com o mesmo eventId.
	publicador := &publicadorFalso{}
	assumidor := comOpcoes(t, u, publicador, "publisher-3", 10, time.Second, 5,
		relogioAdiantado{30 * time.Second})
	publicados, err := assumidor.Ciclo(contexto(t))
	if err != nil {
		t.Fatalf("ciclo do relay que assumiu: %v", err)
	}
	if publicados != 1 {
		t.Fatalf("o relay que assumiu publicou %d, esperado 1", publicados)
	}

	ids := publicador.IDs(t)
	if len(ids) != 1 || ids[0] != evento.EventID().String() {
		t.Errorf("o relay publicou %v, esperado o mesmo eventId %s", ids, evento.EventID())
	}
}

// Republicacao apos a falha entre publicar e confirmar preserva o eventId.
//
// Sem isso, um relay que publica e morre antes de confirmar produziria um segundo
// evento com identidade diferente, e o consumidor nao teria como reconhecer que e o
// mesmo.
func TestRepublicacaoPreservaOEventID(t *testing.T) {
	dbtest.Limpa(t)
	garantirFilaDeEventos(t)

	u := unidade(t)
	evento := gravarEvento(t, u)
	repositorio := pg.NovaRepositorioOutbox()

	// O relay reserva e publica; a confirmacao falha, como um processo que morre
	// entre os dois passos. O registro volta para a fila.
	if err := u.Executar(contexto(t), func(q pg.Querente) error {
		reservados, err := repositorio.Reservar(contexto(t), q, "publisher-1", 10, time.Second, agoraTeste())
		if err != nil {
			return err
		}
		if len(reservados) != 1 {
			return errors.New("a reserva nao devolveu exatamente um registro")
		}
		if err := repositorio.ConfirmarPublicacao(contexto(t), q, evento.EventID(), agoraTeste()); err != nil {
			return err
		}
		// A transacao desfaz: nem a reserva nem a confirmacao sobrevivem.
		return errors.New("o processo morreu antes de confirmar")
	}); err == nil {
		t.Fatal("a unidade deveria ter falhado")
	}

	// O registro segue pendente, com o mesmo eventId gravado.
	publicador := &publicadorFalso{}
	relay := comOpcoes(t, u, publicador, "publisher-2", 10, time.Second, 5,
		relogioAdiantado{30 * time.Second})
	publicados, err := relay.Ciclo(contexto(t))
	if err != nil {
		t.Fatalf("ciclo da republicacao: %v", err)
	}
	if publicados != 1 {
		t.Fatalf("a republicacao publicou %d, esperado 1", publicados)
	}

	ids := publicador.IDs(t)
	if len(ids) != 1 || ids[0] != evento.EventID().String() {
		t.Errorf("a republicacao publicou %v, esperado o mesmo eventId %s", ids, evento.EventID())
	}
}
