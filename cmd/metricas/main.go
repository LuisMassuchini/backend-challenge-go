// Command metricas le o endpoint /metrics do wager-service e imprime as faixas de
// quantil que o relatorio de carga precisa.
//
// Existe para que o p50, p95 e p99 do relatorio venham da latencia que o SERVIDOR mediu
// -- `wager_operacao_duracao_ms`, do inicio da operacao ate o commit -- e nao da
// latencia que o k6 mediu no cliente. As duas respondem perguntas diferentes, e a que
// o enunciado pede e a do servidor: e ela que inclui a espera pelo lock da carteira.
//
// Uso:
//
//	metricas http://localhost:8080/metrics
//	metricas http://localhost:8080/metrics wager_operacao_duracao_ms estado=PROCESSED
package main

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/promtexto"
)

// seriePedida e uma serie que o relatorio vai imprimir.
type seriePedida struct {
	nome    string
	rotulos map[string]string
}

// seriesPadrao e o que o relatorio de carga mostra quando nenhum nome e informado.
//
// `REJECTED` entra junto de `PROCESSED` de proposito: e o contraexemplo que torna a
// latencia de `PROCESSED` interpretavel. Uma recusa de saldo nao espera lock nenhum, e
// por isso responde mais rapido -- ver as duas juntas e o que separa "o sistema e rapido"
// de "o sistema e rapido no que nao disputa dinheiro".
var seriesPadrao = []seriePedida{
	{"wager_operacao_duracao_ms", map[string]string{"estado": "PROCESSED"}},
	{"wager_operacao_duracao_ms", map[string]string{"estado": "REJECTED"}},
	{"wager_requisicao_duracao_ms", map[string]string{"metodo": "POST"}},
}

// quantisRelatorio e o conjunto que o enunciado nomeia.
//
// Sao os tres de Always Correct (1999), e nao uma escolha: sao o suficiente para
// descrever uma cauda sem poluir o relatorio com pontos que ninguem vai olhar.
var quantisRelatorio = []struct {
	nome  string
	valor float64
}{
	{"p50", 0.50},
	{"p95", 0.95},
	{"p99", 0.99},
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "metricas: %v\n", err)
		os.Exit(1)
	}
}

func run(argumentos []string) error {
	if len(argumentos) == 0 {
		return fmt.Errorf("uso: metricas <url> [nome-da-metrica rotulo=valor ...]")
	}

	endereco := argumentos[0]
	texto, err := buscar(endereco)
	if err != nil {
		return err
	}

	series := seriesPadrao
	if len(argumentos) > 1 {
		series, err = seriesDoArgumento(argumentos[1:])
		if err != nil {
			return err
		}
	}

	imprimirCabecalho(series)

	faltou := false
	for _, pedida := range series {
		encontrada := promtexto.Buscar(texto, pedida.nome, pedida.rotulos)
		if encontrada == nil {
			fmt.Printf("%-34s %-28s %s\n", pedida.nome, rotulosComoTexto(pedida.rotulos),
				"serie ausente: a metrica nao foi registrada nesta execucao")
			faltou = true
			continue
		}
		imprimirSerie(encontrada)
	}

	if !faltou {
		imprimirContadores(texto)
	}
	return nil
}

// buscar le o endpoint de metricas.
//
// O timeout e curto e nomeado porque a leitura e local: trinta segundos a mais que o
// necessario aqui significariam que um `/metrics` travado travaria o relatorio por
// trinta segundos sem dizer nada.
func buscar(endereco string) (string, error) {
	cliente := &http.Client{Timeout: 30 * time.Second}

	resposta, err := cliente.Get(endereco)
	if err != nil {
		return "", fmt.Errorf("buscando %s: %w", endereco, err)
	}
	defer resposta.Body.Close()

	if resposta.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s respondeu %d", endereco, resposta.StatusCode)
	}

	corpo, err := io.ReadAll(resposta.Body)
	if err != nil {
		return "", fmt.Errorf("lendo %s: %w", endereco, err)
	}
	return string(corpo), nil
}

// seriesDoArgumento monta a lista de series a partir do que veio na linha de comando.
//
// O formato e `nome-da-metrica rotulo=valor rotulo=valor`, com a metrica em primeiro.
// Exigir um separador explicito -- `--` ou `:` -- evitaria a ambiguidade entre a metrica
// e o rotulo, mas o custo e uma convencao a mais para lembrar e um erro a mais para
// escrever. O que o formato tem de ambiguo e o que o `Cut` resolve: um rotulo sempre
// contem `=`, e uma metrica nunca contem.
func seriesDoArgumento(argumentos []string) ([]seriePedida, error) {
	nome := argumentos[0]
	rotulos := map[string]string{}

	for _, argumento := range argumentos[1:] {
		chave, valor, achou := strings.Cut(argumento, "=")
		if !achou {
			return nil, fmt.Errorf("rotulo %q sem valor: use chave=valor", argumento)
		}
		rotulos[chave] = valor
	}

	return []seriePedida{{nome, rotulos}}, nil
}

