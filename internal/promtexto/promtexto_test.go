package promtexto

import (
	"math"
	"strings"
	"testing"
)

// textoDeExemplo e um trecho de exposicao com dois histogramas.
//
// Os valores dos buckets sao ACUMULADOS, como o Prometheus escreve: a linha
// `..._bucket{le="50"} 60` ja vale sessenta, e nao "mais sessenta". A serie PROCESSED
// tem cem observacoes, para que p50, p95 e p99 caiam em TRES buckets diferentes -- com
// dez observacoes os tres quantis caem no mesmo bucket e o teste passa a nao provar
// nada:
//
//	p50 visa 50,0  -> primeiro acumulado que alcanca: le=50  (60)  -> [10, 50)
//	p95 visa 95,0  -> primeiro acumulado que alcanca: le=250 (97)  -> [100, 250)
//	p99 visa 99,0  -> primeiro acumulado que alcanca: le=500 (99)  -> [250, 500)
const textoDeExemplo = `# HELP wager_operacao_duracao_ms Duracao do processamento da operacao
# TYPE wager_operacao_duracao_ms histogram
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="5"} 10
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="10"} 25
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="50"} 60
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="100"} 90
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="250"} 97
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="500"} 99
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="+Inf"} 100
wager_operacao_duracao_ms_sum{estado="PROCESSED"} 950
wager_operacao_duracao_ms_count{estado="PROCESSED"} 100
wager_operacao_duracao_ms_bucket{estado="REJECTED",le="5"} 2
wager_operacao_duracao_ms_bucket{estado="REJECTED",le="10"} 3
wager_operacao_duracao_ms_bucket{estado="REJECTED",le="50"} 4
wager_operacao_duracao_ms_bucket{estado="REJECTED",le="100"} 4
wager_operacao_duracao_ms_bucket{estado="REJECTED",le="+Inf"} 4
wager_operacao_duracao_ms_sum{estado="REJECTED"} 33
wager_operacao_duracao_ms_count{estado="REJECTED"} 4
wager_operacoes_total{estado="PROCESSED"} 100
`

func TestBuscarAchaASeriePeloNomeERotulo(t *testing.T) {
	serie := Buscar(textoDeExemplo, "wager_operacao_duracao_ms", map[string]string{"estado": "PROCESSED"})
	if serie == nil {
		t.Fatal("a serie PROCESSED nao foi encontrada no texto de exemplo")
	}

	if serie.Contagem != 100 {
		t.Errorf("contagem %v, esperada 100", serie.Contagem)
	}
	if serie.Soma != 950 {
		t.Errorf("soma %v, esperada 950", serie.Soma)
	}
	if serie.Media() != 9.5 {
		t.Errorf("media %v, esperada 9.5", serie.Media())
	}
}

func TestQuantilDevolveFaixaET_naoNumeroInventado(t *testing.T) {
	serie := Buscar(textoDeExemplo, "wager_operacao_duracao_ms", map[string]string{"estado": "PROCESSED"})
	if serie == nil {
		t.Fatal("serie nao encontrada")
	}

	// Os valores dos buckets sao ACUMULADOS, como o Prometheus escreve. Com cem
	// observacoes, o acumulado e 10, 25, 60, 90, 97, 99, 100.
	casos := []struct {
		nome           string
		quantil        float64
		minimo, maximo float64
	}{
		{"p50", 0.50, 10, 50},
		{"p95", 0.95, 100, 250},
		{"p99", 0.99, 250, 500},
	}

	for _, caso := range casos {
		minimo, maximo, ok := serie.Quantil(caso.quantil)
		if !ok {
			t.Errorf("%s: sem faixa calculada", caso.nome)
			continue
		}
		if minimo != caso.minimo || maximo != caso.maximo {
			t.Errorf("%s: faixa [%v, %v), esperada [%v, %v)",
				caso.nome, minimo, maximo, caso.minimo, caso.maximo)
		}
	}
}

