package eventos

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// O envelope e o contrato de integracao. Tudo o que o enunciado exige dele esta
// verificado aqui: eventId, eventType, aggregateId, correlationId, causationId
// opcional, occurredAt em UTC, version e data tipado.

const (
	carteiraA  = "0192f291-27dd-7d3f-8071-5f8685deef37"
	transacaoA = "0192f298-345e-7e38-af88-e43f851a819d"
)

func instanteFixo() time.Time {
	return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
}

func deveID(t *testing.T, texto string) wallet.Identificador {
	t.Helper()
	id, err := wallet.IdentificadorDe(texto)
	if err != nil {
		t.Fatalf("wallet.IdentificadorDe(%q): %v", texto, err)
	}
	return id
}

// instanteComFuso devolve o mesmo instante escrito em outro fuso. E o teste que
// garante que occurredAt sai em UTC mesmo quando o processo roda em Sao Paulo.
func instanteComFuso() time.Time {
	fuso := time.FixedZone("BRT", -3*60*60)
	return time.Date(2026, 9, 8, 9, 0, 0, 0, fuso)
}

func TestEnvelopeTemTodosOsCamposDoContrato(t *testing.T) {
	e, err := Novo(TransacaoProcessada{
		Agregado:   deveID(t, transacaoA),
		Correlacao: "corr-123",
		Causa:      deveID(t, carteiraA),
		OcorridoEm: instanteFixo(),
		Dados:      map[string]any{"transactionId": transacaoA},
	})
	if err != nil {
		t.Fatalf("Novo: %v", err)
	}

	if !e.EventID().Valida() {
		t.Error("evento sem eventId valido")
	}
	if e.Tipo() != TipoTransacaoProcessada {
		t.Errorf("Tipo e %q, esperado %q", e.Tipo(), TipoTransacaoProcessada)
	}
	if e.AggregateID() != deveID(t, transacaoA) {
		t.Errorf("AggregateID e %s, esperado %s", e.AggregateID(), transacaoA)
	}
	if e.Correlacao() != "corr-123" {
		t.Errorf("Correlacao e %q, esperado %q", e.Correlacao(), "corr-123")
	}
	if e.Causa() != deveID(t, carteiraA) {
		t.Errorf("Causa e %s, esperado %s", e.Causa(), carteiraA)
	}
	if e.Versao() != 1 {
		t.Errorf("Versao e %d, esperado 1", e.Versao())
	}
	if !e.OcorreuEm().Equal(instanteFixo()) {
		t.Errorf("OcorreuEm e %v", e.OcorreuEm())
	}
}

// occurredAt e UTC em RFC 3339. O envelope vira snapshot imutavel na outbox e pode
// ser lido em outra regiao: um occurredAt com fuso local vira dado ambiguo para o
// consumidor que ordena por instante.
func TestOcorreuEmSempreEmUTC(t *testing.T) {
	e, err := Novo(TransacaoProcessada{
		Agregado:   deveID(t, transacaoA),
		Correlacao: "corr-123",
		OcorridoEm: instanteComFuso(),
		Dados:      map[string]any{"transactionId": transacaoA},
	})
	if err != nil {
		t.Fatalf("Novo: %v", err)
	}

	if e.OcorreuEm().Location() != time.UTC {
		t.Errorf("Localizacao e %v, esperado UTC", e.OcorreuEm().Location())
	}
	if !e.OcorreuEm().Equal(instanteFixo()) {
		t.Errorf("instante e %v, esperado %v: o mesmo momento em UTC", e.OcorreuEm(), instanteFixo())
	}

	texto, err := json.Marshal(e.OcorreuEm())
	if err != nil {
		t.Fatalf("Marshal do instante: %v", err)
	}
	if got := string(texto); got != `"2026-09-08T12:00:00Z"` {
		t.Errorf("serializacao e %s, esperado %q", got, `"2026-09-08T12:00:00Z"`)
	}
}

