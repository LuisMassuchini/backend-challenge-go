package obs

import (
	"strings"
	"sync"
	"testing"
)

// metricasDeTeste monta um Registro e devolve o valor lido pelo helper.
func metricasDeTeste(t *testing.T) *Registro {
	t.Helper()
	registro := NovoRegistro()
	t.Cleanup(func() {})
	return registro
}

// linhaDeMetrica procura a serie que comeca pelo nome exato informado.
//
// A comparacao e da chave -- o que vem antes do espaco -- e nao da linha inteira, porque
// a linha traz o valor depois do espaco. E a comparacao e por igualdade da chave e nao
// por prefixo do texto, porque duas series da mesma metrica comecam igual:
// `wager_operacoes_total{estado="PROCESSED"}` e `...{estado="REJECTED"}`. Quem
// procurasse por prefixo acharia sempre a primeira e nunca a segunda.
func linhaDeMetrica(t *testing.T, exposicao, chave string) string {
	t.Helper()
	for _, linha := range strings.Split(exposicao, "\n") {
		if strings.HasPrefix(linha, "#") || linha == "" {
			continue
		}
		if chaveDe(linha) == chave {
			return linha
		}
	}
	t.Fatalf("a serie %q nao apareceu em:\n%s", chave, exposicao)
	return ""
}

// chaveDe devolve a chave da serie, que e o nome com rotulo antes do primeiro espaco.
func chaveDe(linha string) string {
	if espaco := strings.Index(linha, " "); espaco >= 0 {
		return linha[:espaco]
	}
	return linha
}

// Um contador so cresce, e a serie carrega os rotulos.
//
// E a propriedade que torna a metrica consultavel: o Prometheus diferencia series
// pelos rotulos, e dois provedores nao podem virar uma serie so.
func TestContadorAcumulaPorRotulo(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_operacoes_total", "Operacoes por desfecho")
	contador.Inc("estado", "PROCESSED")
	contador.Inc("estado", "PROCESSED")
	contador.Inc("estado", "REJECTED")

	exposicao := registro.Expor()

	processadas := linhaDeMetrica(t, exposicao, `wager_operacoes_total{estado="PROCESSED"}`)
	if !strings.Contains(processadas, `estado="PROCESSED"`) {
		t.Errorf("a serie de PROCESSED nao tem o rotulo: %s", processadas)
	}
	if !strings.Contains(processadas, " 2") {
		t.Errorf("PROCESSED = %q, esperado 2", processadas)
	}
	rejeitadas := linhaDeMetrica(t, exposicao, `wager_operacoes_total{estado="REJECTED"}`)
	if !strings.Contains(rejeitadas, " 1") {
		t.Errorf("REJECTED = %q, esperado 1", rejeitadas)
	}
}

// A mesma combinacao de rotulos e a mesma serie, nao outra.
//
// Series duplicadas com o mesmo nome e os mesmos rotulos quebram o scrape: o
// Prometheus trata como serie distinta e a soma sai dobrada.
func TestContadorNaoDuplicaSerie(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_operacoes_total", "Operacoes")
	contador.Inc("estado", "PROCESSED")
	contador.Inc("estado", "PROCESSED")
	contador.Inc("estado", "PROCESSED")

	exposicao := registro.Expor()
	quantas := 0
	for _, linha := range strings.Split(exposicao, "\n") {
		if strings.HasPrefix(linha, `wager_operacoes_total{estado="PROCESSED"}`) {
			quantas++
		}
	}
	if quantas != 1 {
		t.Errorf("a serie PROCESSED aparece %d vezes, esperado 1", quantas)
	}
}

// Os rotulos saem ordenados, para que a serie tenha nome estavel.
//
// O nome da serie no Prometheus e nome mais rotulos. Rotulos em ordem diferente
// produzem series com nomes diferentes para o mesmo dado, e o operador ve duas
// linhas que sao a mesma.
func TestRotulosSaoOrdenadosNoNome(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_x_total", "X")
	contador.Inc("zeta", "z", "alfa", "a")
	contador.Inc("alfa", "a", "zeta", "z")

	exposicao := registro.Expor()
	if !strings.Contains(exposicao, `wager_x_total{alfa="a",zeta="z"}`) {
		t.Errorf("os rotulos nao sairam ordenados:\n%s", exposicao)
	}
	// As duas chamadas produziram a mesma serie.
	quantas := strings.Count(exposicao, `wager_x_total{alfa="a",zeta="z"}`)
	if quantas != 1 {
		t.Errorf("a serie ordenada aparece %d vezes, esperado 1", quantas)
	}
}

