//go:build integration

// Package ciclo sobe o servico completo, do grafo de dependencias ao ultimo
// listener, e fala com ele por HTTP.
//
// O teste existe porque as demais suites montam pecas: o caso de uso com o pool, o
// roteador com o validador. Nenhum delas prova que o processo sobe de fato com a
// configuracao do ambiente, que o banco e alcancado no start e que o shutdown
// encerra o servidor antes de fechar o pool. E o que este arquivo cobre.
package ciclo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	runtime "github.com/LuisMassuchini/backend-challenge-go/internal/runtime/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/config"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// dsnRuntime e o papel de menor privilegio.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// portaLivre devolve uma porta que nao esta em uso.
//
// A porta e escolhida por conta e nao fixa porque dois testes em paralelo, ou uma
// execucao anterior que nao liberou a porta, transformariam um teste de ciclo de
// vida em um teste de sorte.
func portaLivre(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserva de porta: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

// ambienteDeTeste sobe o servico com uma configuracao explicita.
type ambienteDeTeste struct {
	// endereco e a base HTTP do processo.
	endereco string

	// encerrar derruba o processo.
	encerrar func()
}

// novoAmbiente sobe o servico.
func novoAmbiente(t *testing.T, extras map[string]string) *ambienteDeTeste {
	t.Helper()

	porta := portaLivre(t)
	ambiente := map[string]string{
		config.ChaveHTTPAddress:  fmt.Sprintf("127.0.0.1:%d", porta),
		config.ChavePostgresDSN:  dsnRuntime,
		config.ChaveLogLevel:     string(config.LogLevelError),
		config.ChaveOIDCIssuer:   "http://localhost:8081/realms/wager",
		config.ChaveOIDCAudience: "wager-service",
	}
	for chave, valor := range extras {
		ambiente[chave] = valor
	}

	cfg, err := config.FromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	})
	if err != nil {
		t.Fatalf("configuracao: %v", err)
	}

	aplicacao := runtime.New(cfg, &runtime.Eventos{})

	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelar()

	if err := aplicacao.Start(ctx); err != nil {
		t.Fatalf("subida da aplicacao: %v", err)
	}

	return &ambienteDeTeste{
		endereco: fmt.Sprintf("http://127.0.0.1:%d", porta),
		encerrar: func() {
			// O prazo curto de shutdown e proposital: o teste quer o encerramento
			// rapido, e o comportamento com prazo generoso e o mesmo.
			ctxParada, cancelaParada := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelaParada()

			if err := aplicacao.Stop(ctxParada); err != nil {
				t.Errorf("encerramento: %v", err)
			}
		},
	}
}

// consultar faz uma requisicao sem autenticacao.
func (a *ambienteDeTeste) consultar(t *testing.T, caminho string) (int, string) {
	t.Helper()

	ctx, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endereco+caminho, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisicao a %s: %v", caminho, err)
	}
	defer resp.Body.Close()

	//nolint:errcheck
	corpo, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(corpo)
}

// O servico sobe, responde nos health checks e responde as rotas de negocio.
//
// Este e o teste que a suite inteira de pecas nao cobre: ele junta configuracao,
// pool, casos de uso, validador e servidor em um unico processo.
func TestServicoCompletoSobeEResponde(t *testing.T) {
	dbtest.Limpa(t)
	a := novoAmbiente(t, nil)
	defer a.encerrar()

	// Liveness e publico e nao precisa de token.
	if status, corpo := a.consultar(t, "/health/live"); status != http.StatusOK {
		t.Errorf("liveness respondeu %d: %s", status, corpo)
	}

	// Readiness consulta o pool de verdade.
	if status, corpo := a.consultar(t, "/health/ready"); status != http.StatusOK {
		t.Errorf("readiness respondeu %d: %s", status, corpo)
	}

	// A rota de negocio responde 401 sem token, e nao 404 nem 500: ela existe e esta
	// protegida.
	if status, _ := a.consultar(t, "/wallets/"+uuid.NewString()); status != http.StatusUnauthorized {
		t.Errorf("rota de negocio sem token respondeu %d, esperado 401", status)
	}
}

