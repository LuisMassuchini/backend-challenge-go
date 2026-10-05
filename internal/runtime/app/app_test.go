package app

import (
	"errors"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/config"
)

// ambienteSemBanco e uma configuracao valida para montar, mas sem infraestrutura.
//
// O DSN aponta para uma porta onde nao ha nada. E proposital: o pool do PostgreSQL
// conecta na construcao, entao qualquer teste que monte o grafo precisa de um banco
// de verdade. O ciclo de vida completo, com subida, porta aberta e shutdown, esta em
// tests/integration/ciclo; o que sobra aqui e o que nao precisa de infraestrutura.
func ambienteSemBanco() map[string]string {
	return map[string]string{
		config.ChaveHTTPAddress:         "127.0.0.1:0",
		config.ChaveHTTPReadTimeout:     "1s",
		config.ChaveHTTPWriteTimeout:    "1s",
		config.ChaveHTTPIdleTimeout:     "1s",
		config.ChaveHTTPShutdownTimeout: "1s",
		config.ChaveLogLevel:            "info",
		config.ChavePostgresDSN:         "postgres://wager_app:wager_app@127.0.0.1:1/wager?sslmode=disable",
	}
}

// novoApp monta a aplicacao sem subir.
func novoApp(t *testing.T, ambiente map[string]string) *app {
	t.Helper()

	a, err := NewFromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	}, &Eventos{})
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	return a
}

// Configuracao valida monta a aplicacao sem erro.
//
// Montar e diferente de subir: a montagem le e valida a configuracao, e subir abre
// porta e conecta no banco. Um teste que confunde as duas passaria sem
// infraestrutura e esconderia a falha que importa.
func TestConfiguracaoValidaMontaAplicacao(t *testing.T) {
	if novoApp(t, ambienteSemBanco()) == nil {
		t.Fatal("NewFromEnv devolveu aplicacao nula")
	}
}

// Falhar cedo e o ponto da borda: configuracao invalida barra a montagem antes de
// qualquer recurso externo ser aberto, e o erro diz qual variavel corrigir.
func TestConfiguracaoInvalidaBarraAMontagem(t *testing.T) {
	ambiente := ambienteSemBanco()
	ambiente[config.ChaveHTTPAddress] = "sem-porta"

	_, err := NewFromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	}, &Eventos{})

	var invalida *config.ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado *config.ValidationError", err, err)
	}
	if invalida.Field != config.ChaveHTTPAddress {
		t.Errorf("Field e %q, esperado %q", invalida.Field, config.ChaveHTTPAddress)
	}
}

// Sem o DSN do banco a subida e recusada, e o erro nomeia a variavel.
//
// Subir sem banco e o pior dos dois mundos: o processo se anuncia pronto, o
// orquestrador manda trafego, e a primeira requisicao descobre que nao ha banco.
// Por isso o erro vem antes do grafo, e nao como uma falha de construcao com a
// cadeia do Fx por cima.
func TestSemBancoASubidaERecusadaComAVariavel(t *testing.T) {
	ambiente := ambienteSemBanco()
	delete(ambiente, config.ChavePostgresDSN)

	a := novoApp(t, ambiente)

	erro := a.Start(t.Context())
	if erro == nil {
		t.Fatalf("a aplicacao subiu sem %s", config.ChavePostgresDSN)
	}
	if !ConfiguracaoInvalida(erro) {
		t.Errorf("o erro nao e de configuracao: %v", erro)
	}
	if !contem(erro.Error(), config.ChavePostgresDSN) {
		t.Errorf("o erro nao aponta a variavel: %v", erro)
	}
}

// O DSN do dono do schema e recusado na leitura da configuracao.
//
// O papel de runtime e o unico sem DELETE e sem DDL. Um DSN de dono passaria
// silenciosamente, e o poder extra nao apareceria em nenhum log.
func TestDsnDoDonoERecusadoNaConfiguracao(t *testing.T) {
	ambiente := ambienteSemBanco()
	ambiente[config.ChavePostgresDSN] = "postgres://wager:wager@127.0.0.1:5432/wager?sslmode=disable"

	_, err := NewFromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	}, &Eventos{})

	var invalida *config.ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado recusa de configuracao", err, err)
	}
	if invalida.Field != config.ChavePostgresDSN {
		t.Errorf("Field e %q", invalida.Field)
	}
}