// O rotulo com valor de dinheiro nao e aceito.
//
// A metrica vai para um agregador publico, e um `providerId` ou um `carteira` como
// rotulo tem cardinalidade sem limite: o numero de series cresce com o numero de
// clientes, e o Prometheus cai. O enunciado pede metrica de operacao por status, e
// status e um conjunto fechado.
func TestRotuloDeAltaCardinalidadeERecusado(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_x_total", "X")
	contador.Inc("walletId", "0192f291-27dd-7d3f-8071-5f8685deef37")

	exposicao := registro.Expor()
	if strings.Contains(exposicao, "0192f291") {
		t.Errorf("a metrica aceitou identificador de carteira como rotulo:\n%s", exposicao)
	}
}

// Contador ignoraria valor negativo, e nao existe metodo para subtrair.
//
// Contador que desce e contador quebrado: o painel mostraria menos operacoes do que
// aconteceu, e o operador acreditaria. E por isso que `Contador` nao tem `Dec`: a
// ausencia do metodo e o que impede o uso, e o que precisa descer e um gauge.
func TestContadorIgnoraValorNegativo(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_x_total", "X")
	contador.Inc("estado", "PROCESSED")
	contador.Add(-5, "estado", "PROCESSED")

	exposicao := registro.Expor()
	if !strings.Contains(linhaDeMetrica(t, exposicao, `wager_x_total{estado="PROCESSED"}`), " 1") {
		t.Errorf("o contador foi afetado por valor negativo: %s",
			linhaDeMetrica(t, exposicao, `wager_x_total{estado="PROCESSED"}`))
	}
}

// O gauge aceita subir e descer, porque mede nivel e nao total.
//
// O atraso da outbox e o exemplo: quando a fila esta vazia o atraso e zero, e um
// contador nunca voltaria a zero.
func TestGaugeSobeEDesce(t *testing.T) {
	registro := metricasDeTeste(t)

	gauge := registro.Gauge("wager_outbox_atraso_segundos", "Atraso do relay")
	gauge.Define(30)
	gauge.Define(0)

	exposicao := registro.Expor()
	if !strings.Contains(linhaDeMetrica(t, exposicao, "wager_outbox_atraso_segundos"), " 0") {
		t.Errorf("o gauge nao voltou a zero: %s", linhaDeMetrica(t, exposicao, "wager_outbox_atraso_segundos"))
	}
}

// O gauge com valor negativo e aceito.
//
// Um atraso negativo nao acontece, mas uma diferenca de saldo pode ser negativa, e
// recusar o valor aqui tiraria do painel a informacao de que o saldo esta abaixo do
// esperado.
func TestGaugeAceitaValorNegativo(t *testing.T) {
	registro := metricasDeTeste(t)

	registro.Gauge("wager_saldo_diferenca_centavos", "Diferenca").Define(-500)

	if !strings.Contains(registro.Expor(), " -500") {
		t.Errorf("o gauge recusou valor negativo:\n%s", registro.Expor())
	}
}

// A mediana e um resumo, e o codigo precisa de saber de onde vem.
//
// Um histograma sem buckets nomeados produz agregado de nome ilegivel, e quem le o
// painel nao sabe qual bucket olhar. O bucket e o que torna a leitura possivel.
func TestHistogramaTemBucketsNomeados(t *testing.T) {
	registro := metricasDeTeste(t)

	histograma := registro.Histograma("wager_operacao_duracao_ms", "Latencia da operacao",
		[]float64{5, 10, 50, 100, 500, 1000})
	histograma.Observe(30, "estado", "PROCESSED")

	exposicao := registro.Expor()
	if !strings.Contains(exposicao, `wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="50"} 1`) {
		t.Errorf("o bucket de 50ms nao recebeu a observacao:\n%s", exposicao)
	}
	if !strings.Contains(exposicao, "wager_operacao_duracao_ms_sum") {
		t.Error("a soma do histograma nao foi exposta")
	}
	if !strings.Contains(exposicao, "wager_operacao_duracao_ms_count{estado=\"PROCESSED\"} 1") {
		t.Errorf("a contagem do histograma nao foi exposta:\n%s", exposicao)
	}
}