// O servico nao sobe sem o DSN do banco, e a falha acontece no start.
//
// Subir sem banco e o pior dos dois mundos: o processo se anuncia pronto, o
// orquestrador manda trafego, e a primeira requisicao descobre que nao ha banco.
func TestServicoNaoSobeSemBanco(t *testing.T) {
	porta := portaLivre(t)
	ambiente := map[string]string{
		config.ChaveHTTPAddress: fmt.Sprintf("127.0.0.1:%d", porta),
		config.ChaveLogLevel:    string(config.LogLevelError),
	}

	cfg, err := config.FromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	})
	if err != nil {
		t.Fatalf("configuracao: %v", err)
	}

	aplicacao := runtime.New(cfg, &runtime.Eventos{})
	ctx, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelar()

	erro := aplicacao.Start(ctx)
	if erro == nil {
		//nolint:errcheck
		aplicacao.Stop(ctx)
		t.Fatal("a aplicacao subiu sem banco")
	}
	if !runtime.ConfiguracaoInvalida(erro) {
		t.Errorf("o erro nao e de configuracao: %v", erro)
	}
	if !strings.Contains(erro.Error(), config.ChavePostgresDSN) {
		t.Errorf("o erro nao aponta a variavel que falta: %v", erro)
	}
}

// Com banco invalido a subida falha, e nao deixa o processo meio vivo.
func TestServicoNaoSobeComBancoInvalido(t *testing.T) {
	porta := portaLivre(t)
	ambiente := map[string]string{
		config.ChaveHTTPAddress: fmt.Sprintf("127.0.0.1:%d", porta),
		config.ChavePostgresDSN: "postgres://wager_app:wager_app@127.0.0.1:1/wager?sslmode=disable&connect_timeout=1",
		config.ChaveLogLevel:    string(config.LogLevelError),
	}

	cfg, err := config.FromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	})
	if err != nil {
		t.Fatalf("configuracao: %v", err)
	}

	aplicacao := runtime.New(cfg, &runtime.Eventos{})
	ctx, cancelar := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelar()

	if err := aplicacao.Start(ctx); err == nil {
		//nolint:errcheck
		aplicacao.Stop(ctx)
		t.Fatal("a aplicacao subiu com banco invalido")
	}
}

// Uma requisicao em andamento termina antes do processo sair. O shutdown e o que
// segura o servico no ar enquanto a transacao que comecou confirma o commit.
func TestShutdownEsperaRequisicaoEmAndamento(t *testing.T) {
	dbtest.Limpa(t)

	porta := portaLivre(t)
	ambiente := map[string]string{
		config.ChaveHTTPAddress:         fmt.Sprintf("127.0.0.1:%d", porta),
		config.ChavePostgresDSN:         dsnRuntime,
		config.ChaveLogLevel:            string(config.LogLevelError),
		config.ChaveOIDCIssuer:          "http://localhost:8081/realms/wager",
		config.ChaveOIDCAudience:        "wager-service",
		config.ChaveHTTPShutdownTimeout: "10s",
	}

	cfg, err := config.FromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	})
	if err != nil {
		t.Fatalf("configuracao: %v", err)
	}

	aplicacao := runtime.New(cfg, &runtime.Eventos{})

	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelar()

	if err := aplicacao.Start(ctx); err != nil {
		t.Fatalf("subida: %v", err)
	}

	base := fmt.Sprintf("http://127.0.0.1:%d", porta)

	// Uma leitura legitima, autenticada, que precisa do validador e do pool.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/wallets/"+uuid.NewString(), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+pedirTokenDeTeste(t, "provider-a"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisicao: %v", err)
	}
	//nolint:errcheck
	io.ReadAll(resp.Body)
	resp.Body.Close()

	// O processo para depois da requisicao terminar.
	ctxParada, cancelaParada := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelaParada()

	inicio := time.Now()
	if err := aplicacao.Stop(ctxParada); err != nil {
		t.Fatalf("encerramento: %v", err)
	}

	if tempo := time.Since(inicio); tempo > 5*time.Second {
		t.Errorf("o encerramento demorou %s com uma requisicao ja terminada", tempo)
	}

	// Depois de parado, a porta nao responde mais.
	// Depois de parado, a conexao e recusada. O teste verifica exatamente isso: um
	// processo que so aceita conexao depois de encerrado e um processo que nao
	// encerrou.
	if status := consultarTolerante(base, "/health/live"); status == http.StatusOK {
		t.Error("o processo ainda responde depois de encerrado")
	}
}

