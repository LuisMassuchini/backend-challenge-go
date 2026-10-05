# Verificacao e testes do wager-service.
#
# O container e o caminho oficial: a imagem e a mesma declarada no Dockerfile, de
# modo que o container e a unica fonte de verdade do toolchain e nenhum passo
# depende do que esta instalado na maquina de quem desenvolve.
#
# Os alvos sem sufixo usam o toolchain local. Eles existem para o ciclo TDD na
# maquina de desenvolvimento e nao substituem o gate oficial: quando o Docker
# estiver disponivel, o commit deve ser verificado com `verify-docker`.
#
# As receitas sao POSIX e dependem de um shell POSIX. No Windows, o GNU make
# procura `sh.exe`, que o Git for Windows instala em `usr/bin` mas nao coloca no
# PATH por padrao. Sem isso, nenhuma receita roda.
#
# As receitas com `docker run` prefixam MSYS_NO_PATHCONV=1. O shell do Git for
# Windows e um MSYS: sem essa variavel, ele reescreve argumento que parece
# caminho Unix e converte `-w /src` no diretorio equivalente dentro da propria
# instalacao do Git. O sintoma e um erro do daemon sobre um diretorio invalido,
# que parece problema de Docker e nao e.

GO       ?= go
GOFMT    ?= gofmt
GO_IMAGE ?= golang:1.27.0

.DEFAULT_GOAL := help

.PHONY: help check-ascii
##@ Ajuda

help: ## Lista os alvos disponiveis
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

##@ Gate

verify: check-ascii fmt-check vet build test ## Gate minimo: ASCII, gofmt, vet, build e testes
verify-docker: ## Gate minimo dentro da imagem oficial do Go
	MSYS_NO_PATHCONV=1 docker run --rm -v "$(CURDIR):/src" -w /src $(GO_IMAGE) make verify

##@ Codigo

# A convencao de escrever sem acento so se sustenta se o portao a verificar.
# README.md e docs/challenge.md ficam de fora: sao o enunciado do desafio,
# que vem com acentuacao e nao e nosso para reformatar.
#
# A deteccao usa LC_ALL=C com a classe [:print:], e nao grep -P com \x: no GNU
# grep 3.0 o \x dentro de expressao regular POSIX nao identifica o byte, e o
# padrao passa a casar com arquivo que e todo ASCII. Debaixo de LC_ALL=C, um
# byte acima de 0x7F nao e imprimivel, e e exatamente isso que se quer pegar.
check-ascii: ## Falha se arquivo versionado tiver caractere fora do ASCII
	@bad=$$(git ls-files '*.go' '*.md' 'Makefile' ':!README.md' ':!docs/challenge.md' \
		| LC_ALL=C xargs -r grep -l '[^[:print:][:space:]]' 2>/dev/null); \
	if [ -n "$$bad" ]; then \
		echo "arquivos com caractere fora do ASCII:"; echo "$$bad"; exit 1; \
	fi

fmt: ## Formata o codigo
	$(GO) fmt ./...

fmt-check: ## Falha se algum arquivo estiver fora do gofmt
	@unformatted="$$($(GOFMT) -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "arquivos fora do gofmt:"; echo "$$unformatted"; exit 1; \
	fi

vet: ## Executa go vet
	$(GO) vet ./...

build: ## Compila todos os pacotes
	$(GO) build ./...

tidy: ## Sincroniza go.mod e go.sum
	$(GO) mod tidy

##@ Testes

test: ## Executa a suite de testes
	$(GO) test ./...

test-race: ## Executa a suite com o detector de corrida
	$(GO) test -race ./...

cover: ## Gera o relatorio de cobertura em coverage.out
	$(GO) test -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out

test-docker: ## Executa a suite dentro da imagem oficial do Go
	MSYS_NO_PATHCONV=1 docker run --rm -v "$(CURDIR):/src" -w /src $(GO_IMAGE) make test

# O detector de corrida exige cgo e um compilador C. Uma maquina de
# desenvolvimento Windows sem `gcc` e sem `clang` nao tem um, e `go test -race`
# falha la com `-race requires cgo`. A imagem oficial tem gcc, entao este alvo e
# o caminho do portao de `-race` nessa maquina -- e nao um atalho opcional.
test-race-docker: ## Executa a suite com detector de corrida na imagem oficial do Go
	MSYS_NO_PATHCONV=1 docker run --rm -v "$(CURDIR):/src" -w /src $(GO_IMAGE) go test -race ./...

##@ Integracao

# Os testes de integracao rodam contra o PostgreSQL do Compose, com a tag
# `integration`. A tag existe para que `go test ./...` continue sendo um portao
# lento e sem dependencia: quem so quer verificar codigo nao precisa de container.
#
# `-p 1` e obrigatorio, e nao uma preferencia. Os pacotes de teste compartilham o
# mesmo banco e cada um limpa as tabelas no inicio; em paralelo, um pacote apaga o
# dado que o outro esta usando, e a falha aparece em um teste que passou sozinho.
test-integration: ## Executa os testes de integracao contra o Postgres do Compose
	$(GO) test -tags=integration -p 1 -count=1 ./tests/integration/...

migrations-up: ## Aplica as migrations pendentes
	$(GO) run ./cmd/migrate up

migrations-down: ## Reverte a ultima migration aplicada
	$(GO) run ./cmd/migrate down

migrations-status: ## Mostra o estado de cada migration
	$(GO) run ./cmd/migrate status

##@ Execucao

run: ## Sobe a aplicacao
	$(GO) run ./cmd/wager-service

shell-docker: ## Abre um shell na imagem oficial do Go, com o repo montado
	MSYS_NO_PATHCONV=1 docker run --rm -it -v "$(CURDIR):/src" -w /src $(GO_IMAGE) sh

##@ Limpeza

clean: ## Remove artefatos locais
	$(GO) clean
	rm -f coverage.out coverage.html
