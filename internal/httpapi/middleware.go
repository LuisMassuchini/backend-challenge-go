package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/auth"
)

// chaveAtor e o contexto sob o qual o ator autenticado viaja ate o handler.
//
// E um valor tipado e nao uma string solta no contexto: duas strings com o mesmo
// nome em pacotes diferentes colidiram silenciosamente, e o sintoma seria um
// handler que recebe um ator vazio e recusa uma requisicao legitima.
type chaveAtor struct{}

// AtorDoContexto devolve o ator autenticado da requisicao.
//
// Devolve false quando a rota nao passou pelo middleware de autenticacao. Um
// handler de rota protegida quereceba false tem um bug de montagem, e o testes
// trata isso como falha em vez de recusar a requisicao.
func AtorDoContexto(ctx context.Context) (app.Ator, bool) {
	ator, ok := ctx.Value(chaveAtor{}).(app.Ator)
	return ator, ok
}

// cabecalhoCorrelacao e o nome do cabecalho que amarra a requisicao ao log.
//
// O nome e o mesmo do lado do provedor e do lado do SQS, e por isso que o valor
// atravessa as duas pontas: um mesmo fluxo pode ser procurado pelo mesmo texto em
// qualquer um dos dois transports.
const cabecalhoCorrelacao = "X-Correlation-Id"

// chaveCorrelacao e a chave de contexto da correlacao.
type chaveCorrelacao struct{}

// CorrelacaoDoContexto devolve a correlacao da requisicao.
func CorrelacaoDoContexto(ctx context.Context) string {
	valor, _ := ctx.Value(chaveCorrelacao{}).(string)
	return valor
}

// autenticar valida o token e coloca o ator no contexto.
//
// Sem validador montado, a rota fica aberta. Isso e deliberado e vale como
// alarme: o estado sem autenticacao existe para migrations e para o primeiro boot,
// e o health check nao mente sobre ele porque nao depende de token. Em qualquer
// outro estado, a ausencia de validador e erro de montagem.
func autenticar(d Dependencias, proximo http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Validador == nil {
			responderJSON(w, http.StatusServiceUnavailable, respostaErro{
				Erro:    "autenticacao_indisponivel",
				Codigo:  http.StatusServiceUnavailable,
				Detalhe: "a aplicacao esta sem validador de token montado",
			})
			return
		}

		token := auth.ExtrairToken(r.Header.Get("Authorization"))
		if token == "" {
			responderJSON(w, http.StatusUnauthorized, respostaErro{
				Erro:    "credencial_ausente",
				Codigo:  http.StatusUnauthorized,
				Detalhe: "informe o header Authorization: Bearer <token>",
			})
			return
		}

		ator, err := d.Validador.Ator(r.Context(), token)
		if err != nil {
			// A resposta nao distingue token expirado de token invalido alem do
			// status, porque os dois significam a mesma coisa para o cliente: pedir
			// uma credencial nova. O motivo vai para o log, que e para o operador.
			responderJSON(w, http.StatusUnauthorized, respostaErro{
				Erro:    "credencial_invalida",
				Codigo:  http.StatusUnauthorized,
				Detalhe: "o token nao passou na verificacao",
			})
			return
		}

		ctx := context.WithValue(r.Context(), chaveAtor{}, ator)
		proximo(w, r.WithContext(ctx))
	}
}

// comCorrelacao garante que toda requisicao tenha um identificador de correlacao.
//
// O identificador entra no contexto e na resposta. Na resposta porque e o que
// permite ao cliente citar a correlacao ao abrir um chamado, e o caminho mais curto
// entre "algo deu errado" e o log exato.
func comCorrelacao(proximo http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlacao := r.Header.Get(cabecalhoCorrelacao)
		if correlacao == "" {
			correlacao = uuid.NewString()
		}
		// O limite evita que um cliente mande uma correlacao de um megabyte e a
		// usaria como chave de log.
		if len(correlacao) > 128 {
			correlacao = correlacao[:128]
		}

		w.Header().Set(cabecalhoCorrelacao, correlacao)
		ctx := context.WithValue(r.Context(), chaveCorrelacao{}, correlacao)
		proximo.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recuperar impede que um panic no handler derrube o processo.
//
// O panic vira 500 e o stack vai para o log. Um handler que entra em panic por um
// dado de entrada derrubaria o processo inteiro, e um cliente poderia derrubar o
// servico mandando um corpo malformado.
func recuperar(proximo http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if motivo := recover(); motivo != nil {
				// Um ResponseWriter ja utilizado nao pode ser reaproveitado: o
				// cabecalho pode ter saído. Escrever 500 nesse estado adiciona
				// bytes a uma resposta started, e o cliente ve JSON truncado em vez
				// de um erro limpo.
				slog.Error("panic no handler",
					"metodo", r.Method,
					"caminho", r.URL.Path,
					"correlacao", CorrelacaoDoContexto(r.Context()),
					"motivo", motivo,
				)
				responderJSON(w, http.StatusInternalServerError, respostaErro{
					Erro:    "erro_interno",
					Codigo:  http.StatusInternalServerError,
					Detalhe: "erro inesperado no atendimento",
				})
			}
		}()

		proximo.ServeHTTP(w, r)
	})
}

// cronometrar registra a duracao de cada requisicao.
//
// O log por requisicao e o que permite afirmar, depois, que a garantia de nao
// duplicar movimentacao valeu em producao e nao so no teste.
func cronometrar(proximo http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		registrador := &registradorDeStatus{ResponseWriter: w}
		proximo.ServeHTTP(registrador, r)

		slog.Info("requisicao atendida",
			"metodo", r.Method,
			"caminho", r.URL.Path,
			"status", statusGravado(registrador),
			"duracao_ms", time.Since(inicio).Milliseconds(),
			"correlacao", CorrelacaoDoContexto(r.Context()),
		)
	})
}

// registradorDeStatus memoriza o status para o log.
//
// Precisa ser um wrapper porque o http.ResponseWriter nao expoe o status depois
// de escrito, e sem ele o log de toda requisicao seria 200.
type registradorDeStatus struct {
	// http.ResponseWriter e o writer original.
	http.ResponseWriter

	// status e o que foi escrito.
	status int
}

// WriteHeader memoriza o status antes de delegar.
func (g *registradorDeStatus) WriteHeader(status int) {
	g.status = status
	g.ResponseWriter.WriteHeader(status)
}

// WriteHeader ja registrado e um no-op.
//
// Sem este metodo, o segundo WriteHeader mudaria o status registrado depois de o
// cabecalho ter saido, e o log passaria a mentir.
func (g *registradorDeStatus) Write(b []byte) (int, error) {
	if g.status == 0 {
		g.status = http.StatusOK
	}
	return g.ResponseWriter.Write(b)
}

// statusGravado devolve o status, com 200 como padrao de quem nao escreveu
// cabecalho.
func statusGravado(w http.ResponseWriter) int {
	if g, ok := w.(*registradorDeStatus); ok {
		if g.status == 0 {
			return http.StatusOK
		}
		return g.status
	}
	return http.StatusOK
}

// comMiddlewares monta a cadeia completa.
//
// A ordem e a do mais externo para o mais interno e cada posicao tem uma razao:
// recuperar e o mais externo para pegar panic de qualquer etapa; cronometrar vem
// antes da correlacao para que uma requisicao que quebra na correlacao ainda seja
// cronometrada; e a autenticacao e a mais interna de todas para que so o trabalho
// autenticado entre no log de negocio.
func comMiddlewares(proximo http.Handler) http.Handler {
	return recuperar(cronometrar(comCorrelacao(proximo)))
}
