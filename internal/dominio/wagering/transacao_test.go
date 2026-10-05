package wagering

import (
	"errors"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

const (
	jogadorA  = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	carteiraA = "0192f291-27dd-7d3f-8071-5f8685deef37"
	rodadaA   = "round-987"
	jogoA     = "fortune-chimp"
	provedorA = "provider-a"
	externoA  = "transaction-123"
	chaveA    = "provider-a:transaction-123"
	hashA     = "9f2c1b0a4d7e8f3a5c6b2d1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1"
)

// estes testes cobrem a maquina de estados: quem entra, quem nao sai, e o que a
// reidratacao pode e nao pode reconstruir.

func deveID(t *testing.T, texto string) wallet.Identificador {
	t.Helper()
	id, err := wallet.IdentificadorDe(texto)
	if err != nil {
		t.Fatalf("wallet.IdentificadorDe(%q): %v", texto, err)
	}
	return id
}

func deveParse(t *testing.T, texto string) money.Money {
	t.Helper()
	m, err := money.Parse(texto, money.CurrencyBRL)
	if err != nil {
		t.Fatalf("money.Parse(%q): %v", texto, err)
	}
	return m
}

func instanteFixo() time.Time {
	return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
}

// externaDoTeste monta uma transacao externa valida, para que cada teste de
// transicao comece do mesmo ponto e o que muda seja so a transicao testada.
func externaDoTeste(t *testing.T) Transacao {
	t.Helper()
	tr, err := Registrar(Registro{
		Provedor:          Provedor(provedorA),
		TransacaoExterna:  Externo(externoA),
		ChaveIdempotencia: Chave(chaveA),
		HashConteudo:      Hash(hashA),
		Carteira:          deveID(t, carteiraA),
		Jogador:           deveID(t, jogadorA),
		Rodada:            Rodada(rodadaA),
		Jogo:              Jogo(jogoA),
		Tipo:              TipoBET,
		Valor:             deveParse(t, "25.00"),
		CriadaEm:          instanteFixo(),
	})
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}
	return tr
}

// reversaoDoTeste monta uma operacao de reversao com referencia informada.
func reversaoDoTeste(t *testing.T, tipo Tipo) Transacao {
	t.Helper()
	tr, err := Registrar(Registro{
		Provedor:          Provedor(provedorA),
		TransacaoExterna:  Externo("transaction-999"),
		ChaveIdempotencia: Chave("provider-a:transaction-999"),
		HashConteudo:      Hash(hashA),
		Carteira:          deveID(t, carteiraA),
		Jogador:           deveID(t, jogadorA),
		Rodada:            Rodada(rodadaA),
		Jogo:              Jogo(jogoA),
		Tipo:              tipo,
		Valor:             deveParse(t, "25.00"),
		Referencia:        Referencia{Externa: Externo(externoA)},
		CriadaEm:          instanteFixo(),
	})
	if err != nil {
		t.Fatalf("Registrar %s: %v", tipo, err)
	}
	return tr
}

func TestTransacaoNascePendente(t *testing.T) {
	tr := externaDoTeste(t)

	if tr.Estado() != EstadoPendente {
		t.Errorf("Estado e %q, esperado %q", tr.Estado(), EstadoPendente)
	}
	if !tr.ID().Valida() {
		t.Error("transacao sem id valido")
	}
	if tr.Provedor().String() != provedorA {
		t.Errorf("Provedor e %q, esperado %q", tr.Provedor(), provedorA)
	}
	if tr.TransacaoExterna().String() != externoA {
		t.Errorf("TransacaoExterna e %q, esperado %q", tr.TransacaoExterna(), externoA)
	}
	if tr.ChaveIdempotencia().String() != chaveA {
		t.Errorf("ChaveIdempotencia e %q, esperado %q", tr.ChaveIdempotencia(), chaveA)
	}
	if tr.Carteira().String() != carteiraA {
		t.Errorf("Carteira e %q, esperado %q", tr.Carteira(), carteiraA)
	}
	if tr.Rodada().String() != rodadaA {
		t.Errorf("Rodada e %q, esperado %q", tr.Rodada(), rodadaA)
	}
	if tr.CriadaEm().IsZero() {
		t.Error("transacao sem instante de criacao")
	}
}

