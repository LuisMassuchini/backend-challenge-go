# Imagem do wager-service.
#
# A tag do Go aparece aqui e no go.mod. O enunciado pede a versao declarada nos
# dois lugares, e a razao e reproduzibilidade: `golang:latest` muda de conteudo
# sem que ninguem perceba, e um build que nao fixa a versao nao pode ser repetido
# daqui a seis meses.
#
# O binario e compilado com CGO_ENABLED=0, o que produz um executavel estatico:
# a imagem final nao precisa de libc, nem de shell, nem de package manager. E o
# que vai na imagem final e apenas o binario.

FROM golang:1.27.0 AS build

WORKDIR /src

# As dependencias ficam em camada propria. Elas mudam com muito menos frequencia
# que o codigo, e sem essa separacao todo go mod download e reexecutado a cada
# mudanca em um unico arquivo.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# A versao e injetada em buildinfo.version. Sem este ARG, o binario declara
# apenas "dev", que e o valor honesto para quem nao informou nada.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X github.com/LuisMassuchini/backend-challenge-go/internal/runtime/buildinfo.version=${VERSION}" \
      -o /out/wager-service \
      ./cmd/wager-service

# O binario de migration viaja na MESMA imagem, e nao em uma imagem separada.
#
# O Compose sobe a migration e a aplicacao em servicos diferentes, com o segundo
# depending_on do primeiro. Duas imagens custariam um segundo build e uma segunda
# copia de todo o codigo para um executavel de duas telas; a imagem final e a
# mesma, e o que muda e o `entrypoint` de cada servico.
#
# As migrations tambem ficam dentro de `internal`, em `internal/pg/migrations`, e
# sao embutidas no binario por `//go:embed`. E por isso que a imagem precisa do
# codigo em tempo de build e nao de um volume com os arquivos `.sql` em tempo de
# execucao: o esquema viaja junto com o binario que o aplica, e nao pode divergir
# dele.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w" \
      -o /out/migrate \
      ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/wager-service /wager-service
COPY --from=build /out/migrate /migrate

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/wager-service"]
