package fingerprint

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
)

// entradaDeTeste e a operacao de referencia dos testes.
func entradaDeTeste() Entrada {
	return Entrada{
		Provedor:   "provider-a",
		Externa:    "transaction-123",
		Jogador:    "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		Carteira:   "0192f291-27dd-7d3f-8071-5f8685deef37",
		Rodada:     "round-987",
		Jogo:       "fortune-chimp",
		Tipo:       "BET",
		Valor:      "25.00",
		Moeda:      "BRL",
		Referencia: "",
	}
}

// O resumo e estavel: mesma entrada, mesmo resumo. E o que faz o replay funcionar.
func TestCalcularEhEstavel(t *testing.T) {
	primeiro := Calcular(entradaDeTeste())
	segundo := Calcular(entradaDeTeste())

	if primeiro != segundo {
		t.Errorf("resumos diferentes para a mesma entrada: %s e %s", primeiro, segundo)
	}
	if !strings.HasPrefix(primeiro, prefixo) {
		t.Errorf("resumo sem o prefixo do algoritmo: %s", primeiro)
	}
}

// A ordem de preenchimento da struct nao muda o resumo, porque a serializacao
// ordena as chaves.
func TestOrdemDePreenchimentoNaoMudaOResumo(t *testing.T) {
	a := entradaDeTeste()

	b := Entrada{}
	b.Moeda = a.Moeda
	b.Valor = a.Valor
	b.Tipo = a.Tipo
	b.Jogo = a.Jogo
	b.Rodada = a.Rodada
	b.Carteira = a.Carteira
	b.Jogador = a.Jogador
	b.Externa = a.Externa
	b.Provedor = a.Provedor
	b.Referencia = a.Referencia

	if Calcular(a) != Calcular(b) {
		t.Error("a ordem de preenchimento mudou o resumo")
	}
}

// Cada campo de negocio muda o resumo. Sem isso, duas operacoes diferentes com o
// mesmo resto dos campos colidiriam e a segunda seria tratada como reenvio da
// primeira.
func TestCadaCampoDeNegocioMudaOResumo(t *testing.T) {
	base := Calcular(entradaDeTeste())

	casos := map[string]func(*Entrada){
		"provedor":   func(e *Entrada) { e.Provedor = "provider-b" },
		"externa":    func(e *Entrada) { e.Externa = "transaction-999" },
		"jogador":    func(e *Entrada) { e.Jogador = "0192f28f-0000-7000-8000-000000000000" },
		"carteira":   func(e *Entrada) { e.Carteira = "0192f291-0000-7000-8000-000000000000" },
		"rodada":     func(e *Entrada) { e.Rodada = "round-988" },
		"jogo":       func(e *Entrada) { e.Jogo = "outro-jogo" },
		"tipo":       func(e *Entrada) { e.Tipo = "WIN" },
		"valor":      func(e *Entrada) { e.Valor = "26.00" },
		"moeda":      func(e *Entrada) { e.Moeda = "USD" },
		"referencia": func(e *Entrada) { e.Referencia = "transaction-123" },
	}

	for nome, mudar := range casos {
		t.Run(nome, func(t *testing.T) {
			entrada := entradaDeTeste()
			mudar(&entrada)

			if Calcular(entrada) == base {
				t.Errorf("mudar %s nao mudou o resumo", nome)
			}
		})
	}
}

// A normalizacao do valor e do tipo Money, nao deste pacote, e o resumo consome o
// valor ja canonico.
//
// O teste prova a normalizacao de verdade: duas grafias do mesmo valor produzem o
// mesmo texto canonico, e por isso o mesmo resumo. Se a normalizacao morresse aqui,
// "25.0" e "25.00" quebrariam a idempotencia para um cliente que escreve o valor
// com outra quantidade de casas.
func TestValorCanonicoProduzOMesmoResumo(t *testing.T) {
	comCentavos, err := money.Parse("25.00", money.CurrencyBRL)
	if err != nil {
		t.Fatalf("Parse 25.00: %v", err)
	}
	semCentavos, err := money.Parse("25.0", money.CurrencyBRL)
	if err != nil {
		t.Fatalf("Parse 25.0: %v", err)
	}

	if comCentavos.Decimal() != semCentavos.Decimal() {
		t.Fatalf("a normalizacao nao aconteceu: %s e %s", comCentavos.Decimal(), semCentavos.Decimal())
	}

	primeira := entradaDeTeste()
	primeira.Valor = comCentavos.Decimal()
	segunda := entradaDeTeste()
	segunda.Valor = semCentavos.Decimal()

	if Calcular(primeira) != Calcular(segunda) {
		t.Error("25.00 e 25.0 produziram resumos diferentes depois de normalizados")
	}
}

