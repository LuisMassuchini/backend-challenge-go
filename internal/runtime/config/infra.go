package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// chaves dos grupos PostgreSQL e SQS.
const (
	ChavePostgresDSN            = "WAGER_POSTGRES_DSN"
	ChavePostgresMaxConexoes    = "WAGER_POSTGRES_MAX_CONEXOES"
	ChavePostgresTimeoutConexao = "WAGER_POSTGRES_TIMEOUT_CONEXAO"

	ChaveSQSDiretorio      = "WAGER_SQS_ENDPOINT"
	ChaveSQSFilaOperacoes  = "WAGER_SQS_FILA_OPERACOES"
	ChaveSQSFilaDeadLetter = "WAGER_SQS_FILA_DEAD_LETTER"
	ChaveSQSVisaoTimeout   = "WAGER_SQS_VISIBILITY_TIMEOUT"
	ChaveSQSRecebimentoMax = "WAGER_SQS_RECEIVE_COUNT_MAX"
	ChaveSQSLoteMax        = "WAGER_SQS_LOTE_MAX"
)

// Postgres e o endereco e os limites do banco.
type Postgres struct {
	// DSN e a conexao do papel de runtime, o de menor privilegio.
	DSN string

	// MaxConexoes e o tamanho do pool.
	//
	// O valor e pequeno de proposito: a contencao deste sistema e por carteira, e
	// conexoes extras nao resolvem disputa de lock, apenas acumulam espera. Um pool
	// grande com todos os workers em espera no mesmo locktimeout produz uma fila em
	// que ate as transacoes que nao disputam o mesmo dinheiro falham.
	MaxConexoes int32

	// TimeoutConexao limita quanto tempo o pool espera por uma conexao livre.
	TimeoutConexao time.Duration
}

// SQS e o endereco do LocalStack e as filas do consumidor.
type SQS struct {
	// Endpoint e o endereco do LocalStack.
	Endpoint string

	// FilaOperacoes e a fila FIFO de operacoes.
	FilaOperacoes string

	// FilaDeadLetter e a fila de cartao morto.
	FilaDeadLetter string

	// VisaoTimeout e por quanto tempo a mensagem some para os demais consumidores
	// depois de recebida.
	//
	// E o que distribui o trabalho entre tres instancias: sem isso, uma mensagem
	// recebida por uma instancia morre no meio do processamento e so volta quando o
	// timeout expira. O valor tem de ser maior que o pior tempo de processamento.
	VisaoTimeout time.Duration

	// RecebimentoMax e quantas tentativas antes de ir para a cartao morto.
	RecebimentoMax int

	// LoteMax e quantas mensagens sao lidas por chamada.
	LoteMax int32
}

// padroes dos dois grupos.
const (
	// maxConexoesPadrao e pequeno porque a contencao e por carteira.
	maxConexoesPadrao = 16

	// timeoutConexaoPadrao e curto: um sistema que espera mais que isso por
	// conexiao ja esta em situacao que o health check precisa reportar.
	timeoutConexaoPadrao = 3 * time.Second

	// visaoTimeoutPadrao e o dobro do pior processamento esperado.
	//
	// A FIFO exige que o processador delete a mensagem em ate trinta segundos.
	// Com este valor, uma instancia que morre no meio do trabalho devolve a
	// mensagem em um minuto, e nao em trinta.
	visaoTimeoutPadrao = 60 * time.Second

	// recebimentoMaxPadrao e o numero de tentativas antes da cartao morto.
	//
	// Tres tentativas e o equilibrio entre absorver uma falha transitoria de banco e
	// nao ficar reentregando uma mensagem que sempre vai falhar, gastando tempo de
	// CPU e escondendo o defeito.
	recebimentoMaxPadrao = 3

	loteMaxPadrao = 10
)

// lerPostgres le o endereco do banco.
//
// O grupo e opcional aqui, e obrigatorio em quem serve requisicoes. A divisao e
// deliberada: FromEnv le o que existe e valida o que veio, e quem decide o que e
// obrigatorio para aquele binario e o binario. Exigir o banco em FromEnv quebraria
// o comando de migration, que usa o dono do schema e nao precisa de nada disso.
func lerPostgres(getenv func(string) (string, bool)) (Postgres, error) {
	bruto, definido := getenv(ChavePostgresDSN)
	dsn := strings.TrimSpace(bruto)

	if dsn == "" {
		if definido {
			return Postgres{}, &ValidationError{
				Field:  ChavePostgresDSN,
				Reason: "a variavel esta presente e vazia; remova a linha ou informe a conexao do papel de runtime",
			}
		}
		return Postgres{}, nil
	}

	// O papel de runtime nao tem DELETE nem DDL. Um DSN que aponte para o dono do
	// schema passa, mas entrega mais poder do que o processo precisa, e esse poder
	// nao aparece em nenhum log.
	if strings.Contains(dsn, "wager:wager@") {
		return Postgres{}, &ValidationError{
			Field:  ChavePostgresDSN,
			Reason: "o DSN aponta para o dono do schema; use o papel de runtime, que e o unico sem DELETE e sem DDL",
		}
	}

	max, err := lerInteiro(getenv, ChavePostgresMaxConexoes, maxConexoesPadrao, 1, 500)
	if err != nil {
		return Postgres{}, err
	}
	timeout, err := lerDuracao(getenv, ChavePostgresTimeoutConexao, timeoutConexaoPadrao)
	if err != nil {
		return Postgres{}, err
	}

	return Postgres{
		DSN:            dsn,
		MaxConexoes:    int32(max),
		TimeoutConexao: timeout,
	}, nil
}

