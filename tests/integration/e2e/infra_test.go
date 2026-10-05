//go:build integration

// Package e2e prova as garantias financeiras com tres processos independentes.
//
// Este e o portao de verdade do desafio. As demais suites montam pecas -- o caso de
// uso com o pool, o roteador com o validador, o relay com a fila -- e cada uma delas
// prova que a peca funciona. Nenhuma prova que a garantia vale com tres processos ao
// mesmo tempo, que e a condicao em que a idempotencia, o lock por carteira e a
// reconciliacao deixam de ser detalhe de implementacao.
//
// **O que separa este pacote dos demais:** cada instancia e um grafo de Fx proprio, com
// pool proprio, worker de relay proprio, consumidor proprio e memoria propria. Nada e
// compartilhado em memoria entre elas -- so o PostgreSQL, o SQS e o Keycloak, que sao
// os tres que o enunciado manda usar de verdade. Um teste que dividisse os casos de uso
// entre goroutines nao estaria provando distribuicao, estaria provando concorrencia
// dentro de um processo.
package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/google/uuid"

	runtime "github.com/LuisMassuchini/backend-challenge-go/internal/runtime/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/config"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// dsnRuntime e o papel de menor privilegio, o mesmo que a aplicacao usa.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// dsnDono e o papel de dono do schema, para as verificacoes que nao passam
// pela aplicacao.
const dsnDono = "postgres://wager:wager@localhost:5432/wager?sslmode=disable"

// numeroDeInstancias e quantos processos sobem.
//
// Tres e o minimo do enunciado, e o minimo que faz a prova: com duas instancias a
// disputa ainda pode ser resolvida por cache de processo, e com uma so nao ha disputa
// nenhuma. O enunciado e explicito em tres, e a E17 tambem.
const numeroDeInstancias = 3

// Instancia e um processo do servico.
//
// E o grafo de Fx inteiro, com `Start` e `Stop` de verdade. O teste fala com ela por
// HTTP na porta efemera que ela abriu, e nao por chamada de funcao: chamar o caso de uso
// direto dentro do processo eliminaria a borda, e o teste estaria provando outra coisa.
type Instancia struct {
	// nome identifica a instancia no log de falha.
	nome string

	// endereco e a base HTTP.
	endereco string

	// encerrar derruba o processo e libera a porta.
	encerrar func()
}

// sobeInstancia sobe um processo com a configuracao informada.
//
// A porta e efemera e reservada antes do start. Uma porta fixa transformaria dois testes
// que sobem processo em um teste de sorte: o segundo falharia com "endereco ja em uso"
// quando o primeiro nao liberou a porta ainda.
func sobeInstancia(t *testing.T, nome string, extras map[string]string) *Instancia {
	t.Helper()

	porta := portaLivre(t)
	ambiente := map[string]string{
		config.ChaveHTTPAddress:         fmt.Sprintf("127.0.0.1:%d", porta),
		config.ChavePostgresDSN:         dsnRuntime,
		config.ChaveLogLevel:            string(config.LogLevelError),
		config.ChaveOIDCIssuer:          emissorDoIdp(),
		config.ChaveOIDCAudience:        "wager-service",
		config.ChaveHTTPShutdownTimeout: "10s",
	}
	for chave, valor := range extras {
		ambiente[chave] = valor
	}

	cfg, err := config.FromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	})
	if err != nil {
		t.Fatalf("configuracao da instancia %s: %v", nome, err)
	}

	aplicacao := runtime.New(cfg, &runtime.Eventos{})

	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelar()

	if err := aplicacao.Start(ctx); err != nil {
		t.Fatalf("subida da instancia %s: %v", nome, err)
	}

	instant := &Instancia{
		nome:     nome,
		endereco: fmt.Sprintf("http://127.0.0.1:%d", porta),
	}
	instant.encerrar = func() {
		// O prazo e de quinze segundos e nao o do ambiente: um processo que precisa de
		// mais que isso para parar tem worker preso, e o teste de recuperacao precisa
		// notar isso em vez de esperar por ele.
		ctxParada, cancelaParada := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelaParada()

		if err := aplicacao.Stop(ctxParada); err != nil {
			t.Errorf("encerramento da instancia %s: %v", nome, err)
		}
	}
	return instant
}