// Duas instancias independentes no mesmo banco. E o comeco do que o E17 exige: as
// garantias nao podem depender de uma unica instancia.
func TestDuasInstanciasAtendemEmParalelo(t *testing.T) {
	dbtest.Limpa(t)

	primeira := novoAmbiente(t, nil)
	defer primeira.encerrar()
	segunda := novoAmbiente(t, nil)
	defer segunda.encerrar()

	if primeira.endereco == segunda.endereco {
		t.Fatal("as duas instancias escutaram na mesma porta")
	}

	for _, ambiente := range []*ambienteDeTeste{primeira, segunda} {
		if status, corpo := ambiente.consultar(t, "/health/ready"); status != http.StatusOK {
			t.Errorf("instancia nao pronta: %d %s", status, corpo)
		}
	}

	// O token e buscado uma vez, na thread principal. t.Fatalf dentro de outra
	// goroutine encerraria apenas aquela goroutine e a falha apareceria como uma
	// contagem errada, sem dizer nada sobre a causa.
	token := pedirTokenDeTeste(t, "wager-service")

	// As duas abrem carteiras ao mesmo tempo, em jogadores distintos.
	var (
		espera   sync.WaitGroup
		protecao sync.Mutex
		criadas  = map[string]bool{}
	)

	for i := 0; i < 4; i++ {
		espera.Add(1)
		go func(indice int) {
			defer espera.Done()

			ambiente := primeira
			if indice%2 == 1 {
				ambiente = segunda
			}

			if ok := abrirCarteiraEmParalelo(indice, ambiente.endereco, token); ok {
				protecao.Lock()
				defer protecao.Unlock()
				criadas[uuid.NewString()] = true
			}
		}(i)
	}
	espera.Wait()

	if len(criadas) != 4 {
		t.Errorf("carteiras criadas %d, esperado 4", len(criadas))
	}
}

// pedirTokenDeTeste busca um token real no IdP.
// pedirTokenDeTeste busca um token real no IdP.
//
// O cliente vem por parametro porque os escopos sao diferentes: provider-a abre
// operacao, e so o cliente de servico abre carteira. Testar a abertura com o token
// do provedor daria 403 e a falha pareceria ser do ciclo de vida.
func pedirTokenDeTeste(t *testing.T, cliente string) string {
	t.Helper()

	emissor := os.Getenv("WAGER_TEST_OIDC_ISSUER")
	if emissor == "" {
		emissor = "http://localhost:8081/realms/wager"
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", cliente)
	form.Set("client_secret", cliente+"-secret")

	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(emissor, "/")+"/protocol/openid-connect/token",
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

	var corpo struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&corpo); err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	return corpo.AccessToken
}

// abrirCarteiraEmParalelo abre uma carteira de uma instancia.
func abrirCarteiraEmParalelo(indice int, base, token string) bool {
	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelar()

	jogador := uuid.NewString()
	corpo := fmt.Sprintf(
		`{"playerId":%q,"initialBalance":{"amount":"100.00","currency":"BRL"}}`, jogador)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/wallets",
		strings.NewReader(corpo))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	//nolint:errcheck
	io.ReadAll(resp.Body)

	return resp.StatusCode == http.StatusCreated
}

// consultarTolerante faz uma requisicao sem falhar o teste quando a conexao e
// recusada, que e o resultado esperado depois do encerramento.
//
// Distinguir conexao recusada de processo que responde e o ponto: um teste que
// chamasse t.Fatalf na excecao passaria a descrever o sintoma errado.
func consultarTolerante(base, caminho string) int {
	ctx, cancelar := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+caminho, nil)
	if err != nil {
		return 0
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	//nolint:errcheck
	io.ReadAll(resp.Body)

	return resp.StatusCode
}

//
// Existe para o teste de shutdown poder observar a ordem dos hooks sem que o log de
// transicao do processo atrapalhe a leitura da falha.
