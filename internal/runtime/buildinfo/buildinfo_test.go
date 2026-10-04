package buildinfo

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// O modulo declarado em go.mod e o que o binario em execucao pertence. Se os
// dois divergirem, o binario sai com identificacao de outro projeto, e isso
// aparece em log e em resposta de health sem ninguem notar.
func TestModuleCoincideComOModuloDeclaradoEmGoMod(t *testing.T) {
	const caminho = "../../../go.mod"

	conteudo, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("leitura de %s: %v", caminho, err)
	}

	const prefixo = "module "

	for _, linha := range strings.Split(string(conteudo), "\n") {
		if strings.HasPrefix(linha, prefixo) {
			declarado := strings.TrimSpace(strings.TrimPrefix(linha, prefixo))
			if declarado != Module {
				t.Fatalf("modulo em go.mod e %q, mas buildinfo.Module e %q", declarado, Module)
			}
			return
		}
	}

	t.Fatalf("nenhuma linha %q em %s", prefixo, caminho)
}

func TestCurrentReportaOVersaoDoToolchainEmExecucao(t *testing.T) {
	obtida := Current()

	if obtida.GoVersion != runtime.Version() {
		t.Fatalf("GoVersion e %q, mas o toolchain em execucao e %q", obtida.GoVersion, runtime.Version())
	}
}

// Sem -ldflags o binario nao tem versao de release. O valor de desenvolvimento
// precisa ser explicito, para que log e health check nunca afirmem uma versao
// que nao existe.
func TestCurrentUsaVersaoDeDesenvolvimentoPorPadrao(t *testing.T) {
	obtida := Current()

	if obtida.Version != DevelopmentVersion {
		t.Fatalf("Version e %q, mas sem -ldflags o esperado e %q", obtida.Version, DevelopmentVersion)
	}
}

func TestCurrentPreencheTodosOsCampos(t *testing.T) {
	obtida := Current()

	if obtida.Module == "" {
		t.Error("Module vazio")
	}
	if obtida.GoVersion == "" {
		t.Error("GoVersion vazio")
	}
}