// sobeInstancias sobe N processos independentes.
//
// Cada uma em `t.Cleanup`, e nao em `defer` do teste: o `t.Cleanup` roda mesmo quando o
// teste falha com `t.Fatal`, e um `defer` em quem chamou a funcao roda so no caminho
// normal. Um processo que sobrasse vivo seguraria a porta e o pool, e o proximo pacote,
// que roda em seguida com `-p 1`, falharia por causa dele.
func sobeInstancias(t *testing.T, extras map[string]string) []*Instancia {
	t.Helper()

	instancias := make([]*Instancia, 0, numeroDeInstancias)
	for i := 0; i < numeroDeInstancias; i++ {
		nome := fmt.Sprintf("instancia-%d", i+1)
		instant := sobeInstancia(t, nome, extras)
		t.Cleanup(instant.encerrar)
		instancias = append(instancias, instant)
	}

	// Todas precisam responder antes do teste comecar. Uma instancia que subiu e nao
	// esta pronta produz uma falha no meio do cenario, com o sintoma errado.
	for _, instant := range instancias {
		if status, corpo := instant.consultar(t, "/health/ready"); status != http.StatusOK {
			t.Fatalf("%s nao ficou pronta: %d %s", instant.nome, status, corpo)
		}
	}

	return instancias
}

// consultar faz um GET sem autenticacao.
func (i *Instancia) consultar(t *testing.T, caminho string) (int, string) {
	t.Helper()
	return i.chamar(t, http.MethodGet, caminho, "", "", "", nil)
}

// Resposta e o resultado de uma chamada autenticada.
type Resposta struct {
	// Status e o codigo HTTP.
	Status int
	// Corpo e o JSON devolvido.
	Corpo map[string]any
}

// chamar faz uma requisicao autenticada e devolve o corpo cru.
//
// O `t.Helper` fica aqui para que a linha do `t.Fatalf` aponte para quem chamou e nao
// para o auxiliar -- em um cenario com 50 goroutines, um erro de auxiliar apontando
// para a linha errada custaria mais que o proprio teste.
func (i *Instancia) chamar(
	t *testing.T,
	metodo, caminho, token, idempotencia, correlacao string,
	corpo any,
) (int, string) {
	t.Helper()

	var leitor io.Reader
	if corpo != nil {
		bruto, err := json.Marshal(corpo)
		if err != nil {
			t.Fatalf("marshal do corpo: %v", err)
		}
		leitor = bytes.NewReader(bruto)
	}

	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, metodo, i.endereco+caminho, leitor)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencia != "" {
		req.Header.Set("Idempotency-Key", idempotencia)
	}
	if correlacao != "" {
		req.Header.Set("X-Correlation-Id", correlacao)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisicao %s %s na %s: %v", metodo, caminho, i.nome, err)
	}
	defer resp.Body.Close()

	//nolint:errcheck
	bruto, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(bruto)
}

// chamarJSON faz uma requisicao e decodifica a resposta.
func (i *Instancia) chamarJSON(
	t *testing.T,
	metodo, caminho, token, idempotencia, correlacao string,
	corpo any,
) Resposta {
	t.Helper()

	status, bruto := i.chamar(t, metodo, caminho, token, idempotencia, correlacao, corpo)

	var decodificado map[string]any
	if bruto != "" {
		if err := json.Unmarshal([]byte(bruto), &decodificado); err != nil {
			t.Fatalf("resposta de %s %s nao e JSON (%d): %q", metodo, caminho, status, bruto)
		}
	}
	return Resposta{Status: status, Corpo: decodificado}
}

