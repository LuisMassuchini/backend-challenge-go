package promtexto

import (
	"strconv"
	"strings"
)

// linhas devolve as linhas do texto que podem conter uma serie.
//
// O comentario de HELP e o de TYPE sao descartados aqui, e nao em `interpretar`: eles nao
// tem valor, e um numero de serie sem `le` nem valor nao serve para nada. Descartar
// cedo deixa `interpretar` com um unico trabalho, que e ler a tripla nome-rotulo-valor.
func linhas(texto string) []string {
	return strings.Split(texto, "\n")
}

// interpretar le uma linha de exposicao e devolve nome, rotulos e valor.
//
// Devolve rotulos nil quando a linha nao e uma serie. Isso acontece para comentario, para
// linha em branco e para linha sem espaco entre a parte nomeada e o valor -- e o valor
// nil e o que permite `Buscar` pular a linha com um unico `if`, sem repetir a checagem
// em tres lugares.
func interpretar(linha string) (rotulos map[string]string, nome string, valor float64) {
	linha = strings.TrimSpace(linha)
	if linha == "" || strings.HasPrefix(linha, "#") {
		return nil, "", 0
	}

	// O separador e o PRIMEIRO espaco depois da parte nomeada. Nao pode ser o ultimo nem
	// o unico: `nome valor` tem um, e um nome com espaco dentro das aspas do rotulo
	// teria varios. O ultimo espaco resolveria, mas quebraria com `le="+Inf"` -- o
	// espaco antes do valor seria depois do `+Inf` e nao do nome.
	espaco := strings.IndexByte(linha, ' ')
	if espaco < 0 {
		return nil, "", 0
	}

	parte := linha[:espaco]
	resto := strings.TrimSpace(linha[espaco+1:])

	valor, err := parseValor(resto)
	if err != nil {
		return nil, "", 0
	}

	chave := strings.IndexByte(parte, '{')
	if chave < 0 {
		return nil, parte, valor
	}

	nome = parte[:chave]
	fechamento := strings.LastIndexByte(parte, '}')
	if fechamento < chave {
		return nil, "", 0
	}

	rotulos = lerRotulos(parte[chave+1 : fechamento])
	return rotulos, nome, valor
}

// parseValor le o valor numerico de uma linha.
//
// O valor pode ser `NaN`, `Inf` ou `-Inf`, que e o que o Prometheus usa para o bucket
// `+Inf`. `NaN` nao e numero e nao interessa aqui: uma contagem de bucket nunca e `NaN`,
// e aceitar transformaria um texto corrompido em media valida.
func parseValor(bruto string) (float64, error) {
	bruto = strings.TrimSpace(bruto)

	// `+Inf` e `Inf` sao as duas grafias do mesmo valor no texto de exposicao, e
	// `strconv.ParseFloat` entende a segunda e nao a primeira.
	if bruto == "+Inf" || bruto == "Inf" {
		return 0, nil
	}

	valor, err := strconv.ParseFloat(bruto, 64)
	if err != nil {
		return 0, err
	}
	return valor, nil
}

// lerRotulos separa um bloco de rotulos.
//
// O bloco tem a forma `a="1",b="2"`. A separacao e por virgula FORA de aspas, e nao por
// `strings.Split`: um valor de rotulo pode conter virgula -- o `eventId` de um envelope
// tem UUID, mas um nome de recurso da AWS tem virgula -- e dividir ingenuamente
// quebraria o rotulo no meio e produziria um nome de serie errado em silencio.
func lerRotulos(bruto string) map[string]string {
	rotulos := make(map[string]string)

	chave, valor, dentro := "", "", false
	escapado := false

	fechar := func() {
		if chave != "" {
			rotulos[chave] = valor
		}
		chave, valor, dentro = "", "", false
	}

	for _, caractere := range bruto {
		switch {
		case escapado:
			valor += string(caractere)
			escapado = false

		case caractere == '\\' && dentro:
			escapado = true

		case caractere == '"':
			dentro = !dentro

		case caractere == '=' && !dentro:
			chave = strings.TrimSpace(chave)

		case caractere == ',' && !dentro:
			fechar()

		default:
			if dentro {
				valor += string(caractere)
			} else {
				chave += string(caractere)
			}
		}
	}
	fechar()

	return rotulos
}