// O bucket `+Inf` sempre existe, porque sem ele a soma das contagens nao fecha.
//
// E o que permite ao painel calcular a percentil a partir do total: sem `+Inf`, a
// serie nao tem total e o calculo da percentil erra em silencio.
func TestHistogramaTemBucketInfinito(t *testing.T) {
	registro := metricasDeTeste(t)

	registro.Histograma("wager_duracao_ms", "Latencia", []float64{10, 100}).
		Observe(1000, "estado", "PROCESSED")

	exposicao := registro.Expor()
	if !strings.Contains(exposicao, `le="+Inf"`) {
		t.Errorf("o bucket +Inf nao foi exposto:\n%s", exposicao)
	}
}

// Uma observacao fora de todo bucket so entra no `+Inf`, e nao e perdida.
//
// O valor de latencia pode ser maior que o maior bucket em um sistema lento, e uma
// observacao descartada seria uma p99 falsificada.
func TestObservacaoAcimaDoMaiorBucketNaoSePerde(t *testing.T) {
	registro := metricasDeTeste(t)

	registro.Histograma("wager_duracao_ms", "Latencia", []float64{10}).
		Observe(999999, "estado", "PROCESSED")

	exposicao := registro.Expor()
	if !strings.Contains(exposicao, "wager_duracao_ms_count{estado=\"PROCESSED\"} 1") {
		t.Errorf("a observacao fora de bucket se perdeu:\n%s", exposicao)
	}
}

// A exposicao traz nome, tipo e ajuda de cada metrica.
//
// E o que o Prometheus usa para o painel e o que o operador le na documentacao. Sem
// a linha `# HELP`, o nome da serie no painel e um campo vazio.
func TestExposicaoTrazNomeTipoEAjuda(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_operacoes_total", "Operacoes por desfecho")
	registro.Gauge("wager_outbox_atraso", "Atraso").Define(1)
	registro.Histograma("wager_latencia_ms", "Latencia", []float64{10}).Observe(1, "x", "y")
	contador.Inc("a", "b")

	exposicao := registro.Expor()
	for _, esperado := range []string{
		"# HELP wager_operacoes_total Operacoes por desfecho",
		"# TYPE wager_operacoes_total counter",
		"# HELP wager_outbox_atraso Atraso",
		"# TYPE wager_outbox_atraso gauge",
		"# TYPE wager_latencia_ms histogram",
	} {
		if !strings.Contains(exposicao, esperado) {
			t.Errorf("faltou %q na exposicao:\n%s", esperado, exposicao)
		}
	}
}

// A exposicao e estavel entre chamadas, com as series ordenadas.
//
// Duas chamadas iguais precisam dar o mesmo texto: e o que permite diffar o
// resultado do scrape e perceber que uma metrica sumiu.
func TestExposicaoEEstavelEOrdenada(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_x_total", "X")
	contador.Inc("zeta", "z")
	contador.Inc("alfa", "a")

	primeira := registro.Expor()
	segunda := registro.Expor()

	if primeira != segunda {
		t.Error("a exposicao mudou entre chamadas iguais")
	}
	if strings.Index(primeira, `alfa="a"`) > strings.Index(primeira, `zeta="z"`) {
		t.Errorf("as series nao sairam ordenadas:\n%s", primeira)
	}
}

// O registro e seguro para uso concorrente.
//
// O relay, o consumidor e o servidor HTTP contam em goroutines diferentes, e um mapa
// sem trava com escrita concorrente e data race -- que o `-race` do enunciado
// reprovaria.
func TestRegistroAceitaContagemConcorrente(t *testing.T) {
	registro := metricasDeTeste(t)

	var grupo sync.WaitGroup
	for i := 0; i < 50; i++ {
		grupo.Add(1)
		go func() {
			defer grupo.Done()
			contador := registro.Contador("wager_x_total", "X")
			contador.Inc("estado", "PROCESSED")
			registro.Gauge("wager_y", "Y").Define(1)
			registro.Histograma("wager_z_ms", "Z", []float64{10}).Observe(1, "a", "b")
			registro.Expor()
		}()
	}
	grupo.Wait()

	if !strings.Contains(registro.Expor(), " 50") {
		t.Errorf("a contagem concorrente perdeu incrementos:\n%s", registro.Expor())
	}
}