// chamarSemFatal faz a requisicao e devolve erro em vez de derrubar o teste.
//
// Existe para o caminho de dentro de goroutine. `t.Fatalf` em outra goroutine encerra
// apenas aquela goroutine: o teste continua, termina com metade dos resultados e falha
// com uma contagem que nao explica nada. Aqui a falha volta como `(0, "")` e quem
// chamou decide -- normalmente contando, e o numero e o diagnostico.
func (i *Instancia) chamarSemFatal(
	metodo, caminho, token, idempotencia, correlacao string,
	corpo any,
) (int, string) {
	var leitor io.Reader
	if corpo != nil {
		bruto, err := json.Marshal(corpo)
		if err != nil {
			return 0, ""
		}
		leitor = bytes.NewReader(bruto)
	}

	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, metodo, i.endereco+caminho, leitor)
	if err != nil {
		return 0, ""
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencia != "" {
		req.Header.Set("Idempotency-Key", idempotencia)
	}
	if correlacao != "" {
		req.Header.Set("X-Correlation-Id", correlacao)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// O status zero e o sinal de "nem chegou a responder". O teste conta isso como
		// erro de infraestrutura, que e o que aconteceu.
		return 0, err.Error()
	}
	defer resp.Body.Close()

	//nolint:errcheck
	bruto, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(bruto)
}

// deserializar decodifica JSON em um destino.
func deserializar(bruto string, destino any) error {
	return json.Unmarshal([]byte(bruto), destino)
}

// abrirCarteira abre uma carteira pelo caminho interno e devolve o identificador.
//
// O token e o do cliente de servico e nao o do provedor: abertura de carteira e
// operacao interna, e o escopo esta separado no IdP. Usar o token do provedor daria
// 403, e a falha pareceria ser de concorrencia.
//
// A abertura alterna entre as instancias, e nao fica sempre na primeira: um cenario de
// concorrencia que abre tudo na primeira mede a segunda apenas no caminho da disputa.
func abrirCarteira(t *testing.T, token string, instancias []*Instancia, centavos int64) string {
	t.Helper()

	instant := instancias[0]
	resposta := instant.chamarJSON(t, http.MethodPost, "/wallets", token, "", "", map[string]any{
		"playerId": uuid.NewString(),
		"initialBalance": map[string]any{
			"amount":   fmt.Sprintf("%d.%02d", centavos/100, abs(centavos%100)),
			"currency": "BRL",
		},
	})
	if resposta.Status != http.StatusCreated {
		t.Fatalf("abertura respondeu %d: %v", resposta.Status, resposta.Corpo)
	}

	carteira, ok := resposta.Corpo["id"].(string)
	if !ok {
		t.Fatalf("a resposta de abertura nao traz a carteira: %v", resposta.Corpo)
	}
	return carteira
}

// lerSaldo devolve o saldo da carteira em centavos.
//
// A leitura e pela API e nao por SQL pelo mesmo motivo do restante: o que o provedor ve
// e o que o teste precisa medir. Consultar o banco confirmaria a tabela e nao o
// contrato.
func lerSaldo(t *testing.T, token string, instancia *Instancia, carteira string) int64 {
	t.Helper()

	resposta := instancia.chamarJSON(t, http.MethodGet, "/wallets/"+carteira, token, "", "", nil)
	if resposta.Status != http.StatusOK {
		t.Fatalf("leitura da carteira respondeu %d: %v", resposta.Status, resposta.Corpo)
	}

	saldo, ok := resposta.Corpo["balance"].(map[string]any)
	if !ok {
		t.Fatalf("a leitura nao traz saldo: %v", resposta.Corpo)
	}
	valor, ok := saldo["amount"].(string)
	if !ok {
		t.Fatalf("o saldo nao e texto: %v", saldo)
	}
	return centavosDe(t, valor)
}

