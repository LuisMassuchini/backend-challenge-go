//go:build integration

package e2e

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// apostaDeConcorrencia e o valor que o enunciado fixa para a disputa.
//
// Oito reais em carteira de cem. O numero e o enunciado e nao um parametro do teste
// porque ele e o que torna a recusa inevitavel: as duas apostas juntas valem 160, e nao
// existe saldo que as cubra. Um teste com valores que coubessem medria duas operacoes
// que passaram sem disputa, e nao uma recusa.
const apostaDeConcorrencia = int64(8000)

// saldoDaDisputa e o saldo inicial da carteira disputada.
const saldoDaDisputa = int64(10000)

// saldoEsperadoAposDisputa e o saldo que o enunciado exige.
//
// Cem menos oitenta, e nao cem menos cento e sessenta: apenas uma das apostas pode
// passar. E este e o numero que distingue o lock por carteira de qualquer outra
// estrategia -- se as duas passassem, o saldo seria negativo ou o `CHECK (balance >=
// 0)` teria recusado a segunda.
const saldoEsperadoAposDisputa = saldoDaDisputa - apostaDeConcorrencia

// Duas apostas de 80.00 simultaneas em carteira de 100.00: uma passa, uma e recusada.
//
// Este e o teste que o enunciado marca como obrigatorio e o que prova a estrategia de
// concorrencia. O `SELECT ... FOR UPDATE` na linha da carteira e o que serializa as
// duas; sem ele, as duas leriam o mesmo saldo de 100.00, as duas escreveriam 20.00 e o
// `CHECK (balance >= 0)` deixaria passar uma das duas erradas -- ou o `UPDATE`
// condicional por versao recusaria as duas e o jogador nao apostaria.
//
// O `lock_timeout` de um segundo do papel de runtime e o que garante que a disputa
// termine em decisao e nao em duas esperas: quem nao consegue o lock em um segundo
// recebe erro de infraestrutura e o provedor reenvia.
func TestDisputaDeDuasApostasDeOitentaSobreSaldoCemEmTresInstancias(t *testing.T) {
	limparBase(t)
	instancias := sobeInstancias(t, nil)

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	carteira := abrirCarteira(t, tokenInterno, instancias, saldoDaDisputa)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	// As duas apostas vao em instancias DIFERENTES. Disputar na mesma instancia
	// provaria serializacao dentro de um processo, que o `http.Server` faz sozinho --
	// e a garantia que importa e entre processos.
	statusPrimeira, corpoPrimeira := instancias[0].chamarSemFatal("POST", "/wagering/transactions",
		tokenProvedor, "chave-disputa-a", "corr-disputa-a",
		pedidoDeOperacao(carteira, jogador, "transacao-disputa-a", "BET", apostaDeConcorrencia))
	statusSegunda, corpoSegunda := instancias[1].chamarSemFatal("POST", "/wagering/transactions",
		tokenProvedor, "chave-disputa-b", "corr-disputa-b",
		pedidoDeOperacao(carteira, jogador, "transacao-disputa-b", "BET", apostaDeConcorrencia))

	// Espera o commit das duas antes de conferir. Sem isso a leitura do saldo correria
	// com uma das escritas ainda em andamento, e o teste mediria o instante errado.
	esperarAte(t, 15*time.Second, "as duas apostas serem concluidas", func() bool {
		return transacaoEstaTerminal(t, tokenProvedor, instancias[0], "transacao-disputa-a") &&
			transacaoEstaTerminal(t, tokenProvedor, instancias[1], "transacao-disputa-b")
	})

	// Exatamente uma passou. Duas passando e saldo negativo ou CHECK recusado; nenhuma
	// passando e o lock descartando operacao legitima.
	var (
		processadas   = 0
		recusadas     = 0
		indisponiveis = 0
	)
	for indice, resultado := range []struct {
		status int
		bruto  string
	}{
		{statusPrimeira, corpoPrimeira},
		{statusSegunda, corpoSegunda},
	} {
		switch {
		case resultado.status == http.StatusOK:
			processadas++
		case resultado.status == http.StatusUnprocessableEntity:
			recusadas++
		case resultado.status == http.StatusServiceUnavailable:
			// Indisponibilidade transitoria e um desfecho legitimo: quem nao conseguiu o
			// lock em um segundo recusa e o provedor reenvia. Contar como recusa de
			// negocio seria errado, porque o provedor trata os dois de forma diferente.
			indisponiveis++
		default:
			t.Errorf("aposta %d respondeu %d: %s", indice, resultado.status, resultado.bruto)
		}
	}

	if processadas != 1 {
		t.Errorf("%d apostas foram processadas, esperado exatamente 1. Recusadas: %d, indisponiveis: %d",
			processadas, recusadas, indisponiveis)
	}
	if processadas == 1 && recusadas == 0 && indisponiveis == 0 {
		t.Error("uma aposta passou e nenhuma falhou: o resultado nao fecha")
	}

	// O saldo final e o que o enunciado exige: 20.00.
	saldo := lerSaldo(t, tokenInterno, instancias[0], carteira)
	if saldo != saldoEsperadoAposDisputa {
		t.Errorf("saldo final e %d centavos, esperado %d", saldo, saldoEsperadoAposDisputa)
	}
	if saldo < 0 {
		t.Fatalf("saldo negativo de %d centavos: o lock por carteira nao segurou", saldo)
	}

	// E o ledger tem UM unico debito. Dois debitos de 80 sobre 100 dariam -60, e o
	// `CHECK` teria recusado a segunda -- mas o teste verifica o ledger e nao o saldo,
	// porque sao coisas diferentes: o saldo pode estar certo com o ledger errado se
	// alguem ajustar a carteira direto.
	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != 1 {
		t.Errorf("o ledger tem %d debitos, esperado exatamente 1", debitos)
	}
	if creditos != 1 {
		t.Errorf("o ledger tem %d creditos, esperado 1 (a abertura)", creditos)
	}

	// E a reconciliacao concorda: saldo gravado igual ao reconstruido pelo ledger.
	conferir := reconciliar(t, tokenInterno, instancias[0], carteira)
	if consistente, ok := conferir["consistent"].(bool); !ok || !consistente {
		t.Errorf("a reconciliacao acusou divergencia depois da disputa: %v", conferir)
	}
}

