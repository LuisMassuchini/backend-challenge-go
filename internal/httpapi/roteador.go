package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
)

// ValidadorDeToken e o que o middleware precisa saber para autenticar.
//
// E uma interface, e nao o tipo concreto, para que o teste do roteador monte um
// validador sem depender de um IdP no ar. Isso nao e mock de IdP: os testes que
// importam o comportamento do IdP de verdade sao os de integracao, em
// tests/integration/oidc, e eles usam o Keycloak. Aqui so se verifica que o
// middleware liga o token ao ator certo.
type ValidadorDeToken interface {
	Ator(ctx context.Context, bruto string) (app.Ator, error)
}

// Dependencias e o que o servidor HTTP precisa.
//
// Entra como valor e nao como grao de Fx porque o roteador e construivel sem o
// ciclo de vida: o E12 junta as duas coisas, e um teste do roteador nao deveria
// subir um processo inteiro para exercitar uma rota.
type Dependencias struct {
	// Servicos sao os casos de uso.
	Servicos app.Servicos

	// Validador autentica o token. Nil significa que a aplicacao sobe sem
	// autenticacao, que e o estado em que as migrations rodam.
	Validador ValidadorDeToken

	// Pronto e o que o health check de readiness consulta. Nil significa pronto.
	Pronto func(context.Context) error

	// Metricas expoe as metricas do processo. Nil significa que o processo sobe sem
	// registro, que e o estado de quem so precisa atender.
	//
	// Nil e nao um registro vazio de proposito: `GET /metrics` sem registro
	// responderia 200 com um corpo vazio, e quem scrapeia leria "nenhuma metrica" --
	// que e indistinguivel de "o processo esqueceu de medir".
	Metricas *obs.Registro
}

// NovoServidor monta o servidor e as rotas.
//
// As rotas sao montadas em um ServeMux explicito, e nao por biblioteca de
// roteamento, porque o conjunto e pequeno e fixo. Roteador com reflexao traz
// dependencias que so se pagam em quantidade de rota, e o ganho aqui seria zero.
func NovoServidor(d Dependencias) *http.Server {
	return &http.Server{
		Handler:           NovoRoteador(d),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// NovoRoteador monta apenas o handler, sem o servidor.
//
// A separacao existe para que o teste monte o handler com httptest.NewServer e
// Dirac contra uma porta efemera real, em vez de simular o transporte.
func NovoRoteador(d Dependencias) http.Handler {
	rotas := http.NewServeMux()

	// Os health checks sao publicos por contrato e por utilidade: um
	// orquestrador nao tem token, e um probe que depende de autenticacao so prova
	// que o IdP esta de pe.
	rotas.HandleFunc("GET /health/live", live)
	rotas.HandleFunc("GET /health/ready", ready(d))

	// `/metrics` e publico pelo mesmo motivo dos health checks: quem scrapeia e o
	// Prometheus, que opera com credencial propria e nao tem token do Keycloak.
	//
	// O que torna isso aceitavel e o que a metrica recusa ser: nenhum identificador
	// de carteira, jogador, provedor ou transacao vira rotulo. As series sao de
	// estado e de tipo, conjuntos fechados, e o que o endpoint revela e volume de
	// operacao -- nao dado de cliente.
	//
	// Ver `obs.nomesDeRotuloProibidos`, que e a lista que torna a promessa verificavel
	// em vez de apenas afirmada.
	if d.Metricas != nil {
		rotas.HandleFunc("GET /metrics", metricas(d.Metricas))
	}

	// As rotas de negocio ficam sob o grupo que exige token. A autorizacao por
	// escopo e do caso de uso, nao do roteador: o roteador diz quem e o ator, e o
	// caso de uso diz o que aquele ator pode fazer.
	protegidas := map[string]http.HandlerFunc{
		"POST /wallets":                              abrirCarteira(d),
		"GET /wallets/{walletId}":                    lerCarteira(d),
		"GET /wallets/{walletId}/ledger":             listarLedger(d),
		"POST /wallets/{walletId}/reconciliation":    reconciliar(d),
		"POST /wagering/transactions":                enviarOperacao(d),
		"GET /wagering/transactions/{transactionId}": lerTransacao(d),
		"GET /providers/{providerId}/wagering/transactions/{externalTransactionId}": lerTransacaoDoProvedor(d),
	}

	for padrao, handler := range protegidas {
		rotas.Handle(padrao, autenticar(d, handler))
	}

	return comMiddlewares(rotas)
}

// metricas expoe as metricas no formato de texto do Prometheus.
//
// O tipo de conteudo e o que o Prometheus espera. Sem ele, o scrape funciona e o
// painel mostra a serie como texto, o que parece funcionar em uma consulta manual e
// falha em producao.
func metricas(registro *obs.Registro) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		// O erro de escrita aqui nao pode ser tratado: a resposta ja foi iniciada e
		// nao ha canal para corrigir.
		//nolint:errcheck
		_, _ = w.Write([]byte(registro.Expor()))
	}
}

// live responde que o processo esta vivo.
//
// Nao consulta nada. Um probe de liveness que checa banco derruba o processo
// quando o banco cai, e o efeito e o oposto do desejado: o banco voltando nao
// traz o processo de volta porque o orquestrador ja o matou.
func live(w http.ResponseWriter, r *http.Request) {
	responderJSON(w, http.StatusOK, map[string]any{
		"status": "vivo",
	})
}

// ready responde se o processo pode atender trafego.
//
// Aqui sim consulta as dependencias, porque readiness e sobre poder atender, e nao
// sobre existir.
func ready(d Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Pronto == nil {
			responderJSON(w, http.StatusOK, map[string]any{"status": "pronto"})
			return
		}

		ctx, cancelar := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancelar()

		if err := d.Pronto(ctx); err != nil {
			responderJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "nao_pronto",
				"motivo": err.Error(),
			})
			return
		}

		responderJSON(w, http.StatusOK, map[string]any{"status": "pronto"})
	}
}