// Uma transacao pendente nao tem resultado nem codigo de falha. Um dos dois
// preenchidos indicaria que o estado nao corresponde aos dados, e a leitura de
// auditoria passaria a confiar em dado que nao existe.
func TestTransacaoPendenteNaoTemResultadoNemFalha(t *testing.T) {
	tr := externaDoTeste(t)

	if tr.Resultado().Valida() {
		t.Errorf("transacao pendente ja tem resultado %s", tr.Resultado())
	}
	if tr.CodigoFalha() != CodigoFalha("") {
		t.Errorf("transacao pendente ja tem codigo de falha %q", tr.CodigoFalha())
	}
	if tr.ReferenciaInterna().Valida() {
		t.Error("transacao pendente ja tem referencia interna")
	}
}

func TestTransicaoParaProcessada(t *testing.T) {
	tr := externaDoTeste(t)

	processada, err := tr.Processar(deveParse(t, "975.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Processar: %v", err)
	}

	if processada.Estado() != EstadoProcessado {
		t.Errorf("Estado e %q, esperado %q", processada.Estado(), EstadoProcessado)
	}
	if !processada.Resultado().Equal(deveParse(t, "975.00")) {
		t.Errorf("Resultado e %s, esperado 975.00", processada.Resultado())
	}
	if !processada.AtualizadaEm().Equal(instanteFixo()) {
		t.Errorf("AtualizadaEm e %v", processada.AtualizadaEm())
	}
}

func TestTransicaoParaRejeitada(t *testing.T) {
	tr := externaDoTeste(t)

	rejeitada, err := tr.Rejeitar(CodigoFalhaSemSaldo, instanteFixo())
	if err != nil {
		t.Fatalf("Rejeitar: %v", err)
	}

	if rejeitada.Estado() != EstadoRejeitado {
		t.Errorf("Estado e %q, esperado %q", rejeitada.Estado(), EstadoRejeitado)
	}
	if rejeitada.CodigoFalha() != CodigoFalhaSemSaldo {
		t.Errorf("CodigoFalha e %q, esperado %q", rejeitada.CodigoFalha(), CodigoFalhaSemSaldo)
	}
}

func TestTransicaoParaFalha(t *testing.T) {
	// Falha permanente e diferente de rejeicao: rejeicao e regra de negocio e o
	// cliente pode corrigir a entrada; falha e infraestrutura e o cliente nao tem
	// o que corrigir. A distincao precisa sobreviver na auditoria.
	tr := externaDoTeste(t)

	falhou, err := tr.Falhar(CodigoFalhaPersistencia, instanteFixo())
	if err != nil {
		t.Fatalf("Falhar: %v", err)
	}

	if falhou.Estado() != EstadoFalhou {
		t.Errorf("Estado e %q, esperado %q", falhou.Estado(), EstadoFalhou)
	}
	if falhou.CodigoFalha() != CodigoFalhaPersistencia {
		t.Errorf("CodigoFalha e %q, esperado %q", falhou.CodigoFalha(), CodigoFalhaPersistencia)
	}
}