// causationId e opcional: um evento pode ser caused by nada, que e o caso do
// primeiro evento de uma operacao.
func TestCausaEOpcional(t *testing.T) {
	e, err := Novo(TransacaoProcessada{
		Agregado:   deveID(t, transacaoA),
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
		Dados:      map[string]any{"transactionId": transacaoA},
	})
	if err != nil {
		t.Fatalf("Novo: %v", err)
	}

	if e.TemCausa() {
		t.Error("evento sem causa se diz com causa")
	}
	if e.Causa().Valida() {
		t.Errorf("Causa e %s, esperado identificador invalido", e.Causa())
	}
}

// O payload e um snapshot imutavel. O mapa que o chamador passou nao pode mudar
// depois: o evento ja foi para a outbox, e um payload alterado depois da escrita
// faz o consumidor ver algo que o produtor nao gravou.
func TestPayloadEUmSnapshotImutavel(t *testing.T) {
	dados := map[string]any{"transactionId": transacaoA, "status": "PROCESSED"}

	e, err := Novo(TransacaoProcessada{
		Agregado:   deveID(t, transacaoA),
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
		Dados:      dados,
	})
	if err != nil {
		t.Fatalf("Novo: %v", err)
	}

	antes := string(e.Payload())

	dados["status"] = "REJECTED"
	dados["extra"] = "invasor"

	if depois := string(e.Payload()); depois != antes {
		t.Errorf("payload mudou depois da construcao:\n antes: %s\n depois: %s", antes, depois)
	}
}

// Devolver o payload como []byte sem copia permitiria que o consumidor alterasse o
// snapshot do registro que o produtor ainda vai republicar.
func TestPayloadDevolveCopia(t *testing.T) {
	e, err := Novo(TransacaoProcessada{
		Agregado:   deveID(t, transacaoA),
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
		Dados:      map[string]any{"status": "PROCESSED"},
	})
	if err != nil {
		t.Fatalf("Novo: %v", err)
	}

	primeira := e.Payload()
	primeira[0] = 'X'

	if segunda := string(e.Payload()); strings.HasPrefix(segunda, "X") {
		t.Error("alterar o slice devolvido alterou o payload do evento")
	}
}

// O eventId e gerado na construcao e nao na publicacao. O relay pode publicar o
// mesmo registro da outbox mais de uma vez -- depois de publicar e antes de
// confirmar -- e o consumidor precisa reconhecer que e o mesmo evento.
func TestEventIDEEstableEntrePublicacoes(t *testing.T) {
	e, err := Novo(TransacaoProcessada{
		Agregado:   deveID(t, transacaoA),
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
		Dados:      map[string]any{"status": "PROCESSED"},
	})
	if err != nil {
		t.Fatalf("Novo: %v", err)
	}

	// Republicar e reconstruir o registro a partir do que foi persistido.
	restaurado, err := Restaurar(e.EventID(), e.Tipo(), e.AggregateID(), e.Correlacao(), e.Causa(), e.OcorreuEm(), e.Versao(), e.Payload())
	if err != nil {
		t.Fatalf("Restaurar: %v", err)
	}

	if restaurado.EventID() != e.EventID() {
		t.Errorf("eventId mudou na republicacao: %s virou %s", e.EventID(), restaurado.EventID())
	}
	if string(restaurado.Payload()) != string(e.Payload()) {
		t.Error("payload mudou na republicacao")
	}
}

// Um envelope com o mesmo conteudo produz um eventId diferente. Sao duas operacoes
// distintas, e o consumidor precisa poder contar as duas.
func TestEventIDEDiferenteParaOperacoesDistintas(t *testing.T) {
	novo := func() Evento {
		t.Helper()
		e, err := Novo(TransacaoProcessada{
			Agregado:   deveID(t, transacaoA),
			Correlacao: "corr-123",
			OcorridoEm: instanteFixo(),
			Dados:      map[string]any{"status": "PROCESSED"},
		})
		if err != nil {
			t.Fatalf("Novo: %v", err)
		}
		return e
	}

	if novo().EventID() == novo().EventID() {
		t.Error("dois eventos recebeu o mesmo eventId")
	}
}