// lerSQS le o endereco do LocalStack e as filas.
//
// O grupo e opcional junto com o PostgreSQL: as migrations nao tocam fila nenhuma, e
// exigir SQS para rodar uma migration acopla duas dependencias que nao tem relacao.
func lerSQS(getenv func(string) (string, bool)) (SQS, error) {
	endpoint, definido := getenv(ChaveSQSDiretorio)
	endpoint = strings.TrimSpace(endpoint)

	if endpoint == "" {
		if definido {
			return SQS{}, &ValidationError{
				Field:  ChaveSQSDiretorio,
				Reason: "a variavel esta presente e vazia; remova a linha para desligar o consumidor, ou informe o endereco, como \"http://localhost:4566\"",
			}
		}
		for _, chave := range []string{
			ChaveSQSFilaOperacoes, ChaveSQSFilaDeadLetter, ChaveSQSVisaoTimeout,
			ChaveSQSRecebimentoMax, ChaveSQSLoteMax,
		} {
			if _, presente := getenv(chave); presente {
				return SQS{}, &ValidationError{
					Field:  chave,
					Reason: fmt.Sprintf("so faz sentido com %s, que nao foi informada", ChaveSQSDiretorio),
				}
			}
		}
		return SQS{}, nil
	}

	fila, err := lerTexto(getenv, ChaveSQSFilaOperacoes, "wager-transactions.fifo")
	if err != nil {
		return SQS{}, err
	}
	cartaoMorto, err := lerTexto(getenv, ChaveSQSFilaDeadLetter, "wager-transactions-dlq.fifo")
	if err != nil {
		return SQS{}, err
	}

	// As duas filas sao FIFO por nome. Um sufixo diferente transforma a fila em
	// padrao, e a FIFO e o que da ordem por chave de particao e a deduplicacao.
	for _, nome := range []struct{ chave, valor string }{
		{ChaveSQSFilaOperacoes, fila},
		{ChaveSQSFilaDeadLetter, cartaoMorto},
	} {
		if !strings.HasSuffix(nome.valor, ".fifo") {
			return SQS{}, &ValidationError{
				Field:  nome.chave,
				Reason: fmt.Sprintf("a fila %q precisa do sufixo .fifo: sem ele a fila nao ordena por chave de particao nem deduplica", nome.valor),
			}
		}
	}

	visao, err := lerDuracao(getenv, ChaveSQSVisaoTimeout, visaoTimeoutPadrao)
	if err != nil {
		return SQS{}, err
	}
	recebimentos, err := lerInteiro(getenv, ChaveSQSRecebimentoMax, recebimentoMaxPadrao, 1, 1000)
	if err != nil {
		return SQS{}, err
	}
	lote, err := lerInteiro(getenv, ChaveSQSLoteMax, loteMaxPadrao, 1, 10)
	if err != nil {
		return SQS{}, err
	}

	return SQS{
		Endpoint:       endpoint,
		FilaOperacoes:  fila,
		FilaDeadLetter: cartaoMorto,
		VisaoTimeout:   visao,
		RecebimentoMax: recebimentos,
		LoteMax:        int32(lote),
	}, nil
}

// lerTexto le um texto com padrao.
func lerTexto(getenv func(string) (string, bool), chave, padrao string) (string, error) {
	bruto, definido := getenv(chave)
	bruto = strings.TrimSpace(bruto)

	if bruto == "" {
		if definido {
			return "", &ValidationError{
				Field:  chave,
				Reason: fmt.Sprintf("a variavel esta presente e vazia; remova a linha ou informe %q", padrao),
			}
		}
		return padrao, nil
	}
	return bruto, nil
}

// lerInteiro le um inteiro com padrao e faixa.
//
// A faixa existe porque os dois valores que usam esta funcao sao limites de recurso:
// conexoes e lotes. Um valor absurdo nao falha no start com um numero invalido, e
// sim em producao, com o processo em contencao ou com uma rajada de mil mensagens.
func lerInteiro(
	getenv func(string) (string, bool),
	chave string,
	padrao, minimo, maximo int,
) (int, error) {
	bruto, definido := getenv(chave)
	bruto = strings.TrimSpace(bruto)

	if bruto == "" {
		if definido {
			return 0, &ValidationError{
				Field:  chave,
				Reason: fmt.Sprintf("a variavel esta presente e vazia; remova a linha ou informe um valor entre %d e %d", minimo, maximo),
			}
		}
		return padrao, nil
	}

	valor, err := parseInteiro(bruto)
	if err != nil {
		return 0, &ValidationError{
			Field:  chave,
			Reason: fmt.Sprintf("valor invalido %q: informe um inteiro entre %d e %d", bruto, minimo, maximo),
		}
	}
	if valor < minimo || valor > maximo {
		return 0, &ValidationError{
			Field:  chave,
			Reason: fmt.Sprintf("valor %d fora da faixa: informe entre %d e %d", valor, minimo, maximo),
		}
	}
	return valor, nil
}

// parseInteiro converte texto em inteiro.
func parseInteiro(bruto string) (int, error) {
	return strconv.Atoi(bruto)
}
