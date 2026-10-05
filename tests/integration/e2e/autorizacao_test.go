//go:build integration

package e2e

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

// tokenDeProvedor busca o token de um cliente do realm.
//
// Existe separada da `pedirToken` para que o nome do cliente apareca na linha da falha:
// um teste de isolamento que busca o token errado falha com 403, e o nome do cliente
// na linha e o que diz qual dos dois provedores foi usado.
func tokenDeProvedor(t *testing.T, cliente string) string {
	t.Helper()
	return pedirToken(t, cliente)
}

// Um provedor nao le a transacao de outro.
//
// Este e o eliminatorio "acesso nao autorizado a operacoes ou transacoes", e ele
// precisa ser verificado com o token real do outro provedor -- nao com um token
// fabricado. Um token fabricado provaria que o roteador compara strings, e nao que o
// IdP separa as identidades.
func TestProvedorNaoLeTransacaoDeOutro(t *testing.T) {
	limparBase(t)
	instancias := sobeInstancias(t, nil)

	tokenInterno := pedirToken(t, "wager-service")
	tokenA := tokenDeProvedor(t, "provider-a")
	tokenB := tokenDeProvedor(t, "provider-b")

	// provider-a abre a carteira e registra uma operacao.
	//
	// A abertura e pelo cliente interno porque carteira e operacao interna; o provedor
	// tem `wager:operacoes` e nunca tem `wager:carteira:abertura`. E a separacao
	// funcionando, e nao um defeito do teste.
	carteira := abrirCarteira(t, tokenInterno, instancias, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	externa := "transacao-do-provider-a-" + t.Name()
	resposta := instancias[0].chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenA, "provider-a:isolamento-"+t.Name(), "corr-isolamento",
		pedidoDeOperacao(carteira, jogador, externa, "BET", 2500))
	if resposta.Status != http.StatusOK {
		t.Fatalf("a operacao do provider-a respondeu %d: %v", resposta.Status, resposta.Corpo)
	}

	// provider-a consulta a propria transacao: 200.
	consultaA := instancias[0].chamarJSON(t, http.MethodGet,
		"/providers/provider-a/wagering/transactions/"+externa, tokenA, "", "", nil)
	if consultaA.Status != http.StatusOK {
		t.Errorf("provider-a consultando a propria transacao respondeu %d: %v",
			consultaA.Status, consultaA.Corpo)
	}

	// provider-b consulta a transacao de provider-a pela rota de provedor: 403.
	//
	// E 403 e nao 404 de proposito. Um 404 diria "nao existe", que e um dado: revelaria
	// que aquela transacao existe e pertence a outro. O 403 diz "voce nao pode", sem
	// dizer se existe.
	consultaB := instancias[0].chamarJSON(t, http.MethodGet,
		"/providers/provider-a/wagering/transactions/"+externa, tokenB, "", "", nil)
	if consultaB.Status != http.StatusForbidden {
		t.Errorf("provider-b consultando a transacao de provider-a respondeu %d, esperado 403: %v",
			consultaB.Status, consultaB.Corpo)
	}
}

// Um provedor nao escreve em nome de outro.
//
// O `providerId` do corpo e o do token: um provedor que mandasse o `providerId` do
// outro receberia 403 antes de qualquer movimento. E o que impede que `provider-a`
// registre aposta em nome de `provider-b` -- o que faria a transacao aparecer na
// extracao do provider errado.
func TestProvedorNaoEscreveEmNomeDeOutro(t *testing.T) {
	limparBase(t)
	instancias := sobeInstancias(t, nil)

	tokenInterno := pedirToken(t, "wager-service")
	tokenA := tokenDeProvedor(t, "provider-a")

	carteira := abrirCarteira(t, tokenInterno, instancias, 10000)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	// O token e do provider-a, mas o corpo diz provider-b.
	corpo := pedidoDeOperacao(carteira, jogador, "transacao-falsa-"+t.Name(), "BET", 2500)
	corpo["providerId"] = "provider-b"

	resposta := instancias[0].chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenA, "provider-a:escrita-indevida-"+t.Name(), "corr-indevida", corpo)

	if resposta.Status != http.StatusForbidden {
		t.Fatalf("provider-a escrevendo em nome de provider-b respondeu %d, esperado 403: %v",
			resposta.Status, resposta.Corpo)
	}

	// E nada foi aplicado: nem saldo, nem lancamento, nem transacao.
	if saldo := lerSaldo(t, tokenInterno, instancias[0], carteira); saldo != 10000 {
		t.Errorf("saldo e %d centavos, esperado 10000: o acesso indevido moveu dinheiro", saldo)
	}

	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != 0 {
		t.Errorf("o acesso indevido produziu %d debitos", debitos)
	}
	if creditos != 1 {
		t.Errorf("o ledger tem %d creditos, esperado 1 (a abertura)", creditos)
	}

	// E a transacao nem chegou a ser registrada. A leitura e pelo papel de dono
	// porque nao existe rota que o provedor pudesse usar para isso.
	if total := contarTransacoesDeProvedor(t, "provider-b"); total != 0 {
		t.Errorf("a transacao em nome de provider-b foi registrada %d vezes", total)
	}
}