// Estados terminais nao aceitam transicao. Uma transacao processada que volta a
// pendente seria processada de novo, e o debito duplicado aparece no ledger.
func TestEstadoTerminalNaoAceitaNovaTransicao(t *testing.T) {
	processada, err := externaDoTeste(t).Processar(deveParse(t, "975.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Processar: %v", err)
	}
	rejeitada, err := externaDoTeste(t).Rejeitar(CodigoFalhaSemSaldo, instanteFixo())
	if err != nil {
		t.Fatalf("Rejeitar: %v", err)
	}
	falhou, err := externaDoTeste(t).Falhar(CodigoFalhaPersistencia, instanteFixo())
	if err != nil {
		t.Fatalf("Falhar: %v", err)
	}

	terminais := map[string]Transacao{
		"PROCESSED": processada,
		"REJECTED":  rejeitada,
		"FAILED":    falhou,
	}

	for nome, tr := range terminais {
		t.Run(nome, func(t *testing.T) {
			if _, err := tr.Processar(deveParse(t, "1.00"), instanteFixo()); !errors.Is(err, ErrEstadoTerminal) {
				t.Errorf("Processar devolveu %v, esperado ErrEstadoTerminal", err)
			}
			if _, err := tr.Rejeitar(CodigoFalhaSemSaldo, instanteFixo()); !errors.Is(err, ErrEstadoTerminal) {
				t.Errorf("Rejeitar devolveu %v, esperado ErrEstadoTerminal", err)
			}
			if _, err := tr.Falhar(CodigoFalhaPersistencia, instanteFixo()); !errors.Is(err, ErrEstadoTerminal) {
				t.Errorf("Falhar devolveu %v, esperado ErrEstadoTerminal", err)
			}
			if _, err := tr.EsperarReferencia(instanteFixo()); !errors.Is(err, ErrEstadoTerminal) {
				t.Errorf("EsperarReferencia devolveu %v, esperado ErrEstadoTerminal", err)
			}
		})
	}
}

// PENDENTE pode ir para qualquer estado nao terminal. E o que faz uma operacao
// sem dependencia ser concluida sincronamente e uma operacao com dependencia
// ficar esperando.
func TestPendenteAceitaTodosOsEstadosNaoTerminais(t *testing.T) {
	// Espera por referencia so faz sentido para quem tem referencia informada:
	// e o caso de REFUND e ROLLBACK cujo referenciaExterna chegou mas cuja
	// transacao referenciada ainda nao existe no sistema.
	esperando, err := reversaoDoTeste(t, TipoREFUND).EsperarReferencia(instanteFixo())
	if err != nil {
		t.Fatalf("EsperarReferencia: %v", err)
	}
	if esperando.Estado() != EstadoPendenteReferencia {
		t.Errorf("Estado e %q, esperado %q", esperando.Estado(), EstadoPendenteReferencia)
	}

	// De PENDING_REFERENCE a resolucao leva a processado ou a rejeitado, e a
	// espera pode se repetir se a referencia ainda nao chegou.
	processada, err := esperando.Processar(deveParse(t, "975.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Processar a partir de PENDING_REFERENCE: %v", err)
	}
	if processada.Estado() != EstadoProcessado {
		t.Errorf("Estado e %q, esperado %q", processada.Estado(), EstadoProcessado)
	}

	rejeitada, err := esperando.Rejeitar(CodigoFalhaReferenciaNaoEncontrada, instanteFixo())
	if err != nil {
		t.Fatalf("Rejeitar a partir de PENDING_REFERENCE: %v", err)
	}
	if rejeitada.Estado() != EstadoRejeitado {
		t.Errorf("Estado e %q, esperado %q", rejeitada.Estado(), EstadoRejeitado)
	}

	novaEspera, err := esperando.EsperarReferencia(instanteFixo())
	if err != nil {
		t.Fatalf("EsperarReferencia repetida: %v", err)
	}
	if novaEspera.Estado() != EstadoPendenteReferencia {
		t.Errorf("Estado e %q, esperado %q", novaEspera.Estado(), EstadoPendenteReferencia)
	}
}

// PENDENTE sem referencia nao pode ir para PENDING_REFERENCE. A espera existe
// para quando a referencia foi informada e a transacao referenciada ainda nao
// chegou; uma espera sem referencia seria um estado em que a transacao nao
// tem como progredir.
func TestEsperaPorReferenciaSemReferenciaRecusa(t *testing.T) {
	if _, err := externaDoTeste(t).EsperarReferencia(instanteFixo()); !errors.Is(err, ErrRegistroInvalido) {
		t.Errorf("devolveu %v, esperado ErrRegistroInvalido", err)
	}
}