// lerLedger devolve todos os lancamentos da carteira.
//
// A paginacao por cursor e percorrida inteira. Um teste que le so a primeira pagina
// contaria menos lancamentos do que existe, e a contagem errada apontaria para a
// garantia errada -- pareceria que a idempotencia falhou quando o que falhou foi a
// leitura.
func lerLedger(t *testing.T, token string, instancia *Instancia, carteira string) []map[string]any {
	t.Helper()

	var (
		lancamentos []map[string]any
		cursor      string
	)

	// O teto de paginas e uma defesa contra paginacao que nao termina. Sem ele, um
	// cursor que devolve sempre o mesmo valor seria lido para sempre e o teste
	// passaria a ser um laco.
	for pagina := 0; pagina < 20; pagina++ {
		caminho := "/wallets/" + carteira + "/ledger?limit=100"
		if cursor != "" {
			caminho += "&cursor=" + url.QueryEscape(cursor)
		}

		resposta := instancia.chamarJSON(t, http.MethodGet, caminho, token, "", "", nil)
		if resposta.Status != http.StatusOK {
			t.Fatalf("ledger respondeu %d: %v", resposta.Status, resposta.Corpo)
		}

		lote, ok := resposta.Corpo["entries"].([]any)
		if !ok {
			t.Fatalf("o ledger nao traz entradas: %v", resposta.Corpo)
		}
		for _, bruto := range lote {
			lancamento, ok := bruto.(map[string]any)
			if !ok {
				t.Fatalf("lancamento malformado: %v", bruto)
			}
			lancamentos = append(lancamentos, lancamento)
		}

		proximo, tem := resposta.Corpo["nextCursor"].(string)
		if !tem || proximo == "" {
			return lancamentos
		}
		cursor = proximo
	}

	t.Fatalf("a paginacao do ledger nao terminou em 20 paginas: %d lancamentos", len(lancamentos))
	return nil
}

// contaLancamentosPorDirecao devolve quantos lancamentos de cada direcao existem.
//
// E por direcao e nao por transacao porque e a direcao que responde a pergunta do
// enunciado: "um unico debito no ledger". Contar transacoes exigiria abrir cada lancamento
// e seguir a referencia, e a direcao ja esta no proprio lancamento.
func contaLancamentosPorDirecao(lancamentos []map[string]any) (debitos, creditos int) {
	for _, lancamento := range lancamentos {
		switch lancamento["direction"] {
		case "DEBIT":
			debitos++
		case "CREDIT":
			creditos++
		}
	}
	return debitos, creditos
}

// pedidoDeOperacao monta o corpo de uma operacao.
func pedidoDeOperacao(carteira, jogador, externa, tipo string, centavos int64) map[string]any {
	return map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": externa,
		"playerId":              jogador,
		"walletId":              carteira,
		"roundId":               "round-987",
		"gameId":                "fortune-chimp",
		"kind":                  tipo,
		"money": map[string]any{
			"amount":   fmt.Sprintf("%d.%02d", centavos/100, abs(centavos%100)),
			"currency": "BRL",
		},
	}
}

// jogadorDaCarteira devolve o jogador dono da carteira.
//
// A leitura e pela API porque o teste precisa do valor que o provedor usaria no comando,
// e nao de uma coluna: um `playerId` inventado seria recusado por validacao e a falha
// pareceria ser de idempotencia.
func jogadorDaCarteira(t *testing.T, token string, instancia *Instancia, carteira string) string {
	t.Helper()

	resposta := instancia.chamarJSON(t, http.MethodGet, "/wallets/"+carteira, token, "", "", nil)
	if resposta.Status != http.StatusOK {
		t.Fatalf("leitura da carteira respondeu %d: %v", resposta.Status, resposta.Corpo)
	}

	jogador, ok := resposta.Corpo["playerId"].(string)
	if !ok {
		t.Fatalf("a leitura nao traz o jogador: %v", resposta.Corpo)
	}
	return jogador
}

// reconciliar pede a reconciliacao e devolve o corpo.
//
// A reconciliacao e do cliente interno, e o mesmo token que abre carteira. O
// `wager-service` tem `wager:reconciliacao` e nunca tem `wager:operacoes`: usar o token
// do provedor aqui daria 403 e a falha pareceria ser de concorrencia.
func reconciliar(t *testing.T, token string, instancia *Instancia, carteira string) map[string]any {
	t.Helper()

	resposta := instancia.chamarJSON(t, http.MethodPost,
		"/wallets/"+carteira+"/reconciliation", token, "", "", nil)
	if resposta.Status != http.StatusOK {
		t.Fatalf("reconciliacao respondeu %d: %v", resposta.Status, resposta.Corpo)
	}
	return resposta.Corpo
}