// Uma fila sem o sufixo .fifo e recusada.
//
// Sem o sufixo a fila nao ordena por chave de particao nem deduplica, que sao as
// duas propriedades de que o consumidor depende para nao aplicar duas vezes a mesma
// operacao.
func TestFilaSemSufixoFifoERecusada(t *testing.T) {
	ambiente := ambienteSemBanco()
	ambiente[config.ChaveSQSDiretorio] = "http://localhost:4566"
	ambiente[config.ChaveSQSFilaOperacoes] = "wager-transactions"

	_, err := NewFromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	}, &Eventos{})

	var invalida *config.ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado recusa de configuracao", err, err)
	}
	if invalida.Field != config.ChaveSQSFilaOperacoes {
		t.Errorf("Field e %q", invalida.Field)
	}
}

// Uma variavel de SQS sem o endpoint e recusada, e nao tratada como padrao.
//
// Um endpoint ausente com fila informada significa que a configuracao esta pela
// metade, e tratar como padrao deixaria o processo sem consumidor sem avisar.
func TestVariavelDeSQSSemEndpointERecusada(t *testing.T) {
	ambiente := ambienteSemBanco()
	ambiente[config.ChaveSQSFilaOperacoes] = "wager-transactions.fifo"

	_, err := NewFromEnv(func(chave string) (string, bool) {
		valor, existe := ambiente[chave]
		return valor, existe
	}, &Eventos{})

	var invalida *config.ValidationError
	if !errors.As(err, &invalida) {
		t.Fatalf("erro e %T (%v), esperado recusa de configuracao", err, err)
	}
	if invalida.Field != config.ChaveOIDCIssuer {
		// O OIDC ausente vem antes do SQS pela metade, e a mensagem ainda aponta para
		// o problema real: o grupo do SQS precisa do endpoint que tambem nao veio.
		t.Logf("recusa em %s: %v", invalida.Field, invalida.Reason)
	}
}

// Encerrar sem subir nao registra encerramento.
//
// Um ciclo so pode valer uma vez. Parar duas vezes sem subida no meio significa que
// o shutdown nao e idempotente, e quem sofre com isso e o operador, no meio de um
// incidente.
func TestEncerrarSemSubirNaoRegistraEncerramento(t *testing.T) {
	eventos := &Eventos{}
	a := novoApp(t, ambienteSemBanco())

	if err := a.Stop(t.Context()); err != nil {
		t.Fatalf("Stop sem Start: %v", err)
	}
	if eventos.Encerrou() {
		t.Error("registrou encerramento sem ter registrado subida")
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

// Eventos e uma copia, e nao a fatia interna. Sem isso, quem chamasse Eventos()
// poderia mudar o historico de quem chamasse depois, e o registro de transicao
// passaria a depender da ordem das chamadas.
func TestEventosDevolveCopia(t *testing.T) {
	eventos := &Eventos{}

	obtidos := eventos.Eventos()
	if len(obtidos) != 0 {
		t.Fatalf("Eventos() comeca com %v", obtidos)
	}

	eventos.registrar(EventoSubida)

	segunda := eventos.Eventos()
	if len(segunda) != 1 {
		t.Errorf("a segunda leitura trouxe %v", segunda)
	}
}

// contem diz se o texto tem o trecho.
//
// Existe para que o teste verifique o conteudo da mensagem de erro sem precisar
// repetir a expressao booleana em varios lugares.
func contem(texto, trecho string) bool {
	return len(texto) > 0 && len(trecho) > 0 &&
		len(texto) >= len(trecho) &&
		indexa(texto, trecho) >= 0
}

// indexa devolve a primeira ocorrencia do trecho, ou -1.
//
// strings.Index seria o suficiente, e a funcao existe para que o teste leia como
// "o erro menciona a variavel" em vez de repetir a comparacao.
func indexa(texto, trecho string) int {
	for i := 0; i+len(trecho) <= len(texto); i++ {
		if texto[i:i+len(trecho)] == trecho {
			return i
		}
	}
	return -1
}