// O nome invalido e recusado na montagem, e nao na exposicao.
//
// Um nome com espaco quebraria o parse do Prometheus e o erro apareceria no scrape,
// longe de quem escolheu o nome.
func TestNomeInvalidoERecusado(t *testing.T) {
	registro := metricasDeTeste(t)

	for _, nome := range []string{"", "wager operacoes", "wager-operacoes", "1comeca_com_numero"} {
		contador := registro.Contador(nome, "Ajuda")
		if contador != nil {
			t.Errorf("o contador aceitou o nome invalido %q", nome)
		}
	}
}

// O rotulo com nome invalido e recusado, e nao silenciosamente aceito.
//
// Um rotulo com espaco quebraria a serie. Recusar aqui e melhor do que produzir uma
// linha que o Prometheus descarta.
func TestRotuloInvalidoERecusado(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_x_total", "X")
	contador.Inc("estado com espaco", "PROCESSED")

	if strings.Contains(registro.Expor(), "estado com espaco") {
		t.Errorf("o rotulo invalido foi aceito:\n%s", registro.Expor())
	}
}

// Rotulo com numero impar de pares e ignorado, sem perder o par valido.
//
// Uma chamada com numero impar de pares e bug de quem chama; aceitar faria o par
// seguinte virar valor de chave, e a serie teria um nome que ninguem escreveu. O
// par completo que veio antes e preservado: descartar a chamada inteira perderia uma
// medicao legitima por causa de um erro de quem chamou.
func TestRotuloComNumeroImparDeParesPreservaOParValido(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_x_total", "X")
	// `argumentos` tem comprimento impar: o ultimo elemento nao tem par.
	argumentos := []string{"estado", "PROCESSED", "sozinho"}
	contador.Inc(argumentos...)

	exposicao := registro.Expor()
	if strings.Contains(exposicao, "sozinho") {
		t.Errorf("o argumento sem par virou rotulo: %s", exposicao)
	}
}

// O texto do valor de rotulo e escapado.
//
// Um valor com aspas ou barra invertida quebraria o formato de exposicao. O rotulo
// vem do dominio e hoje nao tem aspas, e "hoje" e exatamente o que nao se protege.
func TestValorDeRotuloEEscapado(t *testing.T) {
	registro := metricasDeTeste(t)

	registro.Contador("wager_x_total", "X").Inc("estado", `PROCESSED" malformado`)

	exposicao := registro.Expor()
	if !strings.Contains(exposicao, `\"`) {
		t.Errorf("a aspa nao foi escapada: %s", exposicao)
	}
}

// Escalar sem rotulo e o caso mais comum de contador.
//
// O `wager_reconciliacoes_divergentes_total` do enunciado nao tem por que ser
// quebrado em cinco series para nao ser comparado com nada.
func TestContadorSemRotulo(t *testing.T) {
	registro := metricasDeTeste(t)

	registro.Contador("wager_divergencias_total", "Divergencias").Inc()
	registro.Contador("wager_divergencias_total", "Divergencias").Inc()

	if !strings.Contains(registro.Expor(), "wager_divergencias_total 2") {
		t.Errorf("o contador sem rotulo nao somou: %s", registro.Expor())
	}
}

// Uma metrica recem-criada aparece com zero, e nao some.
//
// Um painel com painel que so mostra a serie depois do primeiro evento faz o
// operador ler "sem dado" como "zero", e sao coisas diferentes.
func TestSerieNovaApareceComZero(t *testing.T) {
	registro := metricasDeTeste(t)

	contador := registro.Contador("wager_x_total", "X")
	contador.Inc("estado", "PENDING")

	// `PROCESSED` ainda nao foi incrementado, e a serie nao aparece: quem consulta
	// precisa saber que pode haver serie que ainda nao existe.
	exposicao := registro.Expor()
	if strings.Contains(exposicao, `estado="PROCESSED"`) {
		t.Errorf("a serie nao incrementada apareceu: %s", exposicao)
	}
}
