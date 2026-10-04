package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// envFixo simula o ambiente com um mapa, para o teste nao depender de variavel
// de ambiente real nem mexer no ambiente do processo.
//
// O segundo retorno segue o formato de os.LookupEnv: chave ausente no mapa e
// variavel nao definida, e chave presente com valor vazio e variavel definida
// e vazia. Sao casos diferentes e o codigo trata os dois.
func envFixo(pares map[string]string) func(string) (string, bool) {
	return func(chave string) (string, bool) {
		valor, definido := pares[chave]
		return valor, definido
	}
}

func TestCarregaValoresPadraoQuandoNadaVemDoAmbiente(t *testing.T) {
	cfg, err := FromEnv(envFixo(map[string]string{}))
	if err != nil {
		t.Fatalf("FromEnv com ambiente vazio: %v", err)
	}

	if cfg.HTTP.Address != ":8080" {
		t.Errorf("Address padrao e %q, esperado %q", cfg.HTTP.Address, ":8080")
	}
	if cfg.Runtime.LogLevel != LogLevelInfo {
		t.Errorf("LogLevel padrao e %q, esperado %q", cfg.Runtime.LogLevel, LogLevelInfo)
	}
	if cfg.HTTP.ReadTimeout != 5*time.Second {
		t.Errorf("ReadTimeout padrao e %v, esperado 5s", cfg.HTTP.ReadTimeout)
	}
}

// O endereco vem do ambiente porque duas instancias de teste disputam a
// mesma porta. A colisao e o sintoma, e o sintoma e dificil de ler.
func TestLeEnderecoEPrazosDoAmbiente(t *testing.T) {
	cfg, err := FromEnv(envFixo(map[string]string{
		"WAGER_HTTP_ADDRESS":          "127.0.0.1:9090",
		"WAGER_HTTP_READ_TIMEOUT":     "3s",
		"WAGER_HTTP_WRITE_TIMEOUT":    "7s",
		"WAGER_HTTP_IDLE_TIMEOUT":     "11s",
		"WAGER_HTTP_SHUTDOWN_TIMEOUT": "25s",
		"WAGER_LOG_LEVEL":             "debug",
	}))
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}

	if cfg.HTTP.Address != "127.0.0.1:9090" {
		t.Errorf("Address e %q, esperado %q", cfg.HTTP.Address, "127.0.0.1:9090")
	}
	if cfg.HTTP.ReadTimeout != 3*time.Second {
		t.Errorf("ReadTimeout e %v, esperado 3s", cfg.HTTP.ReadTimeout)
	}
	if cfg.HTTP.WriteTimeout != 7*time.Second {
		t.Errorf("WriteTimeout e %v, esperado 7s", cfg.HTTP.WriteTimeout)
	}
	if cfg.HTTP.IdleTimeout != 11*time.Second {
		t.Errorf("IdleTimeout e %v, esperado 11s", cfg.HTTP.IdleTimeout)
	}
	if cfg.HTTP.ShutdownTimeout != 25*time.Second {
		t.Errorf("ShutdownTimeout e %v, esperado 25s", cfg.HTTP.ShutdownTimeout)
	}
	if cfg.Runtime.LogLevel != LogLevelDebug {
		t.Errorf("LogLevel e %q, esperado %q", cfg.Runtime.LogLevel, LogLevelDebug)
	}
}

// Validar na borda e o que impede o processo de subir e so descobrir, no
// meio do atendimento do primeiro trafego, que faltava configuracao. Por isso o
// erro precisa nomear a variavel de ambiente a corrigir.
func TestRejeitaEnderecoInvalido(t *testing.T) {
	_, err := FromEnv(envFixo(map[string]string{
		"WAGER_HTTP_ADDRESS": "sem-porta",
	}))

	var invalida *ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado *ValidationError", err, err)
	}
	if invalida.Field != "WAGER_HTTP_ADDRESS" {
		t.Errorf("Field e %q, esperado %q", invalida.Field, "WAGER_HTTP_ADDRESS")
	}
	if invalida.Reason == "" {
		t.Error("Reason vazia: o erro precisa dizer o que fazer")
	}
}