func TestAcumuladoDosBucketsNaoEDobrado(t *testing.T) {
	// O erro mais obvio ao ler histograma: somar os valores dos buckets. Como o
	// Prometheus ja escreve acumulado, somar contaria cada observacao tantas vezes
	// quantos limites ela atravessasse.
	//
	// Com dez observacoes em cinco limites, o total acumulado corretamente e dez. Somado
	// por cima daria dezesseis. O sintoma seria um p99 caindo no bucket errado -- que e
	// exatamente o que aconteceu na primeira versao deste codigo.
	//
	// O teste que pega e o de consistencia: o acumulado do bucket `+Inf` tem que fechar
	// com o `_count` declarado, porque so o ultimo bucket traz o total.
	serie := Buscar(textoDeExemplo, "wager_operacao_duracao_ms", map[string]string{"estado": "PROCESSED"})
	if serie == nil {
		t.Fatal("serie nao encontrada")
	}

	if len(serie.buckets) != 7 {
		t.Fatalf("a serie tem %d buckets, esperados 7", len(serie.buckets))
	}

	maior := serie.buckets[len(serie.buckets)-1]
	if maior.limite != math.Inf(1) {
		t.Fatalf("o ultimo bucket tem limite %v, esperado +Inf", maior.limite)
	}
	if maior.acumulado != serie.Contagem {
		t.Errorf("o acumulado do bucket +Inf e %v e o _count e %v: eles precisam fechar, e so fecham se os buckets nao foram somados por cima",
			maior.acumulado, serie.Contagem)
	}
}

func TestFaixaNaoPodeSerMaisEstreitaQueOBucketReal(t *testing.T) {
	// A propriedade que a interpolacao linear violaria: as respostas tem de ser limites
	// REAIS do histograma, e nao numeros dentro deles.
	//
	// Com quatro observacoes, todas abaixo de 50, o p95 tem de responder um intervalo
	// cujos extremos sao limites declarados -- e nao "38.42".
	//
	// O zero entra como limite declarado porque e o limite inferior implicito do primeiro
	// bucket: uma observacao que cabe em `le="5"` so tem faixa `[0, 5)` a oferecer.
	declarados := map[float64]bool{
		0: true, 5: true, 10: true, 50: true, 100: true, math.Inf(1): true,
	}

	serie := Buscar(textoDeExemplo, "wager_operacao_duracao_ms", map[string]string{"estado": "REJECTED"})
	if serie == nil {
		t.Fatal("serie nao encontrada")
	}

	for _, q := range []float64{0.5, 0.75, 0.95, 0.99} {
		minimo, maximo, ok := serie.Quantil(q)
		if !ok {
			t.Errorf("quantile %v nao calculado", q)
			continue
		}
		if !declarados[minimo] || !declarados[maximo] {
			t.Errorf("quantile %v respondeu [%v, %v) com um limite que o histograma nao declara",
				q, minimo, maximo)
		}
	}
}

func TestFatiaDevolveUmaFaixaPorQuantilNaOrdem(t *testing.T) {
	serie := Buscar(textoDeExemplo, "wager_operacao_duracao_ms", map[string]string{"estado": "PROCESSED"})
	if serie == nil {
		t.Fatal("serie nao encontrada")
	}

	faixas, ok := serie.Fatia([]float64{0.5, 0.95, 0.99})
	if !ok {
		t.Fatal("Fatia devolveu false para quantis que a serie consegue responder")
	}
	if len(faixas) != 3 {
		t.Fatalf("devolveu %d faixas, esperadas 3", len(faixas))
	}

	// As faixas tem de crescer com o quantile. Um p95 abaixo do p50 indicaria que o
	// acumulado saiu errado -- e o bug que acontece quando os buckets nao sao ordenados.
	for i := 1; i < len(faixas); i++ {
		if faixas[i][0] < faixas[i-1][0] {
			t.Errorf("faixa %d comecou em %v, antes da faixa %d em %v",
				i, faixas[i][0], i-1, faixas[i-1][0])
		}
	}
}

