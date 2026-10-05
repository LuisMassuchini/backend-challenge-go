package eventos

import (
	"encoding/json"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
)

// Os quatro eventos que o enunciado exige, cada um com payload tipado. O que se
// verifica aqui e que o tipo do evento e o payload sao decidedos pelo construtor:
// nenhum chamador monta payload a mao, e nenhum evento sai com payload de outro
// tipo.

func deveParse(t *testing.T, texto string) money.Money {
	t.Helper()
	m, err := money.Parse(texto, money.CurrencyBRL)
	if err != nil {
		t.Fatalf("money.Parse(%q): %v", texto, err)
	}
	return m
}

// deveString busca uma chave no payload, para que o teste leia o que de fato foi
// gravado e nao o que a struct tem em memoria.
func deveCampo(t *testing.T, payload []byte, chave string) string {
	t.Helper()
	var dados map[string]any
	if err := json.Unmarshal(payload, &dados); err != nil {
		t.Fatalf("payload nao e JSON: %v", err)
	}
	valor, ok := dados[chave]
	if !ok {
		t.Fatalf("payload sem a chave %q: %s", chave, payload)
	}
	texto, ok := valor.(string)
	if !ok {
		return ""
	}
	return texto
}

func TestTransacaoProcessadaTemOsCamposDoContrato(t *testing.T) {
	e, err := NovaTransacaoProcessada(DadosTransacaoProcessada{
		Transacao:  deveID(t, transacaoA),
		Provedor:   "provider-a",
		Externo:    "transaction-123",
		Carteira:   deveID(t, carteiraA),
		Tipo:       "BET",
		Resultado:  deveParse(t, "975.00"),
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("NovaTransacaoProcessada: %v", err)
	}

	if e.Tipo() != TipoTransacaoProcessada {
		t.Errorf("Tipo e %q, esperado %q", e.Tipo(), TipoTransacaoProcessada)
	}
	if e.AggregateID() != deveID(t, transacaoA) {
		t.Errorf("AggregateID e %s, esperado %s", e.AggregateID(), transacaoA)
	}

	payload := e.Payload()
	if got := deveCampo(t, payload, "transactionId"); got != transacaoA {
		t.Errorf("transactionId e %q, esperado %q", got, transacaoA)
	}
	if got := deveCampo(t, payload, "providerId"); got != "provider-a" {
		t.Errorf("providerId e %q, esperado %q", got, "provider-a")
	}
	if got := deveCampo(t, payload, "externalTransactionId"); got != "transaction-123" {
		t.Errorf("externalTransactionId e %q", got)
	}
	if got := deveCampo(t, payload, "walletId"); got != carteiraA {
		t.Errorf("walletId e %q", got)
	}
	if got := deveCampo(t, payload, "kind"); got != "BET" {
		t.Errorf("kind e %q", got)
	}
	if got := deveCampo(t, payload, "status"); got != "PROCESSED" {
		t.Errorf("status e %q, esperado PROCESSED", got)
	}
	// O saldo devolvido e decimal em string, nunca numero. Numero no payload
	// reintroduz o float que Money existe para impedir.
	if got := deveCampo(t, payload, "balance"); got != "975.00" {
		t.Errorf("balance e %q, esperado %q", got, "975.00")
	}
	if deveCampo(t, payload, "currency") != "BRL" {
		t.Errorf("currency e %q", deveCampo(t, payload, "currency"))
	}
}

func TestTransacaoRejeitadaTemOCodigoDeFalha(t *testing.T) {
	e, err := NovaTransacaoRejeitada(DadosTransacaoRejeitada{
		Transacao:  deveID(t, transacaoA),
		Provedor:   "provider-a",
		Externo:    "transaction-123",
		Carteira:   deveID(t, carteiraA),
		Tipo:       "BET",
		Valor:      deveParse(t, "80.00"),
		Falha:      "BET_SEM_SALDO",
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("NovaTransacaoRejeitada: %v", err)
	}

	if e.Tipo() != TipoTransacaoRejeitada {
		t.Errorf("Tipo e %q", e.Tipo())
	}
	if got := deveCampo(t, e.Payload(), "failureCode"); got != "BET_SEM_SALDO" {
		t.Errorf("failureCode e %q, esperado %q", got, "BET_SEM_SALDO")
	}
	if got := deveCampo(t, e.Payload(), "status"); got != "REJECTED" {
		t.Errorf("status e %q", got)
	}
}

// O payload de WalletBalanceChanged e o mais exigente do enunciado: ele leva
// direcao, saldo anterior, saldo posterior e versao da carteira. Sem a versao o
// consumidor nao consegue detectar uma mudanca que ele nao viu.
func TestSaldoAlteradoTemTodosOsCamposDaMudanca(t *testing.T) {
	e, err := NovaSaldoAlterado(DadosSaldoAlterado{
		Transacao:      deveID(t, transacaoA),
		Carteira:       deveID(t, carteiraA),
		Direcao:        "DEBIT",
		Valor:          deveParse(t, "25.00"),
		SaldoAnterior:  deveParse(t, "100.00"),
		SaldoPosterior: deveParse(t, "75.00"),
		VersaoCarteira: 2,
		Correlacao:     "corr-123",
		OcorridoEm:     instanteFixo(),
	})
	if err != nil {
		t.Fatalf("NovaSaldoAlterado: %v", err)
	}

	if e.Tipo() != TipoSaldoAlterado {
		t.Errorf("Tipo e %q", e.Tipo())
	}

	payload := e.Payload()
	chaves := map[string]string{
		"walletId":      carteiraA,
		"direction":     "DEBIT",
		"money":         "25.00",
		"balanceBefore": "100.00",
		"balanceAfter":  "75.00",
	}
	for chave, esperado := range chaves {
		if got := deveCampo(t, payload, chave); got != esperado {
			t.Errorf("%s e %q, esperado %q", chave, got, esperado)
		}
	}

	var dados map[string]any
	if err := json.Unmarshal(payload, &dados); err != nil {
		t.Fatalf("payload nao e JSON: %v", err)
	}
	if versao, ok := dados["walletVersion"].(float64); !ok || versao != 2 {
		t.Errorf("walletVersion e %v, esperado 2", dados["walletVersion"])
	}
}

func TestPendenteReferenciaTemAReferenciaQueFalta(t *testing.T) {
	e, err := NovaPendenteReferencia(DadosPendenteReferencia{
		Transacao:  deveID(t, transacaoA),
		Provedor:   "provider-a",
		Externo:    "transaction-999",
		Carteira:   deveID(t, carteiraA),
		Tipo:       "ROLLBACK",
		Referencia: "transaction-123",
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("NovaPendenteReferencia: %v", err)
	}

	if e.Tipo() != TipoPendenteReferencia {
		t.Errorf("Tipo e %q", e.Tipo())
	}
	if got := deveCampo(t, e.Payload(), "referenceExternalTransactionId"); got != "transaction-123" {
		t.Errorf("referencia e %q", got)
	}
	if got := deveCampo(t, e.Payload(), "status"); got != "PENDING_REFERENCE" {
		t.Errorf("status e %q", got)
	}
}

// Cada construtor produz o seu proprio tipo. Um construtor que aceitasse o tipo
// como parametro permitiria um WagerTransactionProcessed com payload de rejeicao,
// e o consumidor processaria a rejeicao como sucesso.
func TestCadaConstrutorProduzOProprioTipo(t *testing.T) {
	processada, err := NovaTransacaoProcessada(DadosTransacaoProcessada{
		Transacao: deveID(t, transacaoA), Provedor: "p", Externo: "e",
		Carteira: deveID(t, carteiraA), Tipo: "BET", Resultado: deveParse(t, "1.00"),
		Correlacao: "c", OcorridoEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("processada: %v", err)
	}
	rejeitada, err := NovaTransacaoRejeitada(DadosTransacaoRejeitada{
		Transacao: deveID(t, transacaoA), Provedor: "p", Externo: "e",
		Carteira: deveID(t, carteiraA), Tipo: "BET", Valor: deveParse(t, "1.00"),
		Falha: "BET_SEM_SALDO", Correlacao: "c", OcorridoEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("rejeitada: %v", err)
	}

	if processada.Tipo() == rejeitada.Tipo() {
		t.Error("dois eventos diferentes sairam com o mesmo tipo")
	}
	if deveCampo(t, rejeitada.Payload(), "status") != "REJECTED" {
		t.Error("payload de rejeicao com status de sucesso")
	}
}

// Todos os construtores recusam dado invalido. Payload com campo faltando e o que
// faria o consumidor receber um evento sem informacao essencial.
func TestConstrutoresRecusamDadoInvalido(t *testing.T) {
	casos := map[string]func() error{
		"processada sem resultado": func() error {
			_, err := NovaTransacaoProcessada(DadosTransacaoProcessada{
				Transacao: deveID(t, transacaoA), Provedor: "p", Externo: "e",
				Carteira: deveID(t, carteiraA), Tipo: "BET",
				Correlacao: "c", OcorridoEm: instanteFixo(),
			})
			return err
		},
		"rejeitada sem codigo de falha": func() error {
			_, err := NovaTransacaoRejeitada(DadosTransacaoRejeitada{
				Transacao: deveID(t, transacaoA), Provedor: "p", Externo: "e",
				Carteira: deveID(t, carteiraA), Tipo: "BET", Valor: deveParse(t, "1.00"),
				Correlacao: "c", OcorridoEm: instanteFixo(),
			})
			return err
		},
		"saldo alterado com saldos incoerentes": func() error {
			_, err := NovaSaldoAlterado(DadosSaldoAlterado{
				Transacao: deveID(t, transacaoA), Carteira: deveID(t, carteiraA),
				Direcao: "DEBIT", Valor: deveParse(t, "25.00"),
				SaldoAnterior: deveParse(t, "100.00"), SaldoPosterior: deveParse(t, "10.00"),
				VersaoCarteira: 2, Correlacao: "c", OcorridoEm: instanteFixo(),
			})
			return err
		},
		"pendente sem referencia": func() error {
			_, err := NovaPendenteReferencia(DadosPendenteReferencia{
				Transacao: deveID(t, transacaoA), Provedor: "p", Externo: "e",
				Carteira: deveID(t, carteiraA), Tipo: "ROLLBACK",
				Correlacao: "c", OcorridoEm: instanteFixo(),
			})
			return err
		},
		"somente transacao sem agregado": func() error {
			_, err := NovaTransacaoProcessada(DadosTransacaoProcessada{
				Provedor: "p", Externo: "e", Carteira: deveID(t, carteiraA),
				Tipo: "BET", Resultado: deveParse(t, "1.00"),
				Correlacao: "c", OcorridoEm: instanteFixo(),
			})
			return err
		},
	}

	for nome, construir := range casos {
		t.Run(nome, func(t *testing.T) {
			if err := construir(); err == nil {
				t.Fatal("construtor aceitou dado invalido")
			}
		})
	}
}

// A direcao do evento de saldo tem de concordar com os saldos. E a mesma
// verificacao do lancamento, feita de novo na fronteira, porque o consumidor nao
// vai refazer.
func TestSaldoAlteradoExigeDirecaoCompativelComOsSaldos(t *testing.T) {
	credito := func() error {
		_, err := NovaSaldoAlterado(DadosSaldoAlterado{
			Transacao: deveID(t, transacaoA), Carteira: deveID(t, carteiraA),
			Direcao: "CREDIT", Valor: deveParse(t, "25.00"),
			SaldoAnterior: deveParse(t, "100.00"), SaldoPosterior: deveParse(t, "125.00"),
			VersaoCarteira: 2, Correlacao: "c", OcorridoEm: instanteFixo(),
		})
		return err
	}

	if err := credito(); err != nil {
		t.Fatalf("credito coerente recusado: %v", err)
	}
}

// Os eventos de saldo e de processada tem o mesmo agregado: a transacao. Se o
// agregado fosse a carteira, o consumidor nao conseguiria ordenar as operacoes de
// uma carteira.
func TestEventosDeOperacaoTemTransacaoComoAgregado(t *testing.T) {
	processada, err := NovaTransacaoProcessada(DadosTransacaoProcessada{
		Transacao: deveID(t, transacaoA), Provedor: "p", Externo: "e",
		Carteira: deveID(t, carteiraA), Tipo: "BET", Resultado: deveParse(t, "1.00"),
		Correlacao: "c", OcorridoEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("processada: %v", err)
	}

	if processada.AggregateID() != deveID(t, transacaoA) {
		t.Errorf("agregado e %s, esperado a transacao %s", processada.AggregateID(), transacaoA)
	}
	if deveCampo(t, processada.Payload(), "walletId") != carteiraA {
		t.Error("walletId ausente do payload")
	}
}

// Todos os eventos compartilham a mesma correlacao dentro de uma operacao. E o
// que permite reconstruir o fluxo no log e na auditoria.
func TestEventosDaMesmaOperacaoCompartilhamCorrelacao(t *testing.T) {
	processada, err := NovaTransacaoProcessada(DadosTransacaoProcessada{
		Transacao: deveID(t, transacaoA), Provedor: "p", Externo: "e",
		Carteira: deveID(t, carteiraA), Tipo: "BET", Resultado: deveParse(t, "75.00"),
		Correlacao: "corr-123", OcorridoEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("processada: %v", err)
	}

	saldo, err := NovaSaldoAlterado(DadosSaldoAlterado{
		Transacao: deveID(t, transacaoA), Carteira: deveID(t, carteiraA),
		Direcao: "DEBIT", Valor: deveParse(t, "25.00"),
		SaldoAnterior: deveParse(t, "100.00"), SaldoPosterior: deveParse(t, "75.00"),
		VersaoCarteira: 2, Correlacao: "corr-123", OcorridoEm: instanteFixo(),
		Causa: deveID(t, transacaoA),
	})
	if err != nil {
		t.Fatalf("saldo: %v", err)
	}

	if processada.Correlacao() != saldo.Correlacao() {
		t.Error("eventos da mesma operacao com correlacoes diferentes")
	}
	// A mudanca de saldo e causada pela transacao processada.
	if saldo.Causa() != deveID(t, transacaoA) {
		t.Errorf("causa e %s, esperado %s", saldo.Causa(), transacaoA)
	}
	if !saldo.TemCausa() {
		t.Error("evento com causa se diz sem causa")
	}
}

func TestEnvelopeSerializaTodosOsCamposDoContrato(t *testing.T) {
	e, err := NovaTransacaoProcessada(DadosTransacaoProcessada{
		Transacao: deveID(t, transacaoA), Provedor: "provider-a", Externo: "transaction-123",
		Carteira: deveID(t, carteiraA), Tipo: "BET", Resultado: deveParse(t, "975.00"),
		Correlacao: "corr-123", OcorridoEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("NovaTransacaoProcessada: %v", err)
	}

	serializado, err := e.Envelope()
	if err != nil {
		t.Fatalf("Envelope: %v", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal(serializado, &envelope); err != nil {
		t.Fatalf("envelope nao e JSON: %v", err)
	}

	obrigatorios := []string{"eventId", "eventType", "aggregateId", "correlationId", "occurredAt", "version", "data"}
	for _, chave := range obrigatorios {
		if _, ok := envelope[chave]; !ok {
			t.Errorf("envelope sem a chave %q: %s", chave, serializado)
		}
	}
	if envelope["eventType"] != string(TipoTransacaoProcessada) {
		t.Errorf("eventType e %v", envelope["eventType"])
	}
	if envelope["version"].(float64) != 1 {
		t.Errorf("version e %v, esperado 1", envelope["version"])
	}
	if envelope["occurredAt"].(string) != "2026-09-08T12:00:00Z" {
		t.Errorf("occurredAt e %v", envelope["occurredAt"])
	}
	// data e o payload tipado, e nao uma string.
	if _, ok := envelope["data"].(map[string]any); !ok {
		t.Errorf("data e %T, esperado objeto", envelope["data"])
	}
}