func imprimirCabecalho(series []seriePedida) {
	fmt.Printf("%-34s %-30s %8s %8s %s\n", "metrica", "rotulos", "media", "n", "faixas de quantil (ms)")
	fmt.Println(strings.Repeat("-", 118))
}

func imprimirSerie(serie *promtexto.Serie) {
	faixas, ok := serie.Fatia([]float64{0.5, 0.95, 0.99})

	descricao := "sem observacao nenhuma"
	if ok {
		descricao = ""
		for i, faixa := range faixas {
			if i > 0 {
				descricao += "   "
			}
			descricao += fmt.Sprintf("%s %s", quantisRelatorio[i].nome, faixaComoTexto(faixa))
		}
	}

	fmt.Printf("%-34s %-30s %8.2f %8d %s\n",
		serie.Nome, rotulosComoTexto(serie.Rotulos), serie.Media(), int(serie.Contagem), descricao)
}

// imprimirContadores mostra os contadores que o relatorio de carga le.
//
// Sao os que respondem "o que aconteceu" em vez de "quanto tempo levou": quantas
// operacoes por desfecho, quantas duplicatas reconhecidas, quantos conflitos de chave,
// quantas retentativas e quantas mensagens foram para o cartao morto.
//
// Sem esses numeros, um relatorio com "p95 = [100, 250)ms" e zero erros deixa aberta a
// pergunta que importa: o sistema fez o trabalho, ou recusou o trabalho e por isso
// respondeu rapido? Uma recusa de saldo e uma resposta 4xx, entao o k6 nao a conta como
// erro -- e e por isso que o desfecho tem de aparecer.
func imprimirContadores(texto string) {
	fmt.Println()
	fmt.Println("contadores de desfecho")
	fmt.Println(strings.Repeat("-", 118))

	for _, nome := range []string{
		"wager_operacoes_total",
		"wager_operacoes_duplicadas_total",
		"wager_conflitos_chave_total",
		"wager_conflitos_lock_total",
		"wager_retentativas_total",
		"wager_mensagens_fila_morta_total",
		"wager_reconciliacoes_divergentes_total",
	} {
		for linha := range contador(texto, nome) {
			fmt.Println(linha)
		}
	}
}

// contador devolve as linhas de um contador, ja formatadas.
//
// A leitura e feita linha a linha em vez de por parser de novo: o texto de exposicao ja
// esta no formato que o relatorio quer mostrar, e reescrever "nomedametrica{rotulo=x} N"
// como "nomedametrica rotulo=x = N" seria trabalho de formatacao sem informacao nova.
func contador(texto, nome string) func(func(string) bool) {
	return func(emite func(string) bool) {
		for _, linha := range strings.Split(texto, "\n") {
			if !strings.HasPrefix(linha, nome) {
				continue
			}
			corpo := linha
			if abre := strings.IndexByte(corpo, '{'); abre >= 0 {
				fecha := strings.IndexByte(corpo, '}')
				if fecha < abre {
					continue
				}
				corpo = nome + " " + rotulosComoTextoStrings(corpo[abre+1:fecha]) +
					" = " + strings.TrimSpace(corpo[fecha+1:])
			} else {
				partes := strings.Fields(corpo)
				if len(partes) != 2 {
					continue
				}
				corpo = nome + " = " + partes[1]
			}
			if !emite(corpo) {
				return
			}
		}
	}
}

// rotulosComoTextoStrings reformata um bloco de rotulos do texto de exposicao para a
// notacao `chave=valor`, que e a que o relatorio usa.
func rotulosComoTextoStrings(bruto string) string {
	if bruto == "" {
		return ""
	}

	partes := strings.Split(bruto, ",")
	limpas := make([]string, 0, len(partes))
	for _, parte := range partes {
		limpas = append(limpas, strings.ReplaceAll(parte, `"`, ""))
	}
	sort.Strings(limpas)
	return strings.Join(limpas, ",")
}

// faixaComoTexto escreve um intervalo de quantil.
//
// `+Inf` vira `acima de`, e nao `+Inf`: o ultimo bucket nao tem limite superior, e
// escrever `+Inf` na saida de um relatorio e deixar para o leitor a interpretacao. A
// leitura honesta de "acima de 500ms" e mais util que "500 a infinito".
func faixaComoTexto(faixa [2]float64) string {
	limite := func(valor float64) string {
		if math.IsInf(valor, 1) {
			return "acima de"
		}
		return strconv.FormatFloat(valor, 'f', -1, 64) + "ms"
	}

	inferior := limite(faixa[0])
	if faixa[0] == 0 {
		inferior = "abaixo de"
	}
	return fmt.Sprintf("[%s, %s)", inferior, limite(faixa[1]))
}

func rotulosComoTexto(rotulos map[string]string) string {
	if len(rotulos) == 0 {
		return "-"
	}

	chaves := make([]string, 0, len(rotulos))
	for chave := range rotulos {
		chaves = append(chaves, chave)
	}
	sort.Strings(chaves)

	partes := make([]string, 0, len(chaves))
	for _, chave := range chaves {
		partes = append(partes, chave+"="+rotulos[chave])
	}
	return "{" + strings.Join(partes, ",") + "}"
}