func TestTransicaoRecusadaNaoMudaEstadoNemDados(t *testing.T) {
	processada, err := externaDoTeste(t).Processar(deveParse(t, "975.00"), instanteFixo())
	if err != nil {
		t.Fatalf("Processar: %v", err)
	}

	recusada, err := processada.Rejeitar(CodigoFalhaSemSaldo, instanteFixo())
	if !errors.Is(err, ErrEstadoTerminal) {
		t.Fatalf("devolveu %v, esperado ErrEstadoTerminal", err)
	}
	if recusada.Estado() != EstadoProcessado {
		t.Errorf("Estado e %q, esperado %q: recusa nao pode mudar estado", recusada.Estado(), EstadoProcessado)
	}
	if !recusada.Resultado().Equal(deveParse(t, "975.00")) {
		t.Errorf("Resultado e %s, esperado 975.00: recusa nao pode apagar resultado", recusada.Resultado())
	}
	if recusada.CodigoFalha() != CodigoFalha("") {
		t.Errorf("CodigoFalha e %q, esperado vazio", recusada.CodigoFalha())
	}
}

// A reidratacao repoe o estado lido do banco sem reexecutar regra e sem emitir
// evento. Por isso recebe todos os campos e nao calcula nada.
// reidratacaoDoTeste monta os dados de uma transacao ja persistida. Existe
// porque a reidratacao recebe um unico struct: com dezesseis argumentos
// posicionais, trocar dois identificadores compila e grava a transacao na
// carteira errada.
func reidratacaoDoTeste(t *testing.T, estado Estado, codigo CodigoFalha) Dados {
	return Dados{
		ID:                deveID(t, carteiraA),
		Provedor:          Provedor(provedorA),
		TransacaoExterna:  Externo(externoA),
		ChaveIdempotencia: Chave(chaveA),
		HashConteudo:      Hash(hashA),
		Carteira:          deveID(t, carteiraA),
		Jogador:           deveID(t, jogadorA),
		Rodada:            Rodada(rodadaA),
		Jogo:              Jogo(jogoA),
		Tipo:              TipoBET,
		Valor:             deveParse(t, "25.00"),
		Estado:            estado,
		CodigoFalha:       codigo,
		CriadaEm:          instanteFixo(),
		AtualizadaEm:      instanteFixo().Add(time.Minute),
	}
}

func TestReidratacaoRepoeEstadoTerminal(t *testing.T) {
	dados := reidratacaoDoTeste(t, EstadoRejeitado, CodigoFalhaSemSaldo)

	tr, err := Reidratar(dados)
	if err != nil {
		t.Fatalf("Reidratar: %v", err)
	}

	if tr.Estado() != EstadoRejeitado {
		t.Errorf("Estado e %q, esperado %q", tr.Estado(), EstadoRejeitado)
	}
	if tr.CodigoFalha() != CodigoFalhaSemSaldo {
		t.Errorf("CodigoFalha e %q, esperado %q", tr.CodigoFalha(), CodigoFalhaSemSaldo)
	}
	if !tr.CriadaEm().Equal(dados.CriadaEm) {
		t.Errorf("CriadaEm e %v", tr.CriadaEm())
	}
	if !tr.AtualizadaEm().Equal(dados.AtualizadaEm) {
		t.Errorf("AtualizadaEm e %v", tr.AtualizadaEm())
	}
}

