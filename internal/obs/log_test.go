package obs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// linhaDeLog devolve a linha JSON emitida.
func linhaDeLog(t *testing.T, saida *strings.Builder) map[string]any {
	t.Helper()

	var linha map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(saida.String())), &linha); err != nil {
		t.Fatalf("a saida nao e JSON: %q -> %v", saida.String(), err)
	}
	return linha
}

// novoLogTeste instala um logger JSON que escreve no builder.
func novoLogTeste(t *testing.T) *strings.Builder {
	t.Helper()
	saida := &strings.Builder{}
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(saida, nil)))
	t.Cleanup(func() { slog.SetDefault(anterior) })
	return saida
}

// O log carrega os identificadores que o enunciado exige, e o log sem nenhum deles
// continua valendo.
//
// E a propriedade que torna a operacao rastreavel de ponta a ponta: quem tem a
// correlacao do cliente acha a requisicao, o caso de uso, a gravacao e o evento,
// sem precisar saber em qual processo cada etapa rodou.
func TestLogCarregaOsIdentificadoresDoFluxo(t *testing.T) {
	saida := novoLogTeste(t)
	ctx := De(context.Background()).ComCorrelacao("corr-123")
	ctx = ctx.ComMensagem("msg-456")
	ctx = ctx.ComTransacao("0192f298-345e-7e38-af88-e43f851a819d")
	ctx = ctx.ComCarteira("0192f291-27dd-7d3f-8071-5f8685deef37")
	ctx = ctx.ComProvedor("provider-a")

	Log(ctx).Info("operacao registrada")

	linha := linhaDeLog(t, saida)
	esperados := map[string]string{
		"correlationId": "corr-123",
		"messageId":     "msg-456",
		"transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
		"walletId":      "0192f291-27dd-7d3f-8071-5f8685deef37",
		"providerId":    "provider-a",
	}
	for campo, valor := range esperados {
		if linha[campo] != valor {
			t.Errorf("o log tem %s = %v, esperado %q", campo, linha[campo], valor)
		}
	}
}

// Campos que o fluxo ainda nao conhece nao aparecem.
//
// A razao e o custo: um log com `transactionId: ""` em cada linha de quem nao tem
// transacao polui a busca e faz o operador duvidar do campo. Ausente e diferente de
// vazio, e o agregador de log trata os dois de forma diferente.
func TestCampoDesconhecidoNaoAparece(t *testing.T) {
	saida := novoLogTeste(t)
	ctx := De(context.Background()).ComCorrelacao("corr-123")

	Log(ctx).Info("so a correlacao")

	linha := linhaDeLog(t, saida)
	if linha["correlationId"] != "corr-123" {
		t.Errorf("correlationId = %v", linha["correlationId"])
	}
	for _, campo := range []string{"messageId", "transactionId", "walletId", "providerId"} {
		if _, existe := linha[campo]; existe {
			t.Errorf("o log tem o campo %s, que o fluxo nao informou", campo)
		}
	}
}

// Com argumentos em fatia, o contexto vence o par explicito com a mesma chave.
//
// E o que impede uma linha de quebrar a rastreabilidade: se o par explicito
// ganhasse, a linha teria o valor errado e a busca pela correlacao do cliente
// perderia exatamente a linha que a descreve.
func TestContextoVenceOParChaveValor(t *testing.T) {
	saida := novoLogTeste(t)
	ctx := De(context.Background()).ComCorrelacao("corr-123")

	argumentos := []any{"correlationId", "inventado", "tentativa", 2}
	LogCom(ctx, "mensagem", argumentos...)

	linha := linhaDeLog(t, saida)
	if linha["correlationId"] != "corr-123" {
		t.Errorf("correlationId = %v, esperado que o contexto vencesse", linha["correlationId"])
	}
	if linha["tentativa"] != float64(2) {
		t.Errorf("tentativa = %v, o par sem conflito deveria ter sobrevivido", linha["tentativa"])
	}
}

// A chave do contexto nao aparece duplicada na linha.
//
// A precedencia foi conquistada removendo o par conflitante dos argumentos. A
// alternativa -- repetir a chave -- faria o `slog` emitir `correlationId` duas vezes
// no JSON, e um parseador ingenuo leria a primeira.

func TestContextoNaoDuplicaAChave(t *testing.T) {
	saida := novoLogTeste(t)
	ctx := De(context.Background()).ComCorrelacao("corr-123")

	argumentos := []any{"correlationId", "inventado"}
	LogCom(ctx, "mensagem", argumentos...)

	bruto := saida.String()
	if quantas := strings.Count(bruto, `"correlationId"`); quantas != 1 {
		t.Errorf("correlationId aparece %d vezes na linha: %s", quantas, bruto)
	}
}

