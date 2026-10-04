package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/config"
)

func ambienteValido() map[string]string {
	return map[string]string{
		"WAGER_HTTP_ADDRESS":          "127.0.0.1:0",
		"WAGER_HTTP_READ_TIMEOUT":     "1s",
		"WAGER_HTTP_WRITE_TIMEOUT":    "1s",
		"WAGER_HTTP_IDLE_TIMEOUT":     "1s",
		"WAGER_HTTP_SHUTDOWN_TIMEOUT": "1s",
		"WAGER_LOG_LEVEL":             "info",
	}
}

func novoApp(t *testing.T, getenv func(string) (string, bool)) (*app, *Eventos) {
	t.Helper()

	eventos := &Eventos{}
	a, err := NewFromEnv(getenv, eventos)
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	return a, eventos
}

// A aplicacao precisa subir e encerrar sem regra de negocio. E o que a E1
// entrega: o esqueleto, e nao o jogo.
func TestAplicacaoSobeEEncerra(t *testing.T) {
	a, eventos := novoApp(t, func(c string) (string, bool) {
		v, ok := ambienteValido()[c]
		return v, ok
	})

	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := a.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if !eventos.Subiu() {
		t.Error("a aplicacao encerrou sem ter iniciado")
	}
	if !eventos.Encerrou() {
		t.Error("a aplicacao encerrou sem registrar o encerramento")
	}
}

// O que importa no shutdown e a ordem: nada pode encerrar antes de subir, e o
// encerramento tem de acontecer depois do inicio, sempre.
func TestShutdownOcorreDepoisDoStartup(t *testing.T) {
	a, eventos := novoApp(t, func(c string) (string, bool) {
		v, ok := ambienteValido()[c]
		return v, ok
	})

	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := a.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	obtidos := eventos.Eventos()
	if len(obtidos) < 2 {
		t.Fatalf("eventos registrados: %v", obtidos)
	}
	if obtidos[0] != EventoSubida {
		t.Errorf("primeiro evento e %q, esperado %q", obtidos[0], EventoSubida)
	}
	if obtidos[len(obtidos)-1] != EventoEncerramento {
		t.Errorf("ultimo evento e %q, esperado %q", obtidos[len(obtidos)-1], EventoEncerramento)
	}
}

// Um ciclo so pode valer uma vez. Rodar Stop duas vezes sem Start no meio
// significa que o shutdown nao e idempotente, e quem sofre com isso e o
// operator, no meio de um incidente.
func TestEncerrarSemSubirNaoRegistraEncerramento(t *testing.T) {
	a, eventos := novoApp(t, func(c string) (string, bool) {
		v, ok := ambienteValido()[c]
		return v, ok
	})

	if err := a.Stop(t.Context()); err != nil {
		t.Fatalf("Stop sem Start: %v", err)
	}

	if eventos.Encerrou() {
		t.Error("registrou encerramento sem ter registrado subida")
	}
}

// Falhar cedo e o ponto da borda: configuracao invalida tem de barrar a subida
// antes de qualquer recurso externo ser aberto, e o erro tem de dizer qual
// variavel corrigir.
func TestConfiguracaoInvalidaBarraAMontagem(t *testing.T) {
	ambiente := ambienteValido()
	ambiente["WAGER_HTTP_ADDRESS"] = "sem-porta"

	_, err := NewFromEnv(func(c string) (string, bool) {
		v, ok := ambiente[c]
		return v, ok
	}, &Eventos{})

	var invalida *config.ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado *config.ValidationError", err, err)
	}
	if invalida.Field != "WAGER_HTTP_ADDRESS" {
		t.Errorf("Field e %q, esperado %q", invalida.Field, "WAGER_HTTP_ADDRESS")
	}
}

func TestConfiguracaoValidaMontaAplicacao(t *testing.T) {
	a, _ := novoApp(t, func(c string) (string, bool) {
		v, ok := ambienteValido()[c]
		return v, ok
	})

	if a == nil {
		t.Fatal("NewFromEnv devolveu aplicacao nula")
	}
}

// A montagem nao pode depender do ambiente global, senao o teste e a aplicacao
// compartilham estado e o teste passa por acidente.
func TestEventosComecamVazios(t *testing.T) {
	eventos := &Eventos{}

	if got := eventos.Eventos(); len(got) != 0 {
		t.Errorf("Eventos() comeca com %v, esperado lista vazia", got)
	}
	if eventos.Subiu() || eventos.Encerrou() {
		t.Error("Eventos{} ja nasce marcando subida ou encerramento")
	}
}

func TestEventosPreservaAOremRegistrada(t *testing.T) {
	eventos := &Eventos{}

	eventos.registrar(EventoSubida)
	eventos.registrar(EventoEncerramento)

	obtidos := eventos.Eventos()
	if !reflect.DeepEqual(obtidos, []Evento{EventoSubida, EventoEncerramento}) {
		t.Errorf("Eventos() e %v, esperado [%v %v]", obtidos, EventoSubida, EventoEncerramento)
	}
}
