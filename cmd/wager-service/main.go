// Command wager-service sobe o servico de processamento de apostas.
//
// O entrypoint nao monta nada: ele chama app.Run e traduz o resultado em codigo
// de saida. Toda a montagem e a validacao estao em internal/runtime/app, que e
// testavel sem subir processo.
package main

import (
	"fmt"
	"os"

	"github.com/LuisMassuchini/backend-challenge-go/internal/runtime/app"
)

func main() {
	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "wager-service: %v\n", err)
		os.Exit(1)
	}
}
