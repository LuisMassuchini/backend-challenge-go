// Package promtexto le o formato de exposicao de texto do Prometheus e extrai
// faixas de quantil de histogramas.
//
// Existe porque o enunciado pede p50, p95 e p99 da latencia de processamento, e a
// latencia que importa aqui e a do SERVIDOR: `wager_operacao_duracao_ms` mede do inicio
// da operacao ate o commit, e inclui a disputa pelo lock da carteira. A latencia que o
// k6 reporta e do lado do cliente e nao inclui o tempo que a requisicao passou esperando
// em uma transacao SQL.
//
// Quantil em histograma e interpolacao dentro do bucket onde a observacao cai, e a
// observacao individual nunca existiu: o histograma guarda contagem por bucket, nao as
// amostras. Um numero com dois decimais aqui seria um numero inventado com precisao
// falsa, que e pior do que dizer que se trata de uma faixa.
package promtexto

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Sufixos que o Prometheus acrescenta ao nome de um histograma.
const (
	sufixoBucket = "_bucket"
	sufixoSoma   = "_sum"
	sufixoTotal  = "_count"
)

// Serie e um histograma lido de um texto de exposicao.
type Serie struct {
	// Nome e o nome do histograma, sem sufixo.
	Nome string

	// Rotulos sao os rotulos da serie, sem o `le`, que so existe nos buckets.
	Rotulos map[string]string

	// Contagem e quantas observacoes o histograma registrou.
	Contagem float64

	// Soma e a soma de todas as observacoes.
	Soma float64

	// buckets sao os limites declarados e o acumulado declarado em cada um, em ordem
	// crescente de limite.
	buckets []bucket
}

// bucket e um limite declarado e quantas observacoes caem **ate** ele.
//
// O campo e acumulado porque e assim que o Prometheus escreve: a linha
// `..._bucket{le="50"} 8` ja vale oito, e nao "mais oito". Somar os valores -- o erro
// mais obvio ao ler histograma -- contaria cada observacao tantas vezes quantos buckets
// ela atravessasse, e com dez observacoes e cinco limites o total fecharia em dezesseis.
type bucket struct {
	limite    float64
	acumulado float64
}

// Media devolve a media das observacoes.
//
// Zero quando a serie nao tem nenhuma observacao: quem chama precisa poder distinguir
// "media zero" de "sem medicao", e um zero silencioso parece uma medicao.
func (s *Serie) Media() float64 {
	if s.Contagem == 0 {
		return 0
	}
	return s.Soma / s.Contagem
}

// Quantil devolve a faixa em que o quantil `q` cai.
//
// Devolve o limite superior do bucket onde a observacao cai e o limite do bucket
// anterior. A resposta e uma FAIXA e nao um numero, e isso e o que os dados sustentam:
// entre dois limites o histograma nao sabe a distribuicao.
//
// A interpolacao linear dentro do bucket seria o caminho comum e seria uma mentira --
// devolveria "p95 = 512.3ms" para um bucket `[500, 1000)`, com uma precisao que nenhum
// dado produziu. Ver `docs/carga.md` para a discussao.
func (s *Serie) Quantil(q float64) (minimo, maximo float64, ok bool) {
	if s.Contagem <= 0 {
		return 0, 0, false
	}

	alvo := q * s.Contagem
	anterior := 0.0

	// Os valores dos buckets ja sao acumulados, entao nao ha soma a fazer: a busca e a
	// primeira limite cujo acumulado alcanca o alvo.
	for _, b := range s.buckets {
		if b.acumulado >= alvo {
			return anterior, b.limite, true
		}
		anterior = b.limite
	}

	return 0, 0, false
}

// Fatia devolve as faixas dos quantis pedidos, na ordem.
//
// Devolve false quando algum quantile nao pode ser calculado, para que o relatorio diga
// que faltou dado em vez de mostrar um zero como se fosse a resposta.
func (s *Serie) Fatia(quantis []float64) ([][2]float64, bool) {
	faixas := make([][2]float64, 0, len(quantis))

	for _, q := range quantis {
		minimo, maximo, ok := s.Quantil(q)
		if !ok {
			return nil, false
		}
		faixas = append(faixas, [2]float64{minimo, maximo})
	}
	return faixas, true
}