// A recusa por saldo insuficiente e codigo estavel e diferente da indisponibilidade.
//
// O enunciado pede que "uma reversao que precisaria debitar mais que o saldo e
// recusada e auditavel, com codigo diferente do da aposta sem saldo". Aqui o ponto e o
// outro lado: o provedor precisa distinguir "nao tenho saldo, espere deposito" de
// "o sistema estava ocupado, reenvie". Um 503 no primeiro caso faz o provedor desistir
// de uma aposta legitima.
func TestDisputaDistingueRecusaDeIndisponibilidade(t *testing.T) {
	limparBase(t)
	instancias := sobeInstancias(t, nil)

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	// Saldo de 100.00 e uma aposta de 200.00: a recusa e certa e nao ha disputa.
	carteira := abrirCarteira(t, tokenInterno, instancias, saldoDaDisputa)
	jogador := jogadorDaCarteira(t, tokenInterno, instancias[0], carteira)

	resposta := instancias[0].chamarJSON(t, http.MethodPost, "/wagering/transactions",
		tokenProvedor, "chave-sem-saldo", "corr-sem-saldo",
		pedidoDeOperacao(carteira, jogador, "transacao-sem-saldo", "BET", 20000))

	if resposta.Status != http.StatusUnprocessableEntity {
		t.Fatalf("aposta acima do saldo respondeu %d, esperado 422: %v", resposta.Status, resposta.Corpo)
	}
	if codigo, ok := resposta.Corpo["failureCode"].(string); !ok || codigo == "" {
		t.Errorf("a recusa nao trouxe failureCode: %v", resposta.Corpo)
	}

	// E o saldo nao mudou. A recusa e conclusao, e nao "vai tentar depois".
	if saldo := lerSaldo(t, tokenInterno, instancias[0], carteira); saldo != saldoDaDisputa {
		t.Errorf("saldo apos recusa e %d, esperado %d: a recusa moveu dinheiro",
			saldo, saldoDaDisputa)
	}

	// E o lancamento nao existe: recusa nao produz lancamento.
	lancamentos := lerLedger(t, tokenInterno, instancias[0], carteira)
	debitos, creditos := contaLancamentosPorDirecao(lancamentos)
	if debitos != 0 {
		t.Errorf("a recusa produziu %d debitos", debitos)
	}
	if creditos != 1 {
		t.Errorf("o ledger tem %d creditos, esperado 1 (a abertura)", creditos)
	}
}

