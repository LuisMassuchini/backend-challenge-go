// Package dominio nao possui codigo de producao: os agregados estao nos
// subdiretorios.
//
// Este arquivo existe para tornar verificavel uma promessa que aparecia so como frase
// em `docs/requisitos.md` -- que o dominio nao depende de Fx, HTTP, SQS nem de
// biblioteca de persistencia.
//
// **Uma promessa escrita em documento nao e uma verificacao.** Ela se mantem ate
// alguem adicionar o `import` e nao notar que a frase continua ali, e o documento
// continua afirmando uma verdade que o codigo ja contradiz. Um teste falha; uma frase
// nao.
package dominio

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Infraestrutura que o dominio nao pode depender.
//
// A comparacao e por caminho de import e nao por nome de pacote, porque e o caminho que
// decide a dependencia real: `go.uber.org/fx` e `go.uber.org/fx/fxevent` sao o mesmo
// modulo com caminhos diferentes, e uma comparacao por sufixo pegaria um e nao o outro.
var proibidas = map[string]string{
	"go.uber.org/fx":               "composicao de aplicacao",
	"net/http":                     "transporte HTTP",
	"database/sql":                 "persistencia relacional",
	"github.com/jackc/pgx":         "driver do PostgreSQL",
	"github.com/aws/aws-sdk-go-v2": "SDK da AWS",
	"github.com/gorilla/mux":       "roteador HTTP de terceiro",
}

// Pacotes de aplicacao que o dominio nao pode conhecer.
//
// A direcao correta, e verificada por este teste, e `dominio <- app <- httpapi`: a
// camada de fora conhece o agregado, e o agregado nao conhece a camada de fora. O que o
// teste proibe e a seta invertida, `dominio -> app`, porque um agregado que sabe de onde
// foi chamado deixa de ser reaproveitavel.
var desconhecidos = []string{
	"/internal/app",
	"/internal/httpapi",
	"/internal/consumidor",
	"/internal/pg",
	"/internal/sqs",
	"/internal/obs",
	"/internal/auth",
	"/internal/outbox",
	"/internal/pendencias",
	"/internal/fingerprint",
	"/internal/runtime",
}

// permitidasDeTerceiros e a lista de dependencias externas que o dominio aceita.
//
// `github.com/google/uuid` e a unica, e o motivo esta em `wallet/identificador.go`: a
// representacao canonica de um identificador e a do UUID, e reimplementar a validacao
// criaria uma segunda regra que divergiria da primeira sem que ninguem percebesse. Um
// UUID gerado dentro do dominio seria um UUID que o `uuid.Parse` do `google/uuid`
// aceita e o de outra biblioteca talvez nao.
var permitidasDeTerceiros = map[string]bool{
	"github.com/google/uuid": true,
}

func TestDominioNaoImportaInfraestrutura(t *testing.T) {
	pacotes := pacotesDoDominio(t)
	if len(pacotes) == 0 {
		t.Fatal("nenhum pacote de dominio encontrado")
	}

	set := token.NewFileSet()

	for _, pacote := range pacotes {
		t.Run(pacote, func(t *testing.T) {
			forEach(t, set, filepath.Join(pacote), func(_ string, caminho string) {
				if motivo, proibido := proibidas[caminho]; proibido {
					t.Errorf("%q importa %q: o dominio nao pode depender de %s", caminho, caminho, motivo)
					return
				}

				for _, desconhecido := range desconhecidos {
					if strings.Contains(caminho, desconhecido) {
						t.Errorf("%q importa %q: a dependencia vai na direcao errada, o dominio nao pode conhecer casos de uso e adaptadores",
							caminho, caminho)
						return
					}
				}

				if deTerceiros(caminho) && !permitidasDeTerceiros[caminho] {
					t.Errorf("%q importa o pacote de terceiro %q, que nao esta na lista de permitidos do dominio",
						caminho, caminho)
				}
			})
		})
	}
}

// pacotesDoDominio devolve os subdiretorios que contem codigo de dominio.
func pacotesDoDominio(t *testing.T) []string {
	t.Helper()

	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("lendo o diretorio do dominio: %v", err)
	}

	pacotes := make([]string, 0, len(entradas))
	for _, entrada := range entradas {
		if entrada.IsDir() {
			pacotes = append(pacotes, entrada.Name())
		}
	}
	return pacotes
}

// forEach percorre os arquivos `.go` de um diretorio e chama a funcao para cada import.
//
// `os.ReadDir` e nao `filepath.Glob("*/")` porque o glob com barra final nao casa
// diretorio no Windows, e o teste passava em Linux e falhava aqui -- que e a forma pior de
// um teste depender de plataforma.
func forEach(t *testing.T, set *token.FileSet, diretorio string, aoImportar func(arquivo, caminho string)) {
	t.Helper()

	entradas, err := os.ReadDir(diretorio)
	if err != nil {
		t.Fatalf("lendo %s: %v", diretorio, err)
	}

	for _, entrada := range entradas {
		arquivo := filepath.Join(diretorio, entrada.Name())

		if entrada.IsDir() {
			forEach(t, set, arquivo, aoImportar)
			continue
		}

		if !strings.HasSuffix(entrada.Name(), ".go") || strings.HasSuffix(entrada.Name(), "_test.go") {
			// O arquivo de teste importa `testing` e `go/parser`, e verificar as
			// dependencias de um teste de dependencia seria verificar a ferramenta em
			// vez do codigo.
			continue
		}

		analisado, err := parser.ParseFile(set, arquivo, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("analisando %s: %v", arquivo, err)
		}

		for _, spec := range analisado.Imports {
			caminho, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("caminho de import invalido em %s: %v", arquivo, err)
			}
			aoImportar(arquivo, caminho)
		}
	}
}

// deTerceiros diz se o caminho pertence a um modulo de fora do projeto E da biblioteca
// padrao.
//
// Tres casos, e confundir qualquer um deles reprova o dominio inteiro:
//
//   - o proprio projeto: permitido, e sao os pacotes irmaos -- `money`, `wallet`,
//     `wagering`, `eventos` compoem o dominio entre si;
//   - a biblioteca padrao: `bytes`, `time`, `errors`. O criterio e a AUSENCIA de ponto no
//     primeiro segmento;
//   - o resto: `github.com/google/uuid`, que e o unico permitido e esta na lista.
func deTerceiros(caminho string) bool {
	if strings.HasPrefix(caminho, prefixoDoProjeto) {
		return false
	}

	primeiro := caminho
	if barra := strings.IndexByte(caminho, '/'); barra >= 0 {
		primeiro = caminho[:barra]
	}
	return strings.Contains(primeiro, ".")
}

// prefixoDoProjeto e o caminho de import do proprio modulo.
const prefixoDoProjeto = "github.com/LuisMassuchini/backend-challenge-go/"