// Estado terminal sem codigo de falha e estado que a auditoria nao consegue
// explicar. O esquema vai permitir a coluna nula para estados nao terminais, e
// a regra de que terminal sempre tem codigo e do dominio.
func TestReidratacaoDeTerminalSemCodigoDeFalhaRecusa(t *testing.T) {
	casos := []Estado{EstadoRejeitado, EstadoFalhou}

	for _, estado := range casos {
		t.Run(string(estado), func(t *testing.T) {
			dados := reidratacaoDoTeste(t, estado, CodigoFalha(""))
			if _, err := Reidratar(dados); !errors.Is(err, ErrEstadoInvalido) {
				t.Errorf("devolveu %v, esperado ErrEstadoInvalido", err)
			}
		})
	}
}

// PROCESSED carrega o resultado financeiro devolvido ao provedor. Rehydrate-lo
// sem resultado deixaria a resposta do replay sem o saldo observado no
// processamento original, que e o que o enunciado exige.
func TestReidratacaoDeProcessadaSemResultadoRecusa(t *testing.T) {
	dados := reidratacaoDoTeste(t, EstadoProcessado, CodigoFalha(""))
	if _, err := Reidratar(dados); !errors.Is(err, ErrEstadoInvalido) {
		t.Errorf("devolveu %v, esperado ErrEstadoInvalido", err)
	}

	dados.Resultado = deveParse(t, "975.00")
	if _, err := Reidratar(dados); err != nil {
		t.Errorf("processada com resultado foi recusada: %v", err)
	}
}

func TestReidratacaoRecusaEstadoDesconhecido(t *testing.T) {
	dados := reidratacaoDoTeste(t, Estado("EM_ANDAMENTO"), CodigoFalha(""))

	if _, err := Reidratar(dados); !errors.Is(err, ErrEstadoInvalido) {
		t.Errorf("devolveu %v, esperado ErrEstadoInvalido", err)
	}
}

func TestReidratacaoRecusaResultadoEmEstadoNaoTerminal(t *testing.T) {
	// Resultado em estado nao terminal e dado inconsistente: ha saldo devolvido
	// sem operacao concluida. Aceitar faria o replay responder com um saldo que
	// nao corresponde a nenhum movimento.
	dados := reidratacaoDoTeste(t, EstadoPendente, CodigoFalha(""))
	dados.Resultado = deveParse(t, "975.00")

	if _, err := Reidratar(dados); !errors.Is(err, ErrEstadoInvalido) {
		t.Errorf("devolveu %v, esperado ErrEstadoInvalido", err)
	}
}

func TestResultadoDeTransacaoNaoInicializadaNaoEValido(t *testing.T) {
	// O valor zero de Transacao nao e uma transacao. Devolve-lo como valor em
	// vez de erro faria um caso de uso tratar o zero como transacao pendente.
	var tr Transacao
	if tr.Valida() {
		t.Fatal("Transacao nao inicializada se diz valida")
	}
	if tr.Estado() != Estado("") {
		t.Errorf("Estado e %q, esperado vazio", tr.Estado())
	}
}

func TestEstadoValidoEConhecido(t *testing.T) {
	// A lista de estados e fechada. Um estado novo aceito por engano passaria a
	// ser gravado e a aparecer em consulta de auditoria sem tratamento.
	naoTerminais := map[Estado]bool{
		EstadoPendente:           true,
		EstadoPendenteReferencia: true,
	}
	terminais := map[Estado]bool{
		EstadoProcessado: true,
		EstadoRejeitado:  true,
		EstadoFalhou:     true,
	}

	for estado := range naoTerminais {
		if !estado.Valido() {
			t.Errorf("estado %q do conjunto se diz invalido", estado)
		}
		if estado.Terminal() {
			t.Errorf("estado %q se diz terminal", estado)
		}
	}
	for estado := range terminais {
		if !estado.Valido() {
			t.Errorf("estado %q do conjunto se diz invalido", estado)
		}
		if !estado.Terminal() {
			t.Errorf("estado %q nao se diz terminal", estado)
		}
	}

	if Estado("CANCELLED").Valido() {
		t.Error("estado fora do conjunto se diz valido")
	}
}
