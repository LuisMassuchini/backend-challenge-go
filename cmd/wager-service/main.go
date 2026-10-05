// Command wager-service sobe o servico de processamento de apostas.
//
// O entrypoint nao monta nada: ele chama app.Run e traduz o resultado em codigo
// de saida. Toda a montagem e a validacao estao em internal/runtime/app, que e
// testavel sem subir processo.
//
// O binario tambem e a sonda de liveness do Compose:
//
//	wager-service healthcheck
//
// Uma imagem distroless nao tem curl, nem wget, nem shell, e um probe de Compose
// com `CMD-SHELL` falharia no primeiro start. Fazer o proprio binario consultar o
// proprio `/health/live` evita uma imagem extra e um script que pode divergir.
package main

import (
	"fmt"
	"os"

	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/healthcheck"
)

func main() {
	// `wager-service healthcheck` e a sonda de liveness, e ela nao sobe a aplicacao.
	//
	// O Compose precisa de um comando que responda "o processo esta vivo", e a
	// imagem final e distroless: sem curl, sem wget, sem shell. Um script de probe
	// seria um arquivo a mais na imagem e mais uma peca para divergir. O proprio
	// binario fazendo a propria consulta e uma dependencia a menos.
	//
	// O subcomando e verificado antes de `app.Run` justamente porque `app.Run` sobe o
	// servidor inteiro: se o reconhecimento viesse depois, a sonda estaria recusando
	// conexao enquanto sobe um grafo de dependencias que ela nao precisa.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck.DaExecucao(os.Args[2:], os.Stderr))
	}

	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "wager-service: %v\n", err)
		os.Exit(1)
	}
}
