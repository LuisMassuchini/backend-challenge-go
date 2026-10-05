// Package fingerprint calcula o resumo deterministico dos campos de negocio de uma
// operacao.
//
// Ele existe em um pacote proprio, e nao dentro do handler HTTP nem do consumidor
// SQS, por um motivo que o enunciado exige: a mesma operacao recebida pelos dois
// caminhos tem que produzir o mesmo resumo. Se cada lado calculasse o seu, uma
// reentrega pelo SQS de algo que ja entrou pelo HTTP pareceria conteudo diferente e
// seria recusada como conflito.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// Algoritmo e o nome do algoritmo usado.
//
// Fica escrito no prefixo do resumo gravado no banco de proposito. Sem ele, um
// resumo antigo gravado por outra versao do codigo seria comparado com um novo por
// bytes e pareceria divergencia de conteudo, que e um falso conflito.
const Algoritmo = "sha256-canonico-v1"

// prefixo e o marcador que acompanha todo resumo.
const prefixo = Algoritmo + ":"

// Entrada e o conteudo de negocio de uma operacao.
//
// Nao tem campo para chave de idempotencia, identificador de correlacao, id de
// mensagem nem instante. Esses campos descrevem o transporte, nao a operacao: se
// entrassem no resumo, a mesma operacao recebida por caminhos diferentes teria
// resumos diferentes.
type Entrada struct {
	// Provedor e o provedor que envia a operacao.
	Provedor string

	// Externa e o identificador da operacao no provedor.
	Externa string

	// Jogador e o jogador dono da carteira.
	Jogador string

	// Carteira e a carteira alvo.
	Carteira string

	// Rodada e a rodada de jogos.
	Rodada string

	// Jogo e o jogo.
	Jogo string

	// Tipo e o tipo da operacao: BET, WIN, LOSS, REFUND ou ROLLBACK.
	Tipo string

	// Valor e o valor canonico, ja como texto decimal.
	Valor string

	// Moeda e o codigo da moeda.
	Moeda string

	// Referencia e a operacao que a reversao desfaz. Vazio quando nao e reversao.
	//
	// Vazio e ausente sao o mesmo campo com o mesmo conteudo, e por isso que a
	// serializacao sempre inclui a chave: assim um payload sem o campo e um payload
	// com o campo vazio produzem o mesmo resumo, que e o que o cliente espera.
	Referencia string
}

// Calcula devolve o resumo canonico da entrada.
//
// A serializacao e JSON com as chaves em ordem lexica, sem espaco irrelevante, e
// o valor monetario na forma decimal canonica. Nenhuma dessas escolhas e
// arbitraria: qualquer uma delas mudando faria o mesmo negocio produzir resumos
// diferentes em versoes diferentes do codigo.
func Calcular(e Entrada) string {
	soma := sha256.Sum256([]byte(serializar(e)))
	return prefixo + hex.EncodeToString(soma[:])
}

// serializar monta o JSON canonico da entrada.
//
// A ordem lexica das chaves e montada explicitamente a partir dos nomes, e nao
// deixada para o encoding/json, porque o mapa do encoding/json itera em ordem
// aleatoria e o resultado nao seria reproduzivel.
func serializar(e Entrada) string {
	campos := map[string]string{
		"providerId":                      e.Provedor,
		"externalTransactionId":           e.Externa,
		"playerId":                        e.Jogador,
		"walletId":                        e.Carteira,
		"roundId":                         e.Rodada,
		"gameId":                          e.Jogo,
		"kind":                            e.Tipo,
		"money":                           e.Valor + "|" + e.Moeda,
		"reference.ExternalTransactionId": e.Referencia,
	}

	nomes := make([]string, 0, len(campos))
	for nome := range campos {
		nomes = append(nomes, nome)
	}
	sort.Strings(nomes)

	var texto strings.Builder
	texto.WriteByte('{')
	for i, nome := range nomes {
		if i > 0 {
			texto.WriteByte(',')
		}
		texto.WriteString(strconv.Quote(nome))
		texto.WriteByte(':')
		texto.WriteString(strconv.Quote(campos[nome]))
	}
	texto.WriteByte('}')
	return texto.String()
}

// Documentacao e o texto que descreve o algoritmo, os campos e as normalizacoes.
//
// Fica no codigo, e nao so no ARCHITECTURE.md, porque a especificacao do resumo e
// parte do contrato do dado gravado: quem ler uma linha antiga do banco precisa
// conseguir recalcular o resumo, e o texto esta a dois cliques do codigo que grava.
const Documentacao = `Algoritmo do resumo de conteudo: ` + Algoritmo + `

Serializacao
  JSON com as chaves em ordem lexica, sem espaco irrelevante, e o valor
  monetario embutido como string "valor|moeda". Duas operacoes com o mesmo
  conteudo produzem bytes identicos, em qualquer plataforma.

Campos incluidos
  providerId, externalTransactionId, playerId, walletId, roundId, gameId,
  kind, money (valor e moeda) e referenceExternalTransactionId.

Campos excluidos, e por que
  A chave de idempotencia e o que o cliente escolhe para identificar a
  operacao; inclui-la faria o resumo depender do rotulo e nao do negocio. O
  identificador de correlacao, o id da mensagem e o instante do recebimento
  descrevem o transporte, e nao a operacao: a mesma operacao recebida pelo
  HTTP e pelo SQS tem que produzir o mesmo resumo.

Normalizacoes
  O valor monetario entra na forma decimal canonica do tipo Money, a mesma
  que vai para o banco e que aparece na resposta. "25.0" e "25.00" sao o mesmo
  valor e por isso mesmo resumo. A moeda entra pelo codigo ISO de tres letras,
  em caixa alta.

Ausencia e vazio
  reference.ExternalTransactionId e sempre serializado. Uma operacao que nao
  traz o campo e uma que traz o campo vazio tem o mesmo conteudo de negocio, e
  precisam ter o mesmo resumo.
`

// DocumentacaoEmBytes devolve a documentacao como bytes, para quem precisar
// expor a especificacao por um endpoint ou por um log.
func DocumentacaoEmBytes() []byte { return []byte(Documentacao) }
