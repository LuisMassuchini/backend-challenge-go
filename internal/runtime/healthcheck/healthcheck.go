// Package healthcheck faz a sonda de liveness do wager-service.
//
// Existe como subcomando do proprio binario, e nao como script, porque a imagem
// final e distroless: nao tem curl, nem wget, nem shell. Um `healthcheck` de
// Compose com `CMD-SHELL` daria "exec: curl: not found" no primeiro start, e o
// sintoma -- servico marcado como unhealthy logo depois de subir sem erro
// visivel -- aponta para a imagem, nao para o Compose.
//
// O que a sonda consulta e o endpoint publico `/health/live` do proprio processo,
// e nao `/health/ready`. Liveness e sobre existir: um probe que consulta o banco
// derruba o processo quando o banco cai, e o efeito e o oposto do desejado,
// porque o banco voltando nao traz o processo de volta se o orquestrador ja o
// matou.
package healthcheck

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Prazo de uma sondagem.
//
// Curto de proposito: o probe do Compose ja tem o seu proprio `timeout`, e um
// probe que espera mais que isso e interrompido antes de dar resposta -- o que
// o Compose registra como falha, mesmo tendo o processo responsivo.
const prazo = 2 * time.Second

// Erro e o resultado de uma sondagem que nao deu certo.
type Erro struct {
	// Endereco e a URL consultada, para que o log do Compose diga onde.
	Endereco string
	// Motivo e o que deu errado.
	Motivo string
}

func (e *Erro) Error() string {
	return fmt.Sprintf("sonda em %s: %s", e.Endereco, e.Motivo)
}

// Roda consulta /health/live de uma vez.
//
// O parametro `endereco` e o que a sonda deve consultar. Quem chama decide se e
// o endereco da maquina ou o do proprio container -- dentro do Compose, `localhost`
// ja e o processo, porque a sonda roda como processo separado no mesmo container.
func Roda(endereco string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endereco, nil)
	if err != nil {
		return &Erro{Endereco: endereco, Motivo: fmt.Sprintf("montando a requisicao: %v", err)}
	}

	// Sem `Connection: close`, a conexao fica no pool do cliente e o probe vira um
	// pool de sockets abertos contra o proprio processo, um a cada dez segundos.
	// Nao e problema de recurso em duas horas; e problema de numero de descritores
	// em um container com limite.
	req.Close = true

	resp, err := (&http.Client{Timeout: prazo}).Do(req)
	if err != nil {
		return &Erro{Endereco: endereco, Motivo: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &Erro{
			Endereco: endereco,
			Motivo:   fmt.Sprintf("respondeu %d, e o esperado e 200", resp.StatusCode),
		}
	}
	return nil
}

// DaExecucao roda a sonda a partir dos argumentos do processo.
//
// O endereco vem do primeiro argumento, e o padrao `http://localhost:8080/health/live`
// ja e o certo dentro do container, porque a sonda roda na mesma rede de loopback do
// processo. O argumento existe para quem roda a imagem fora do Compose, com a
// aplicacao em outro endereco -- e para o teste, que precisa de um servidor efemero.
//
// `io.Writer` e nao `*os.File` porque a saida de um probe nao tem nada de arquivo:
// o Compose le stderr, e o teste precisa de algo que aceite escrita e que se possa
// ler, que `*os.File` nao permite sem criar arquivo temporario para um teste que
// nao tem relacao com disco.
//
// O codigo de saida e o contrato com o orquestrador: zero para vivo, um para
// qualquer outra coisa.
func DaExecucao(argumentos []string, saidaErro io.Writer) int {
	endereco := "http://localhost:8080/health/live"
	if len(argumentos) > 0 && argumentos[0] != "" {
		endereco = argumentos[0]
	}

	if err := Roda(endereco); err != nil {
		fmt.Fprintf(saidaErro, "healthcheck: %v\n", err)
		return 1
	}
	return 0
}