// Um provedor sem o escopo interno nao abre carteira nem reconcilia.
//
// A separacao de escopo esta no IdP e nao no codigo, e este teste verifica que o IdP a
// impede. Um teste que so verificasse o codigo da aplicacao provaria que a aplicacao
// respeita o escopo, e nao que o escopo existe.
func TestProvedorNaoAbreNemReconcilia(t *testing.T) {
	limparBase(t)
	instancias := sobeInstancias(t, nil)

	tokenA := tokenDeProvedor(t, "provider-a")
	carteira := abrirCarteira(t, pedirToken(t, "wager-service"), instancias, 10000)

	abertura := instancias[0].chamarJSON(t, http.MethodPost, "/wallets", tokenA, "", "",
		map[string]any{
			"playerId": idDeIndice(99),
			"initialBalance": map[string]any{
				"amount": "50.00", "currency": "BRL",
			},
		})
	if abertura.Status != http.StatusForbidden {
		t.Errorf("provider-a abrindo carteira respondeu %d, esperado 403: %v",
			abertura.Status, abertura.Corpo)
	}

	reconciliacao := instancias[0].chamarJSON(t, http.MethodPost,
		"/wallets/"+carteira+"/reconciliation", tokenA, "", "", nil)
	if reconciliacao.Status != http.StatusForbidden {
		t.Errorf("provider-a reconciliando respondeu %d, esperado 403: %v",
			reconciliacao.Status, reconciliacao.Corpo)
	}

	// Nenhuma das duastentativas criou nada.
	if saldo := lerSaldo(t, pedirToken(t, "wager-service"), instancias[0], carteira); saldo != 10000 {
		t.Errorf("o saldo da carteira original mudou para %d", saldo)
	}
}

// Rota de negocio exige credencial valida.
//
// E o eliminatorio "autenticacao efetiva nos endpoints de negocio", verificado nos
// quatro estados que o operador precisa distinguir no log: sem header, com token que
// nao existe, com token de outro formato e com token valido.
func TestRotaDeNegogoExigeCredencialValida(t *testing.T) {
	limparBase(t)
	instancias := sobeInstancias(t, nil)

	carteira := abrirCarteira(t, pedirToken(t, "wager-service"), instancias, 10000)
	tokenValido := tokenDeProvedor(t, "provider-a")

	casos := []struct {
		nome        string
		autorizacao string
		esperado    int
	}{
		{"sem header", "", http.StatusUnauthorized},
		{"token invalido", "Bearer nao-e-um-token", http.StatusUnauthorized},
		{"token de outro formato", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
		{"token valido", "Bearer " + tokenValido, http.StatusOK},
	}

	for _, c := range casos {
		status := consultarComAutorizacao(t, instancias[0], "/wallets/"+carteira, c.autorizacao)
		if status != c.esperado {
			t.Errorf("%s respondeu %d, esperado %d", c.nome, status, c.esperado)
		}
	}
}

// consultarComAutorizacao faz um GET com o cabecalho informado e devolve o status.
func consultarComAutorizacao(t *testing.T, instancia *Instancia, caminho, autorizacao string) int {
	t.Helper()

	// O contexto e criado aqui e nao por um helper compartilhado porque e o unico
	// cenario que precisa de um GET com cabecalho arbitrario: os demais usam o
	// `chamar`, que ja monta o pedido completo.
	ctx, cancelar := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelar()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, instancia.endereco+caminho, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if autorizacao != "" {
		req.Header.Set("Authorization", autorizacao)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("requisicao: %v", err)
	}
	defer resp.Body.Close()
	//nolint:errcheck
	_, _ = io.ReadAll(resp.Body)

	return resp.StatusCode
}

// contarTransacoesDeProvedor devolve quantas transacoes o provedor tem.
func contarTransacoesDeProvedor(t *testing.T, provedor string) int {
	t.Helper()

	db, err := abrirLeitura(t)
	if err != nil {
		t.Fatalf("leitura: %v", err)
	}
	defer db.Close()

	var total int
	//nolint:errcheck
	err = db.QueryRow(
		"SELECT count(*) FROM wager_transactions WHERE provider_id = $1", provedor,
	).Scan(&total)
	if err != nil {
		t.Fatalf("contagem de transacoes: %v", err)
	}
	return total
}
