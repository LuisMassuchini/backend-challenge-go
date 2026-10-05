// Command migrate aplica e reverte as migrations do wager-service.
//
// Existe como comando separado e nao como parte do start da aplicacao por dois
// motivos. Primeiro,quem aplica migrate tem direito de DDL e quem roda o servico
// nao: misturar as duas coisas obriga a aplicacao a ter o superusuario do banco em
// tempo de execucao. Segundo, o start da aplicacao precisa continuar silencioso
// em relacao a schema: em tres instancias subindo ao mesmo tempo, tres migracoes
// correndo ao mesmo tempo e uma corrida que o goose resolve mas que e melhor nao
// tentar.
//
// Uso:
//
//	migrate up      aplica as migrations pendentes
//	migrate down    reverte a ultima migration aplicada
//	migrate status  mostra o estado de cada migration
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/LuisMassuchini/backend-challenge-go/internal/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("uso: migrate up | down | status")
	}

	// Migracao nao espera indefinidamente: um DDL com bloqueio esperando um lock
	// fica em silencio ate o operador desistir, e um operador nao tem como ver
	// um processo parado sem timeout.
	ctx, cancelar := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelar()

	sinal := make(chan os.Signal, 1)
	signal.Notify(sinal, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sinal
		cancelar()
	}()

	dsn := os.Getenv("WAGER_POSTGRES_DSN")
	if dsn == "" {
		return fmt.Errorf("WAGER_POSTGRES_DSN ausente")
	}

	conexao, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("conexao: %w", err)
	}
	defer conexao.Close()

	if err := conexao.PingContext(ctx); err != nil {
		return fmt.Errorf("conexao com o postgres: %w", err)
	}

	migrador := db.NovoMigrator(conexao)

	switch os.Args[1] {
	case "up":
		return migrador.Up(ctx)
	case "down":
		return migrador.Down(ctx)
	case "status":
		return migrador.Status(ctx)
	}

	return fmt.Errorf("comando desconhecido %q: use up, down ou status", os.Args[1])
}