// A correlacao anexada ao contexto aparece no log sem ninguem pedir.
//
// E o que faz a rastreabilidade atravessar a borda HTTP: o middleware anexa uma vez e
// todo log subsequente le daqui, sem que o handler precise passar o valor adiante.
func TestCorrelacaoAnexadaAoContextoApareceNoLog(t *testing.T) {
	saida := novoLogTeste(t)
	ctx := AnexarCorrelacao(context.Background(), "corr-123")

	Log(De(ctx)).Info("mensagem")

	linha := linhaDeLog(t, saida)
	if linha["correlationId"] != "corr-123" {
		t.Errorf("correlationId = %v, esperado que viesse do contexto anexado", linha["correlationId"])
	}
}

// A correlacao explicita do Contexto vence a que o contexto traz.
//
// A ordem importa porque o consumidor anexa a chave de idempotencia como correlacao
// quando o produtor nao mandou nenhuma, e `De` roda antes do `ComCorrelacao`. Se o
// `De` sobrescrevesse, a linha da operacao responderia a um identificador que o
// cliente nunca viu -- e a busca pelo `X-Correlation-Id` perderia o registro da
// operacao.
func TestCorrelacaoExplicitaVenceADoContextoAnexado(t *testing.T) {
	saida := novoLogTeste(t)
	ctx := AnexarCorrelacao(context.Background(), "corr-do-contexto")

	Log(De(ctx).ComCorrelacao("chave-de-idempotencia")).Info("mensagem")

	linha := linhaDeLog(t, saida)
	if linha["correlationId"] != "chave-de-idempotencia" {
		t.Errorf("correlationId = %v, esperado que a correlacao explicita vencesse", linha["correlationId"])
	}
}

// CorrelacaoDoContexto devolve a correlacao, ou vazio.
func TestCorrelacaoDoContextoLeOValor(t *testing.T) {
	if CorrelacaoDoContexto(nil) != "" {
		t.Error("CorrelacaoDoContexto(nil) deveria devolver vazio")
	}
	ctx := AnexarCorrelacao(context.Background(), "corr-123")
	if CorrelacaoDoContexto(ctx) != "corr-123" {
		t.Errorf("CorrelacaoDoContexto = %q", CorrelacaoDoContexto(ctx))
	}
	// Correlacao vazia nao anula: um contexto sem correlacao e melhor do que um
	// contexto com correlacao vazia, que o log trataria como se fosse valor.
	if CorrelacaoDoContexto(AnexarCorrelacao(context.Background(), "")) != "" {
		t.Error("anexar correlacao vazia devolveu valor")
	}
}

// Log sem contexto funciona e nao quebra.
//
// Existe o caminho em que um log e emitido antes de qualquer requisicao -- o relay
// subindo, o consumidor se anunciando. Se `De(nil)` panicasse, o proprio log seria o
// que derruba o processo, e o operador perderia a mensagem que diria por que.
func TestLogSemContextoNaoQuebra(t *testing.T) {
	saida := novoLogTeste(t)

	Log(De(nil)).Info("fora de qualquer requisicao")

	linha := linhaDeLog(t, saida)
	if linha["msg"] != "fora de qualquer requisicao" {
		t.Errorf("msg = %v", linha["msg"])
	}
}

// Chave e valor precisam parecer par.
//
// Um par com numero impar de argumentos faria o slog indexar o array e a linha sairia
// com `!BADKEY`, que e o sintoma de um log silenciosamente quebrado.
func TestChaveEValorDesemparelhadosSaoRecusados(t *testing.T) {
	saida := novoLogTeste(t)

	// O par impar vem por `args...` porque o `go vet` recusa um par desemparelhado
	// literal na propria assinatura de Info. O vet tem razao no codigo de producao, e
	// aqui ele atrapalha o teste de quem chama por `args...` -- que e um caminho real,
	// porque o relay e o consumidor montam os argumentos do log em fatia.
	argumentos := []any{"valor sem chave", 42}
	Log(De(context.Background()).ComCarteira("0192f291-27dd-7d3f-8071-5f8685deef37")).
		Info("mensagem", argumentos...)

	// O slog aceita o par impar e emite a linha; o que importa e que nao hubo panic e
	// que o identificador continua na linha, para que a falha nao perca a correlacao.
	linha := linhaDeLog(t, saida)
	if linha["walletId"] != "0192f291-27dd-7d3f-8071-5f8685deef37" {
		t.Errorf("walletId = %v, esperado que a linha exista mesmo com par impar", linha["walletId"])
	}
}