// O envelope e o unico formato aceito pela outbox. Um registro sem tipo conhecido
// entraria na fila e o consumidor nao saberia o que fazer com ele.
func TestTipoForaDoConjuntoRecusa(t *testing.T) {
	invalidos := []string{"", "WagerTransactionQualquer", "wallet_balance_changed"}

	for _, tipo := range invalidos {
		t.Run(tipo, func(t *testing.T) {
			if Tipo(tipo).Valido() {
				t.Errorf("tipo %q fora do conjunto se diz valido", tipo)
			}
		})
	}

	validos := []Tipo{
		TipoTransacaoProcessada,
		TipoTransacaoRejeitada,
		TipoSaldoAlterado,
		TipoPendenteReferencia,
	}
	for _, tipo := range validos {
		if !tipo.Valido() {
			t.Errorf("tipo %q do conjunto se diz invalido", tipo)
		}
	}
}

func TestEnvelopeRecusaDadosInvalidos(t *testing.T) {
	casos := map[string]TransacaoProcessada{
		"sem agregado":   {Correlacao: "corr-123", OcorridoEm: instanteFixo(), Dados: map[string]any{"transactionId": transacaoA}},
		"sem correlacao": {Agregado: deveID(t, transacaoA), OcorridoEm: instanteFixo(), Dados: map[string]any{"transactionId": transacaoA}},
		"correlacao com espaco": {
			Agregado:   deveID(t, transacaoA),
			Correlacao: "  ",
			OcorridoEm: instanteFixo(),
			Dados:      map[string]any{"transactionId": transacaoA},
		},
		"sem instante": {Agregado: deveID(t, transacaoA), Correlacao: "corr-123", Dados: map[string]any{"transactionId": transacaoA}},
		"sem dados":    {Agregado: deveID(t, transacaoA), Correlacao: "corr-123", OcorridoEm: instanteFixo()},
	}

	for nome, entrada := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := Novo(entrada); err == nil {
				t.Fatal("envelope com dado invalido foi aceito")
			}
		})
	}
}

// O payload tem de ser JSON valido. Um payload que nao serializa fica na outbox e
// falha no relay, ou seja, no pior lugar possivel para descobrir.
func TestDadosQueNaoSerializamRecusa(t *testing.T) {
	_, err := Novo(TransacaoProcessada{
		Agregado:   deveID(t, transacaoA),
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
		Dados:      map[string]any{"funcao": func() {}},
	})
	if err == nil {
		t.Fatal("payload nao serializavel foi aceito")
	}
	if !errors.Is(err, ErrPayloadInvalido) {
		t.Errorf("devolveu %v, esperado ErrPayloadInvalido", err)
	}
}

// As chaves do payload sao ordenadas na serializacao. O hash de idempotencia e o
// dedupe de republicacao dependem do payload ser byte a byte igual entre duas
// construcoes equivalentes.
func TestPayloadComChavesOrdenadas(t *testing.T) {
	e, err := Novo(TransacaoProcessada{
		Agregado:   deveID(t, transacaoA),
		Correlacao: "corr-123",
		OcorridoEm: instanteFixo(),
		Dados:      map[string]any{"zeta": 1, "alfa": 2, "meio": 3},
	})
	if err != nil {
		t.Fatalf("Novo: %v", err)
	}

	payload := string(e.Payload())
	if got := strings.Index(payload, "alfa"); got > strings.Index(payload, "meio") || got > strings.Index(payload, "zeta") {
		t.Errorf("payload nao esta com chaves ordenadas: %s", payload)
	}
}

func TestValorZeroDeEventoNaoEValido(t *testing.T) {
	var e Evento
	if e.Valida() {
		t.Fatal("Evento nao inicializado se diz valido")
	}
	if e.EventID().Valida() {
		t.Error("Evento nao inicializado tem eventId")
	}
	if e.Tipo() != Tipo("") {
		t.Errorf("Tipo e %q, esperado vazio", e.Tipo())
	}
}