func TestBucketForaDeOrdemNAoDesalteraOAcumulado(t *testing.T) {
	// O texto de exposicao nao promete ordem de bucket. Se `+Inf` vier antes dos
	// limites menores, um acumulado feito na ordem da leitura fecharia em `+Inf` na
	// primeira iteracao e todo quantile cairia no bucket errado.
	desordenado := `
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="+Inf"} 100
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="250"} 97
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="50"} 60
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="5"} 10
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="100"} 90
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="10"} 25
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="500"} 99
wager_operacao_duracao_ms_sum{estado="PROCESSED"} 950
wager_operacao_duracao_ms_count{estado="PROCESSED"} 100
`

	serie := Buscar(desordenado, "wager_operacao_duracao_ms", map[string]string{"estado": "PROCESSED"})
	if serie == nil {
		t.Fatal("serie nao encontrada no texto desordenado")
	}

	minimo, maximo, ok := serie.Quantil(0.95)
	if !ok {
		t.Fatal("p95 nao calculado")
	}
	if minimo != 100 || maximo != 250 {
		t.Errorf("p95 em [%v, %v), esperado [100, 250): os buckets fora de ordem corromperam a busca",
			minimo, maximo)
	}
}

func TestRotulosExatosRecusamSerieComRotuloExtra(t *testing.T) {
	// Correspondencia parcial seria o comportamento certo para um buscador de metricas e
	// seria perigoso aqui: somaria series diferentes e produziria um percentil de uma
	// mistura que ninguem pediu.
	serie := Buscar(textoDeExemplo, "wager_operacao_duracao_ms", map[string]string{"estado": "PROCESSED"})
	if serie == nil {
		t.Fatal("serie PROCESSED nao encontrada")
	}

	if serie.Rotulos["estado"] != "PROCESSED" {
		t.Errorf("rotulo estado %q, esperado PROCESSED", serie.Rotulos["estado"])
	}
	if _, temLimite := serie.Rotulos["le"]; temLimite {
		t.Error("o rotulo `le` vazou para os rotulos da serie: ele so existe nos buckets")
	}
}

func TestSerieAusenteDevolveNilET_naoZeros(t *testing.T) {
	// Nil e nao uma serie zerada: quem chama precisa poder dizer "essa metrica nao foi
	// registrada" em vez de imprimir zeros que parecem medicao.
	if serie := Buscar(textoDeExemplo, "wager_operacao_duracao_ms", map[string]string{"estado": "PENDING"}); serie != nil {
		t.Error("devolveu serie para um estado que nao existe no texto")
	}
	if serie := Buscar(textoDeExemplo, "metrica_que_nao_existe", nil); serie != nil {
		t.Error("devolveu serie para um nome que nao existe no texto")
	}
}

func TestQuantilEmSerieSemObservacaoNaoFingeQueSabe(t *testing.T) {
	// Uma serie declarada e nunca observada e um estado real: nobody operou ainda. O que
	// ela nao pode fazer e devolver um quantile inventado.
	vazio := `
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="5"} 0
wager_operacao_duracao_ms_bucket{estado="PROCESSED",le="+Inf"} 0
wager_operacao_duracao_ms_sum{estado="PROCESSED"} 0
wager_operacao_duracao_ms_count{estado="PROCESSED"} 0
`

	serie := Buscar(vazio, "wager_operacao_duracao_ms", map[string]string{"estado": "PROCESSED"})
	if serie == nil {
		t.Fatal("a serie declarada mas vazia deveria ser encontrada")
	}
	if _, _, ok := serie.Quantil(0.95); ok {
		t.Error("devolveu p95 para uma serie sem nenhuma observacao")
	}
	if _, ok := serie.Fatia([]float64{0.5, 0.95}); ok {
		t.Error("Fatia devolveu faixas para uma serie sem nenhuma observacao")
	}
	if serie.Media() != 0 {
		t.Errorf("media %v de uma serie sem observacao, esperada zero", serie.Media())
	}
}