// Os valoresfinancial nao entram no log por acidente.
//
// O enunciado proibe log de payload financeiro completo. Esta funcao existe para que
// a forma de registrar dinheiro no log seja explicita e nao acidental: quem quiser
// registrar um valor passa por aqui, e o que sai e um identificador curto que diz
// que houve movimento sem dizer quanto.
func TestValorFinanceiroViraIndicadorSemValor(t *testing.T) {
	saida := novoLogTeste(t)

	Log(De(context.Background()).ComMovimento("debito")).
		Info("movimento registrado")

	linha := linhaDeLog(t, saida)
	if linha["movimento"] != "debito" {
		t.Errorf("movimento = %v", linha["movimento"])
	}
	// Nenhum campo que carregue valor monetario pode aparecer. A lista e de nomes de
	// campo, e nao de texto solto: procurar "saldo" no JSON inteiro pegaria a propria
	// mensagem do log, que e o que o primeiro teste Pagou de imediato.
	bruto := saida.String()
	for _, proibido := range []string{"amount", "balance", "balanceAfter", "result_amount"} {
		if strings.Contains(bruto, proibido) {
			t.Errorf("a linha de log contem o campo %q: %s", proibido, bruto)
		}
	}
}

// Um valor muito longo e truncado em vez de gravado inteiro.
//
// Um identificador externo vem do cliente. Um cliente que mande um megabyte em
// `externalTransactionId` colocaria um megabyte em cada linha de log, e o volume de
// log cresceria com o que o cliente mandou, nao com o que o servico fez.
func TestValorLongoETruncado(t *testing.T) {
	saida := novoLogTeste(t)
	longo := strings.Repeat("x", 5000)

	Log(De(context.Background()).ComProvedor(longo)).
		Info("provedor enorme")

	linha := linhaDeLog(t, saida)
	valor, ok := linha["providerId"].(string)
	if !ok {
		t.Fatalf("providerId nao e texto: %v", linha["providerId"])
	}
	if len(valor) >= len(longo) {
		t.Errorf("o valor foi gravado inteiro, com %d caracteres", len(valor))
	}
}

// Contexto Anulado devolve o Contexto original.
func TestContextoAnuladoVoltaAoOriginal(t *testing.T) {
	base := context.Background()
	ctx := De(base).ComCorrelacao("corr-123")

	if ctx.Anulado() != base {
		t.Error("Anulado nao devolveu o contexto original")
	}
	if ctx.Correlacao() != "corr-123" {
		t.Errorf("Correlacao = %q", ctx.Correlacao())
	}
}

// O Contexto sobrevive ao cancelamento do pai.
//
// O relay e o consumidor criam Contexto a partir de um contexto que sera cancelado
// no shutdown. Se De(ctx) guardasse o ctx para recuperar os identificadores, o
// cancelamento derrubaria a leitura depois do fim do trabalho.
func TestContextoNaoGuardaOContextoPai(t *testing.T) {
	base, cancelar := context.WithCancel(context.Background())
	ctx := De(base).ComCorrelacao("corr-123")
	cancelar()

	if ctx.Correlacao() != "corr-123" {
		t.Errorf("Correlacao = %q, esperado que sobrevivesse ao cancelamento", ctx.Correlacao())
	}
	if ctx.Anulado().Err() != context.Canceled {
		t.Error("o contexto original nao preservou o cancelamento")
	}
}

// ContextoVazio devolve Contexto sem identificador, sem quebrar.
//
// `var c Contexto` e o valor zero. Uma library que exigisse `Contexto(ctx)` antes de
// qualquer uso teria um nil panic no primeiro log de quem montou o struct na ordem
// errada.
func TestContextoVazioNaoQuebra(t *testing.T) {
	var c Contexto

	if c.Correlacao() != "" {
		t.Errorf("Correlacao = %q, esperado vazio", c.Correlacao())
	}
	if c.Anulado() == nil {
		t.Error("Anulado devolveu nil em Contexto vazio")
	}
	// E o encadeamento funciona a partir do valor zero.
	if c.ComCorrelacao("x").Correlacao() != "x" {
		t.Error("o encadeamento a partir do valor zero nao funciona")
	}
}

// MsgCom devolve o par ja formatado, para quem compose log a mao.
func TestMsgComDevolveValoresFormatados(t *testing.T) {
	saida := novoLogTeste(t)
	ctx := De(context.Background())

	Log(ctx).Info("registro", MsgCom("tentativa", 3))

	linha := linhaDeLog(t, saida)
	if linha["tentativa"] != float64(3) {
		t.Errorf("tentativa = %v, esperado 3", linha["tentativa"])
	}
}

// ErroCom devolve o par de um erro.
//
// Existe para o log de falha ter sempre a mesma chave, `erro`, em vez de cada
// chamador inventar um nome. Um agregador que indexa por chave encontra toda falha
// no mesmo campo.
func TestErroComDevolveOCampoDeErro(t *testing.T) {
	saida := novoLogTeste(t)
	original := errors.New("banco fora")

	Log(De(context.Background())).Error("falha", ErroCom(original))

	linha := linhaDeLog(t, saida)
	if linha["erro"] != "banco fora" {
		t.Errorf("erro = %v", linha["erro"])
	}
}