// pedirToken busca um token real do IdP.
//
// O token e buscado na thread principal e nunca dentro de goroutine: um `t.Fatalf`
// dentro de outra goroutine encerra apenas aquela goroutine, e a falha apareceria como
// uma contagem errada, sem dizer nada sobre a causa.
func pedirToken(t *testing.T, cliente string) string {
	t.Helper()

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", cliente)
	form.Set("client_secret", cliente+"-secret")

	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(emissorDoIdp(), "/")+protocoloDeToken,
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("request do token: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("IdP acessivel? %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		//nolint:errcheck
		bruto, _ := io.ReadAll(resp.Body)
		t.Fatalf("token de %s: status %d, corpo %s", cliente, resp.StatusCode, bruto)
	}

	var corpo struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&corpo); err != nil {
		t.Fatalf("decodificacao do token: %v", err)
	}
	if corpo.AccessToken == "" {
		t.Fatalf("o IdP devolveu token vazio para %s", cliente)
	}
	return corpo.AccessToken
}

// protocoloDeToken e o caminho do token no realm.
//
// Fica em constante porque as tres suites de integracao que falam com o IdP precisam do
// mesmo caminho, e tres copias do texto sao tres lugares para errar.
const protocoloDeToken = "/protocol/openid-connect/token"

// emissorDoIdp devolve o endereco do realm de teste.
func emissorDoIdp() string {
	if valor := os.Getenv("WAGER_TEST_OIDC_ISSUER"); valor != "" {
		return strings.TrimSuffix(valor, "/")
	}
	return "http://localhost:8081/realms/wager"
}