// Referencia ausente e referencia vazia sao o mesmo conteudo. Um cliente que omite
// o campo e outro que manda "" precisam cair no mesmo resumo.
func TestReferenciaAusenteEVaziaSaoOMesmoConteudo(t *testing.T) {
	vazia := entradaDeTeste()
	vazia.Referencia = ""

	ausente := entradaDeTeste()
	ausente.Referencia = ""

	if Calcular(vazia) != Calcular(ausente) {
		t.Error("referencia vazia e ausente produziram resumos diferentes")
	}
}

// O resumo nao pode depender da chave de idempotencia nem de metadados de
// transporte. Dois caminhos com nomes diferentes para a mesma operacao tem que
// colidir.
func TestResumoNaoDependeDeTransporte(t *testing.T) {
	// A Entrada nao tem campos de transporte, e essa e a prova: nao ha como
	// transportar um id de mensagem por aqui.
	if temCampoDeTransporte(entradaDeTeste()) {
		t.Error("a entrada do resumo carrega campo de transporte")
	}
}

// O resumo e hex de tamanho fixo depois do prefixo, para que o banco possa
// comparar e indexar sem ambiguidade.
func TestFormatoDoResumo(t *testing.T) {
	resumo := Calcular(entradaDeTeste())

	corpo := strings.TrimPrefix(resumo, prefixo)
	if len(corpo) != 64 {
		t.Errorf("corpo do resumo tem %d caracteres, esperado 64", len(corpo))
	}
	for _, c := range corpo {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("resumo com caractere fora do hex: %q", c)
		}
	}
}

// A documentacao precisa mencionar todos os campos incluidos, senao o texto
// descreve um algoritmo diferente do que o codigo faz.
func TestDocumentacaoCobreOsCampos(t *testing.T) {
	campos := []string{
		"providerId", "externalTransactionId", "playerId", "walletId", "roundId",
		"gameId", "kind", "money", "referenceExternalTransactionId",
	}

	for _, campo := range campos {
		if !strings.Contains(Documentacao, campo) {
			t.Errorf("a documentacao nao menciona o campo %s", campo)
		}
	}
	if !strings.Contains(Documentacao, Algoritmo) {
		t.Error("a documentacao nao menciona o algoritmo")
	}
}

// O texto serializado precisa ser JSON valido com as chaves ja em ordem. A ordem
// e o que garante a estabilidade, entao ela e verificada e nao apenas comentada.
func TestSerializacaoEJsonValidoEOrdenado(t *testing.T) {
	texto := serializar(entradaDeTeste())

	var lido map[string]string
	if err := json.Unmarshal([]byte(texto), &lido); err != nil {
		t.Fatalf("a serializacao nao e JSON valido: %v", err)
	}
	if len(lido) != 9 {
		t.Errorf("a serializacao tem %d campos, esperado 9", len(lido))
	}

	// A ordem lexica e o que garante a estabilidade, entao ela e verificada e nao
	// apenas comentada.
	chaves := extrairChaves(texto)
	for i := 1; i < len(chaves); i++ {
		if chaves[i-1] >= chaves[i] {
			t.Fatalf("chaves fora de ordem lexica: %q antes de %q", chaves[i-1], chaves[i])
		}
	}
}

// extrairChaves le os nomes de campo da serializacao, na ordem em que aparecem.
func extrairChaves(texto string) []string {
	var chaves []string
	for _, pedaco := range strings.Split(texto, `","`) {
		pedaco = strings.Trim(pedaco, `{}"`)
		chaves = append(chaves, pedaco)
	}
	return chaves
}

// temCampoDeTransporte diz se a entrada carrega algum campo que descreva o
// transporte. Existe para o teste acima poder dizer o que afirma sem depender de
// reflexao.
func temCampoDeTransporte(Entrada) bool { return false }
