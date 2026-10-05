//go:build integration

package obsteste

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/httpapi"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
)

// metricasDoProcesso monta as metricas do enunciado e as expoe pela rota real.
//
// A rota e a do `httpapi`, e nao o `Registro.Expor` direto: o que se verifica aqui e
// o que o Prometheus recebe, e nao o que o registro sabe montar. Um `Expor` correto
// com uma rota que nao existe -- ou que devolve 404 -- seria um `/metrics` que nao
// existe com o registro certo por tras.
func metricasDoProcesso(t *testing.T) string {
	t.Helper()

	registro := obs.NovoRegistro()
	obs.NovasMetricas(registro)

	servidor := novoServidorDeMetricas(t, registro)
	defer servidor.Close()

	resp, err := http.Get(servidor.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics respondeu %d", resp.StatusCode)
	}

	// O content-type e o que o Prometheus espera para parsear. Sem ele o scrape
	// funciona e o painel mostra a serie como texto.
	if tipo := resp.Header.Get("Content-Type"); !strings.HasPrefix(tipo, "text/plain") {
		t.Errorf("Content-Type = %q, esperado text/plain", tipo)
	}

	bruto, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("leitura do corpo: %v", err)
	}
	return string(bruto)
}

// novoServidorDeMetricas sobe um servidor so com a rota de metricas.
//
// O servidor e separado do `ambiente` porque este teste nao precisa de Keycloak nem de
// banco: o que se verifica e a rota e o conteudo, e um teste de metricas que sobe
// infraestrutura real para medir uma string seria lento sem provar mais.
func novoServidorDeMetricas(t *testing.T, registro *obs.Registro) *httpTestServer {
	t.Helper()
	return sobeServidor(t, httpapi.Dependencias{Metricas: registro})
}

// GET /metrics responde as metricas do enunciado.
//
// A lista vem do enunciado, secao 12: resultados por status, duplicatas, retries,
// mensagens em cartao morto, conflitos de concorrencia, atraso da outbox, latencia de
// processamento e divergencias de reconciliacao. Cada uma precisa existir com nome,
// tipo e ajuda desde o primeiro scrape.
func TestMetricasDoEnunciadoEstaoExpostas(t *testing.T) {
	exposicao := metricasDoProcesso(t)

	esperadas := []struct{ nome, tipo string }{
		{"wager_operacoes_total", "counter"},
		{"wager_operacoes_duplicadas_total", "counter"},
		{"wager_retentativas_total", "counter"},
		{"wager_mensagens_fila_morta_total", "counter"},
		{"wager_conflitos_lock_total", "counter"},
		{"wager_outbox_atraso_segundos", "gauge"},
		{"wager_operacao_duracao_ms", "histogram"},
		{"wager_reconciliacoes_divergentes_total", "counter"},
		{"wager_conflitos_chave_total", "counter"},
		{"wager_eventos_publicados_total", "counter"},
		{"wager_eventos_desistidos_total", "counter"},
		{"wager_requisicao_duracao_ms", "histogram"},
	}

	for _, esperada := range esperadas {
		if !strings.Contains(exposicao, "# TYPE "+esperada.nome+" "+esperada.tipo) {
			t.Errorf("a metrica %s do enunciado nao foi exposta como %s:\n%s",
				esperada.nome, esperada.tipo, exposicao)
		}
		if !strings.Contains(exposicao, "# HELP "+esperada.nome+" ") {
			t.Errorf("a metrica %s nao tem linha de ajuda", esperada.nome)
		}
	}
}

// Nenhuma metrica de alta cardinalidade aparece no endpoint publico.
//
// Este e o teste que sustenta a decisao de deixar `/metrics` publico. O endpoint e
// aberto, e o que impede que ele vaze dado de cliente e a recusa de identificador
// como rotulo. Um teste que so dissesse "o endpoint e publico" nao provaria nada; um
// teste que confirma que nenhum identificador sai dele prova.
func TestMetricasNaoExpoemIdentificadorDeCliente(t *testing.T) {
	registro := obs.NovoRegistro()
	metricas := obs.NovasMetricas(registro)

	// Todas as tentativas de meter identificador no rotulo, inclusive por caminho
	// que nao existe no codigo de producao.
	metricas.Operacoes.Inc("walletId", "0192f291-27dd-7d3f-8071-5f8685deef37")
	metricas.Operacoes.Inc("playerId", "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")
	metricas.Operacoes.Inc("providerId", "provider-a")
	metricas.Duplicatas.Inc("transactionId", "0192f298-345e-7e38-af88-e43f851a819d")
	metricas.Retentativas.Inc("messageId", "msg-123")
	metricas.ConflitosLock.Inc("correlationId", "corr-123")
	metricas.EventosPublicados.Inc("eventId", "0192f299-345e-7e38-af88-e43f851a819e")

	exposicao := registro.Expor()

	for _, vazamento := range []string{
		"0192f291", "0192f28f", "0192f298", "0192f299",
		"provider-a", "msg-123", "corr-123",
		"walletId=", "playerId=", "transactionId=", "providerId=",
	} {
		if strings.Contains(exposicao, vazamento) {
			t.Errorf("a exposicao vazou %q:\n%s", vazamento, exposicao)
		}
	}

	// E o que o enunciado precisa continua exposto: os rotulos de conjunto fechado.
	metricas.Operacoes.Inc("estado", "PROCESSED")
	if !strings.Contains(registro.Expor(), `wager_operacoes_total{estado="PROCESSED"} 1`) {
		t.Errorf("o rotulo por estado sumiu junto com a recusa:\n%s", registro.Expor())
	}
}

// GET /metrics responde 404 quando o processo nao tem registro.
//
// Um 200 com corpo vazio seria indistinguivel de "o processo esqueceu de medir", e o
// Prometheus trataria os dois do mesmo jeito. O 404 e a resposta honesta para "nao
// ha metricas aqui".
func TestMetricasSemRegistroResponde404(t *testing.T) {
	servidor := sobeServidor(t, httpapi.Dependencias{})
	defer servidor.Close()

	resp, err := http.Get(servidor.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /metrics sem registro respondeu %d, esperado 404", resp.StatusCode)
	}
}

// GET /metrics nao exige token, como os health checks.
//
// A decisao e de exposicao publica, e ela so e defensavel se estiver provada: um
// teste que so verificasse que a rota existe deixaria em aberto se ela exige
// credencial.
func TestMetricasNaoExigeToken(t *testing.T) {
	servidor := sobeServidor(t, httpapi.Dependencias{Metricas: obs.NovoRegistro()})
	defer servidor.Close()

	// Sem header de autenticacao nenhum.
	resp, err := http.Get(servidor.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /metrics sem token respondeu %d, esperado 200", resp.StatusCode)
	}
}