// Buscar localiza um histograma pelo nome e pelos rotulos exatos.
//
// Devolve nil quando a serie nao existe no texto, e nao uma serie zerada: quem chama
// precisa poder dizer "essa metrica nao foi registrada" em vez de imprimir zeros.
//
// A correspondencia de rotulos e EXATA. Pedir `{estado="PROCESSED"}` nao devolve a serie
// que tem tambem `via="http"`, e essa recusa e deliberada: correspondencia parcial seria
// o comportamento certo para um buscador de metricas e seria perigoso aqui, porque
// somaria series que nao sao a mesma e produziria um percentil de uma mistura que ninguem
// pediu.
func Buscar(texto, nome string, rotulos map[string]string) *Serie {
	contagens := make(map[float64]float64)
	soma := 0.0
	contagem := 0.0
	viu := false

	for _, linha := range linhas(texto) {
		rotulosLinha, nomeLinha, valor := interpretar(linha)
		if rotulosLinha == nil || !mesmosRotulos(rotulosLinha, rotulos) {
			continue
		}

		switch _, sufixo := decompor(nomeLinha, nome); sufixo {
		case sufixoBucket:
			limite, temLimite := rotulosLinha["le"]
			if !temLimite {
				continue
			}
			valorLimite, err := strconv.ParseFloat(limite, 64)
			if err != nil {
				// O bucket `+Inf` e o ultimo e precisa entrar: e onde cai tudo que
				// passou do maior limite declarado. `ParseFloat` entende `+Inf`.
				if limite != "+Inf" {
					continue
				}
				valorLimite = math.Inf(1)
			}
			contagens[valorLimite] = valor
			viu = true

		case sufixoSoma:
			soma = valor
			viu = true

		case sufixoTotal:
			contagem = valor
			viu = true
		}
	}

	if !viu {
		return nil
	}

	// O texto de exposicao nao garante ordem de bucket. Ordenar e o que permite a
	// busca do quantile caminhar uma vez so -- sem isso o `+Inf` poderia vir antes do
	// ultimo limite declarado, e como os valores JA sao acumulados, aceitar a ordem de
	// leitura faria o `+Inf` ser lido primeiro e responder por qualquer quantile.
	buckets := make([]bucket, 0, len(contagens))
	for limite, acumulado := range contagens {
		buckets = append(buckets, bucket{limite: limite, acumulado: acumulado})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].limite < buckets[j].limite })

	return &Serie{
		Nome:     nome,
		Rotulos:  semLimite(rotulos),
		Contagem: contagem,
		Soma:     soma,
		buckets:  buckets,
	}
}

// decompor separa o nome da serie em nome-base e sufixo.
//
// O sufixo devolvido e vazio quando a linha nao pertence ao histograma pedido. E o que
// faz `Buscar` ignorar contadores simples que existem no mesmo texto com nome parecido.
func decompor(nomeSerie, nomeBase string) (base, sufixo string) {
	for _, candidato := range []string{sufixoBucket, sufixoSoma, sufixoTotal} {
		if strings.HasSuffix(nomeSerie, candidato) &&
			strings.TrimSuffix(nomeSerie, candidato) == nomeBase {
			return nomeBase, candidato
		}
	}
	return nomeSerie, ""
}

// mesmosRotulos compara os rotulos da linha com os pedidos.
//
// A comparacao ignora o `le`, que existe so nos buckets e nao identifica a serie. Os
// rotulos de entrada precisam existir todos no pedido, e nenhum rotulo do pedido pode
// faltar na linha -- e o que torna a correspondencia exata em vez de parcial.
func mesmosRotulos(daLinha, pedidos map[string]string) bool {
	for chave, valor := range pedidos {
		daLinha, tem := daLinha[chave]
		if !tem || daLinha != valor {
			return false
		}
	}

	for chave := range daLinha {
		if chave == "le" {
			continue
		}
		if _, quer := pedidos[chave]; !quer {
			return false
		}
	}
	return true
}

// semLimite devolve os rotulos sem o `le`.
func semLimite(rotulos map[string]string) map[string]string {
	copia := make(map[string]string, len(rotulos))
	for chave, valor := range rotulos {
		if chave == "le" {
			continue
		}
		copia[chave] = valor
	}
	return copia
}