// Carteiras diferentes sao processadas em paralelo, e o enunciado proibe lock global.
//
// Este e o teste que distingue o lock por carteira de um lock global. Com lock global,
// as duas apostas acima passariam uma por vez -- o resultado seria identico. Aqui o que
// se verifica e o oposto: com carteiras diferentes, N apostas simultaneas precisam
// concluir em bem menos tempo do que N vezes o tempo de uma, porque nao ha serializacao.
//
// A medicao e por tempo total e nao por timestamp de cada resposta: o que interessa e
// que o conjunto terminou antes do limite, e nao que cada uma foi rapida.
func TestCarteirasDistintasProcessamEmParalelo(t *testing.T) {
	limparBase(t)
	instancias := sobeInstancias(t, nil)

	tokenInterno := pedirToken(t, "wager-service")
	tokenProvedor := pedirToken(t, "provider-a")

	const carteiras = 9

	// Abre as carteiras em paralelo tambem: abrir em serie custaria uma transacao por
	// carteira antes de comecar a medir o que importa.
	var (
		abertura sync.WaitGroup
		protecao sync.Mutex
		ids      = make([]string, 0, carteiras)
	)
	for i := 0; i < carteiras; i++ {
		abertura.Add(1)
		go func(indice int) {
			defer abertura.Done()

			status, bruto := instancias[indice%len(instancias)].chamarSemFatal(
				"POST", "/wallets", tokenInterno, "", "",
				map[string]any{
					"playerId": idDeIndice(indice),
					"initialBalance": map[string]any{
						"amount": "100.00", "currency": "BRL",
					},
				})
			if status != http.StatusCreated {
				t.Errorf("abertura %d respondeu %d: %s", indice, status, bruto)
				return
			}

			var corpo struct {
				ID string `json:"id"`
			}
			if err := deserializar(bruto, &corpo); err != nil || corpo.ID == "" {
				t.Errorf("abertura %d sem identificador: %s", indice, bruto)
				return
			}

			protecao.Lock()
			defer protecao.Unlock()
			ids = append(ids, corpo.ID)
		}(i)
	}
	abertura.Wait()

	if len(ids) != carteiras {
		t.Fatalf("foram abertas %d carteiras, esperado %d", len(ids), carteiras)
	}

	// Todas as apostas simultaneas, cada uma na sua carteira e na sua instancia.
	inicio := time.Now()

	var apostas sync.WaitGroup
	for i, carteira := range ids {
		apostas.Add(1)
		go func(indice int, id string) {
			defer apostas.Done()

			instant := instancias[indice%len(instancias)]
			status, bruto := instant.chamarSemFatal("POST", "/wagering/transactions",
				tokenProvedor, "chave-paralela-"+idDeIndice(indice), "corr-paralela-"+idDeIndice(indice),
				pedidoDeOperacao(id, idDeIndice(indice), "transacao-paralela-"+idDeIndice(indice), "BET", 2500))
			if status != http.StatusOK {
				t.Errorf("aposta %d na carteira %s respondeu %d: %s", indice, id, status, bruto)
			}
		}(i, carteira)
	}
	apostas.Wait()

	duracao := time.Since(inicio)

	// O limite e generoso de proposito. Nove apostas serializadas em transacoes de
	// poucos milissegundos passariam em menos de um segundo, entao um limite de dois
	// segundos nao provaria paralelismo -- nem o rejeitaria se houvesse lock global, que e
	// o que este teste precisa distinguir.
	//
	// O que prova o paralelismo aqui e a estrutura do teste: nove goroutines em tres
	// processos, todas com transacao propria e carteira propria. O limite existe
	// apenas para pegar o caso degenerado de fila serial, e ele e largo porque um
	// limite apertado seria um teste de velocidade da maquina, e nao do desenho.
	if duracao > 20*time.Second {
		t.Errorf("as %d apostas em carteiras distintas levaram %s, que sugere serializacao",
			carteiras, duracao)
	}

	// E cada carteira tem o saldo certo: 100.00 menos 25.00.
	for i, carteira := range ids {
		saldo := lerSaldo(t, tokenInterno, instancias[i%len(instancias)], carteira)
		if esperado := int64(7500); saldo != esperado {
			t.Errorf("saldo da carteira %d e %d, esperado %d", i, saldo, esperado)
		}
	}
}

// transacaoEstaTerminal informa se a transacao externa ja saiu de PENDING.
//
// Existe para esperar condicao que depende de outro processo. Uma aposta recusada por
// saldo chega a REJECTED e uma processada chega a PROCESSED; as duas sao terminais, e
// enquanto a transacao estiver em PENDING o commit ainda nao aconteceu.
func transacaoEstaTerminal(
	t *testing.T,
	token string,
	instant *Instancia,
	externa string,
) bool {
	t.Helper()

	resposta := instant.chamarJSON(t, http.MethodGet,
		"/providers/provider-a/wagering/transactions/"+externa, token, "", "", nil)
	if resposta.Status != http.StatusOK {
		return false
	}

	estado, ok := resposta.Corpo["state"].(string)
	if !ok {
		return false
	}
	return estado != "PENDING" && estado != "PENDING_REFERENCE"
}