// portaLivre devove uma porta que nao esta em uso.
//
// A reserva e o fechamento sao o que torna a porta efemera confiavel: sem o fechamento a
// porta ja estaria ocupada por este processo no momento de passar ao start, e o start
// falharia com "endereco ja em uso" contra o proprio teste.
func portaLivre(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserva de porta: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

// centavosDe converte o valor decimal do contrato em centavos.
//
// O parser e o mesmo do dominio, e nao uma conta feita aqui. Um teste que mede dinheiro
// precisa do mesmo parsing do sistema: uma diferenca de arredondamento entre o teste e
// o codigo apareceria como divergencia financeira e apontaria para a garantia errada.
func centavosDe(t *testing.T, valor string) int64 {
	t.Helper()

	centavos, err := centavosParse(valor)
	if err != nil {
		t.Fatalf("valor %q nao e decimal valido: %v", valor, err)
	}
	return centavos
}

// centavosParse converte texto decimal em centavos, sem passar por float.
//
// A razao de nao usar `strconv.ParseFloat` e o eliminatorio do enunciado: um float de
// 64 bits nao representa `0.01` de forma exata, e um teste que usasse float para
// conferir saldo mediria o erro do float em vez da garantia do sistema.
func centavosParse(valor string) (int64, error) {
	texto := strings.TrimSpace(valor)

	negativo := strings.HasPrefix(texto, "-")
	if negativo {
		texto = texto[1:]
	}

	inteira, fracionaria, temPonto := strings.Cut(texto, ".")
	if inteira == "" {
		return 0, fmt.Errorf("sem parte inteira")
	}

	var total int64
	for i := 0; i < len(inteira); i++ {
		if inteira[i] < '0' || inteira[i] > '9' {
			return 0, fmt.Errorf("digito invalido em %q", inteira)
		}
		total = total*10 + int64(inteira[i]-'0')
	}

	// As duas casas sao as do contrato. Uma terceira casa viria como erro aqui, e nao
	// como truncamento silencioso: o mesmo principio que o `Money` do sistema aplica.
	var fracao int64
	for i := 0; i < 2; i++ {
		if temPonto && i < len(fracionaria) {
			if fracionaria[i] < '0' || fracionaria[i] > '9' {
				return 0, fmt.Errorf("digito invalido em %q", fracionaria)
			}
			fracao = fracao*10 + int64(fracionaria[i]-'0')
		} else {
			fracao *= 10
		}
	}

	total = total*100 + fracao
	if negativo {
		total = -total
	}
	return total, nil
}

// idDeIndice devolve um identificador estavel e unico por indice.
//
// O `playerId` tem de ser um UUID valido porque o dominio valida antes de gravar, e
// um indice como texto seria recusado com "playerId invalido" -- e a falha pareceria
// ser de concorrencia. O UUID vem do indice com um prefixo de namespace, de modo que
// dois cenarios nunca colidam no mesmo banco.
//
// A funcao e pura e nao usa `uuid.NewString` porque dois testes que abrissem a mesma
// carteira por acasobentrum flourishiam um no outro.
func idDeIndice(indice int) string {
	//nolint:errcheck
	gerado := uuid.NewSHA1(namespaceE2E, []byte(fmt.Sprintf("indice-%d", indice)))
	return gerado.String()
}

// namespaceE2E e o namespace dos identificadores gerados pelo pacote.
//
// Um UUID no namespace Fixo e o que garante que `idDeIndice(3)` seja sempre o mesmo
// valor: um teste que abre a carteira do indice 3 em uma execucao e em outra precisa
// ver a mesma carteira, senao a segunda execucao comecaria do zero sem o teste ter
// mudado.
var namespaceE2E = uuid.MustParse("6f8d1e2a-3b4c-4d5e-8f90-1a2b3c4d5e6f")

// abs devolve o valor absoluto.
func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// contarMensagensNaInbox devolve quantas mensagens o consumidor registrou.
//
// A leitura e pelo SQL e nao pela API porque nao existe rota de inbox, e o que se quer
// verificar e a chegada da mensagem -- um fato de infraestrutura, nao de contrato. Um
// teste que esperasse pela API nao teria o que consultar.
func contarMensagensNaInbox(t *testing.T) int {
	t.Helper()

	db, err := abrirLeitura(t)
	if err != nil {
		return 0
	}
	defer db.Close()

	var total int
	//nolint:errcheck
	err = db.QueryRow("SELECT count(*) FROM inbox_messages").Scan(&total)
	if err != nil {
		return 0
	}
	return total
}

// abrirLeitura abre uma conexao de leitura com o papel de dono.
//
// O dono e o papel de menor privilegio nao seria alcancado aqui: a contagem e uma
// verificacao de teste, e nao parte do caminho que se quer provar.
func abrirLeitura(t *testing.T) (*sql.DB, error) {
	t.Helper()
	return sql.Open("pgx", dsnDono)
}

// esperarAte repete a condicao ate ela valer ou o prazo acabar.
//
// Existe para esperar condicao que depende de outro processo. A diferenca para um
// `time.Sleep` fixo e que o teste nao fica lento quando a condicao e rapida nem
// instavel quando ela e lenta.
//
// O primeiro intervalo e curto e o crescimento e dobrado, e os dois por um motivo: o
// consumidor tem long polling de vinte segundos, entao a condicao costuma virar antes
// disso -- mas um poll unico de duzentos milissegundos transformaria um caso rapido em
// quase um segundo de espera, e um poll fixo de cinco segundos transformaria o
// `TestConteudoDiferenteComAMesmaChaveEConflito`, cuja condicao e instantanea, em
// cinco segundos de espera.
//
// O erro final mostra o que foi medido na ultima tentativa, que e o que ajuda a
// decidir se o cenario nunca vai passar ou so ainda nao passou.
func esperarAte(t *testing.T, prazo time.Duration, descricao string, condicao func() bool) {
	t.Helper()

	limite := time.Now().Add(prazo)
	espera := 20 * time.Millisecond

	for time.Now().Before(limite) {
		if condicao() {
			return
		}
		time.Sleep(espera)

		// Teto no crescimento: um intervalo que chegue a meio segundoaria o atraso de
		// deteccao em um cenario que depende de outro processo.
		if espera < 500*time.Millisecond {
			espera *= 2
		}
	}
	t.Fatalf("a condicao %q nao foi satisfeita em %s", descricao, prazo)
}

// limparBase esvazia as tabelas de negocio no comeco do cenario.
//
// E chamada no comeco e nao no fim: um cenario que herda dado do anterior mede o
// anterior, e o sintoma seria uma contagem de lancamentos a mais que nao tem nada a
// ver com a garantia testada.
func limparBase(t *testing.T) {
	t.Helper()
	dbtest.Limpa(t)
}

// fmtChave devolve a chave de idempotencia de uma operacao do cenario.
func fmtChave(indice int) string {
	return "provider-a:relay-" + fmtIndice(indice)
}

// fmtIndice devolve o indice como texto.
func fmtIndice(indice int) string {
	return fmt.Sprintf("%d", indice)
}