func TestContadorSimplesNaoEConfundidoComHistograma(t *testing.T) {
	// `wager_operacoes_total` esta no mesmo texto e o nome contem a palavra do
	// histograma sem ser um. Um `Buscar` que casasse por substring transformaria um
	// contador em histograma com um bucketso.
	serie := Buscar(textoDeExemplo, "wager_operacoes_total", map[string]string{"estado": "PROCESSED"})
	if serie != nil {
		t.Fatal("um contador simples foi interpretado como histograma")
	}
}

func TestRotuloComVirgulaNaoQuebraOParser(t *testing.T) {
	// Separar rotulos por virgula ingenuamente quebraria o valor no meio, e o nome da
	// serie sairia errado em silencio -- sem erro, sem panic, so um numero errado.
	texto := `
wager_operacao_duracao_ms_bucket{estado="PROCESSED",origem="fila,outbox",le="10"} 3
wager_operacao_duracao_ms_bucket{estado="PROCESSED",origem="fila,outbox",le="+Inf"} 4
wager_operacao_duracao_ms_sum{estado="PROCESSED",origem="fila,outbox"} 25
wager_operacao_duracao_ms_count{estado="PROCESSED",origem="fila,outbox"} 4
`

	serie := Buscar(texto, "wager_operacao_duracao_ms", map[string]string{
		"estado": "PROCESSED",
		"origem": "fila,outbox",
	})
	if serie == nil {
		t.Fatal("serie com virgula no valor do rotulo nao foi encontrada")
	}
	if serie.Contagem != 4 {
		t.Errorf("contagem %v, esperada 4", serie.Contagem)
	}
	if serie.Rotulos["origem"] != "fila,outbox" {
		t.Errorf("rotulo origem %q, esperado fila,outbox", serie.Rotulos["origem"])
	}
}

func TestInterpretarIgnoraComentariosELinhasVazias(t *testing.T) {
	// Um `# HELP` sem valor nao pode virar serie com valor zero. Se virar, um histograma
	// "medido" pode aparecer a partir so do comentario que o descreve.
	linhasParaChecar := []string{
		"# HELP wager_operacao_duracao_ms Duracao",
		"# TYPE wager_operacao_duracao_ms histogram",
		"",
		"   ",
	}

	for _, linha := range linhasParaChecar {
		rotulos, _, _ := interpretar(linha)
		if rotulos != nil {
			t.Errorf("a linha %q foi interpretada como serie", linha)
		}
	}
}

func TestContadorSemRotuloEUmaSerieValida(t *testing.T) {
	// `wager_outbox_atraso_segundos` e um gauge sem rotulo nenhum. O parser precisa
	// tratar "sem chaves" como "rotulos vazios", e nao como linha malformada.
	rotulos, nome, valor := interpretar(`wager_outbox_atraso_segundos 4.25`)
	if nome != "wager_outbox_atraso_segundos" {
		t.Errorf("nome %q, esperado wager_outbox_atraso_segundos", nome)
	}
	if valor != 4.25 {
		t.Errorf("valor %v, esperado 4.25", valor)
	}
	if len(rotulos) != 0 {
		t.Errorf("rotulos %v, esperado nenhum", rotulos)
	}
}

func TestTextoVazioNaoQuebra(t *testing.T) {
	if serie := Buscar("", "qualquer", nil); serie != nil {
		t.Error("texto vazio produziu serie")
	}
	if serie := Buscar(strings.Repeat("\n", 10), "qualquer", nil); serie != nil {
		t.Error("texto so com quebras de linha produziu serie")
	}
}
