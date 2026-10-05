// Package pg e a borda de persistencia: pool, migrations e unidade transacional.
//
// Este pacote e onde o SQL aparece. Nenhum outro pacote do sistema escreve
// consulta, e o dominio nao importa nada daqui: a dependencia aponta sempre do
// adaptador para o dominio, nunca o contrario.
//
// As migrations sao arquivos .sql embedded no binario. Embed e a escolha porque
// uma migration que precisa estar no disco para ser aplicada falha justo no
// ambiente em que mais importa -- o container, que nao tem o codigo fonte -- e
// porque um binario que carrega a propria version de schema nao depende de o
// arquivo traveling junto.
package pg

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// migrationsFS e o conjunto de migrations compilado no binario.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// gooseTable e o nome da tabela de controle de versao.
//
// Nome explicito porque o padrao do goose e "goose_db_version", que numa base com
// varias tabelas de aplicacao fica invisivel no painel do PostgreSQL.
const gooseTable = "wager_schema_version"

// Migrator aplica e reverte as migrations.
//
// Existe como tipo e nao como funcao solta porque quem migra precisa poder
// consultar o estado sem alterar nada, e um par de funcoes de pacote nao deixa
// isso explicito.
type Migrator struct {
	// db e a conexao usada apenas para DDL.
	db *sql.DB
	// dir e o sistema de arquivos com os arquivos .sql.
	dir fs.FS
}

// NovoMigrator constroi o migrador sobre a conexao informada.
func NovoMigrator(db *sql.DB) *Migrator {
	return &Migrator{db: db, dir: migrationsFS}
}

// Up aplica as migrations pendentes, na ordem.
//
// Idempotente por construcao: o goose registra o que ja aplicou e nao reaplica.
// Rodar duas vezes seguidas nao muda nada, que e o que permite o start da
// aplicacao chamar Up sem coordenar com o start de outra instancia.
func (m *Migrator) Up(ctx context.Context) error {
	if err := m.configurar(); err != nil {
		return fmt.Errorf("configuracao do goose: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("dialeto do goose: %w", err)
	}
	if err := goose.UpContext(ctx, m.db, "migrations"); err != nil {
		return fmt.Errorf("aplicacao de migrations: %w", err)
	}
	return nil
}

// Down reverte a ultima migration aplicada.
//
// E "a ultima", e nao "todas", porque reverter tudo em um ambiente que ja tem
// dado e o caminho mais rapido para perder dado sem querer. O teste de integracao
// que precisa do schema vazio cria um banco proprio.
func (m *Migrator) Down(ctx context.Context) error {
	if err := m.configurar(); err != nil {
		return fmt.Errorf("configuracao do goose: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("dialeto do goose: %w", err)
	}
	if err := goose.DownContext(ctx, m.db, "migrations"); err != nil {
		return fmt.Errorf("reversao de migration: %w", err)
	}
	return nil
}

// Status imprime o estado de cada migration.
func (m *Migrator) Status(ctx context.Context) error {
	if err := m.configurar(); err != nil {
		return fmt.Errorf("configuracao do goose: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("dialeto do goose: %w", err)
	}
	if err := goose.StatusContext(ctx, m.db, "migrations"); err != nil {
		return fmt.Errorf("status de migrations: %w", err)
	}
	return nil
}

// configurar registra o sistema de arquivos e a tabela de controle.
//
// Registrar o FS e o passo que torna o embedded utilizavel: sem ele o goose le o
// diretorio do processo, que no container nao existe.
func (m *Migrator) configurar() error {
	goose.SetBaseFS(m.dir)
	goose.SetTableName(gooseTable)
	return nil
}