func TestRejeitaDuracaoNaoPositiva(t *testing.T) {
	for _, chave := range []string{
		"WAGER_HTTP_READ_TIMEOUT",
		"WAGER_HTTP_WRITE_TIMEOUT",
		"WAGER_HTTP_IDLE_TIMEOUT",
		"WAGER_HTTP_SHUTDOWN_TIMEOUT",
	} {
		t.Run(chave, func(t *testing.T) {
			_, err := FromEnv(envFixo(map[string]string{chave: "0s"}))

			var invalida *ValidationError
			if !errors.As(err, &invalida) {
				t.Fatalf("erro e %T (%v), esperado *ValidationError", err, err)
			}
			if invalida.Field != chave {
				t.Errorf("Field e %q, esperado %q", invalida.Field, chave)
			}
		})
	}
}

func TestRejeitaDuracaoInvalida(t *testing.T) {
	_, err := FromEnv(envFixo(map[string]string{"WAGER_HTTP_READ_TIMEOUT": "5 segundos"}))

	var invalida *ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado *ValidationError", err, err)
	}
	if invalida.Field != "WAGER_HTTP_READ_TIMEOUT" {
		t.Errorf("Field e %q, esperado %q", invalida.Field, "WAGER_HTTP_READ_TIMEOUT")
	}
}

func TestRejeitaNivelDeLogDesconhecido(t *testing.T) {
	_, err := FromEnv(envFixo(map[string]string{"WAGER_LOG_LEVEL": "verbose"}))

	var invalida *ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado *ValidationError", err, err)
	}
	if invalida.Field != "WAGER_LOG_LEVEL" {
		t.Errorf("Field e %q, esperado %q", invalida.Field, "WAGER_LOG_LEVEL")
	}
}

// Um valor vazio e o caso em que a variavel existe no arquivo de ambiente e
// nao foi preenchida. Ele precisa ser tratado como ausente, nao como
// configuracao valida.
func TestRejeitaEnderecoVazio(t *testing.T) {
	_, err := FromEnv(envFixo(map[string]string{"WAGER_HTTP_ADDRESS": ""}))

	var invalida *ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado *ValidationError", err, err)
	}
	if invalida.Field != "WAGER_HTTP_ADDRESS" {
		t.Errorf("Field e %q, esperado %q", invalida.Field, "WAGER_HTTP_ADDRESS")
	}
}

// A ordem de validacao e fixa, para que dois erros de configuracao sempre
// aparecam na mesma mensagem, e nao na ordem em que as variaveis chegaram.
func TestValidacaoSempreApontaOMesmoCampoPrimeiro(t *testing.T) {
	ambiente := map[string]string{
		"WAGER_LOG_LEVEL":         "inexistente",
		"WAGER_HTTP_ADDRESS":      "sem-porta",
		"WAGER_HTTP_READ_TIMEOUT": "0s",
	}

	primeiro, err := FromEnv(envFixo(ambiente))
	if err == nil {
		t.Fatal("FromEnv aceitou configuracao invalida")
	}

	for i := 0; i < 5; i++ {
		_, outra := FromEnv(envFixo(ambiente))
		if outra.Error() != err.Error() {
			t.Fatalf("erro variou entre execucoes:\n 1: %v\n 2: %v", err, outra)
		}
	}

	var invalida *ValidationError
	errors.As(err, &invalida)
	if invalida.Field != "WAGER_HTTP_ADDRESS" {
		t.Errorf("primeiro campo reportado e %q, esperado %q", invalida.Field, "WAGER_HTTP_ADDRESS")
	}
	_ = primeiro
}

func TestMensagemDeErroNomeiaAVariavelDeAmbiente(t *testing.T) {
	_, err := FromEnv(envFixo(map[string]string{"WAGER_HTTP_ADDRESS": "sem-porta"}))
	if err == nil {
		t.Fatal("FromEnv aceitou endereco invalido")
	}

	if !strings.Contains(err.Error(), "WAGER_HTTP_ADDRESS") {
		t.Errorf("mensagem %q nao nomeia a variavel de ambiente", err.Error())
	}
}
