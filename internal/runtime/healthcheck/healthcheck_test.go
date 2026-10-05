package healthcheck

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// testaRota sobe um servidor de teste com a rota e devolve a URL completa.
//
// `httptest.NewServer` e servidor real em porta efemera, e nao `httptest.NewRecorder`:
// o que se quer verificar aqui e que a sonda faz uma chamada de rede de verdade, e
// um recorder que devolve 200 sem sair do processo provaria que a sonda sabe tratar
// um `*http.Response` e nao que ela sabe falar HTTP.
func testaRota(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	servidor := httptest.NewServer(handler)
	t.Cleanup(servidor.Close)
	return servidor.URL
}

func TestRodaAceitaLivenessQueResponde200(t *testing.T) {
	endereco := testaRota(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/live" {
			t.Errorf("consultou %q, e o esperado e /health/live", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("usou %s, e o esperado e GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"vivo"}`))
	})

	if err := Roda(endereco + "/health/live"); err != nil {
		t.Fatalf("sonda de processo vivo reprovou: %v", err)
	}
}

func TestRodaRecusaRespostaQueNaoE200(t *testing.T) {
	// 503 e o que o proprio servico devolve em `/health/ready` quando o banco nao
	// responde. Uma sonda que aceitasse 503 como "vivo" transformaria indisponibilidade
	// de dependencia em reinicio de processo, que e o oposto do desejado.
	endereco := testaRota(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	err := Roda(endereco + "/health/live")
	if err == nil {
		t.Fatal("sonda aceitou 503, e o esperado e recusa")
	}

	var erroDeSonda *Erro
	if !errors.As(err, &erroDeSonda) {
		t.Fatalf("erro %v nao e *Erro, e a sonda precisa nomear o endereco consultado", err)
	}
	if !strings.Contains(erroDeSonda.Motivo, "503") {
		t.Errorf("motivo %q nao menciona o codigo recebido, e o operador precisa ver o 503", erroDeSonda.Motivo)
	}
}

func TestRodaFalhaComEnderecoInexistente(t *testing.T) {
	servidor := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endereco := servidor.URL
	servidor.Close()

	// Um endereco que estava ouvindo e deixou de ouvir e o caso real de um processo
	// que subiu antes do servidor. A sonda precisa falhar com erro de transporte em
	// vez de devolver "vivo" por nao ter obtido resposta nenhuma.
	if err := Roda(endereco + "/health/live"); err == nil {
		t.Fatal("sonda deu viva para um endereco sem processo")
	}
}

func TestDaExecucaoDevolveZeroParaProbeBemSucedido(t *testing.T) {
	stderr := os.Stderr
	endereco := testaRota(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if codigo := DaExecucao([]string{endereco + "/health/live"}, stderr); codigo != 0 {
		t.Errorf("codigo de saida %d, e o esperado e 0", codigo)
	}
}

func TestDaExecucaoDevolveUmComMensagemNoStderr(t *testing.T) {
	capturado := &strings.Builder{}

	codigo := DaExecucao([]string{"http://127.0.0.1:1/health/live"}, capturado)

	if codigo != 1 {
		t.Errorf("codigo de saida %d, e o esperado e 1 para um probe que falhou", codigo)
	}
	if capturado.String() == "" {
		t.Error("a falha nao escreveu nada no stderr, e o Compose mostra stderr em `docker compose ps`")
	}
	if !strings.Contains(capturado.String(), "127.0.0.1:1") {
		t.Errorf("a mensagem %q nao nomeia o endereco consultado, e o operador precisa saber onde sondou", capturado.String())
	}
}

func TestDaExecucaoUsaEnderecoInformado(t *testing.T) {
	stderr := os.Stderr
	endereco := testaRota(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Sem argumento, a sonda consultaria o padrao e falharia. O fato de o codigo
	// de saida ser zero prova que o argumento foi usado.
	if codigo := DaExecucao([]string{endereco + "/health/live"}, stderr); codigo != 0 {
		t.Errorf("codigo de saida %d com endereco valido em argumento", codigo)
	}
}
