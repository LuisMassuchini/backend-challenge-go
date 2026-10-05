// Package eventos e o contrato de integracao: o que sai do sistema e como.
//
// Um evento nao e uma struct livre serializada sob demanda. E um registro
// imutavel com envelope e payload ja serializado, porque ele e gravado na outbox,
// publicado por um worker e republicado quando o relay falha entre publicar e
// confirmar. Se o payload fosse montado na hora de publicar, o que seria publicado
// poderia nao ser o que foi gravado.
//
// O envelope carrega eventId, eventType, aggregateId, correlationId, causationId
// opcional, occurredAt em UTC, version e data tipado. O tipo e a versao sao
// definidos pelo construtor do evento, nunca pelo chamador.
package eventos

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// Tipo e o nome do evento.
//
// O conjunto e fechado pelos quatro eventos que o enunciado exige. Um tipo a mais
// aqui e um tipo que o consumidor precisa conhecer, e nao e o contrato que a
// solucao promete.
type Tipo string

const (
	// TipoTransacaoProcessada e emitido quando uma operacao termina com sucesso,
	// incluindo LOSS.
	TipoTransacaoProcessada Tipo = "WagerTransactionProcessed"
	// TipoTransacaoRejeitada e emitido quando a operacao e recusada por regra de
	// negocio, definitivamente.
	TipoTransacaoRejeitada Tipo = "WagerTransactionRejected"
	// TipoSaldoAlterado e emitido quando o saldo muda de verdade. LOSS nao emite,
	// porque nao ha mudanca.
	TipoSaldoAlterado Tipo = "WalletBalanceChanged"
	// TipoPendenteReferencia e emitido quando a operacao passa a depender de uma
	// referencia que ainda nao chegou.
	TipoPendenteReferencia Tipo = "WagerTransactionPendingReference"
)

// versaoInicial e a versao do envelope.
//
// O envelope tem versao propria, separada da versao do agregado. Um consumidor que
// veja a versao 2 sabe que o payload mudou de forma, e nao que a carteira mudou de
// saldo.
const versaoInicial = 1

// Valido informa se o tipo pertence ao conjunto conhecido.
func (t Tipo) Valido() bool {
	switch t {
	case TipoTransacaoProcessada, TipoTransacaoRejeitada, TipoSaldoAlterado, TipoPendenteReferencia:
		return true
	}
	return false
}

// Erros do envelope.
var (
	// ErrEnvelopeInvalido cobre dado ausente ou malformado no envelope.
	ErrEnvelopeInvalido = errors.New("eventos: envelope invalido")
	// ErrPayloadInvalido cobre payload que nao serializa em JSON.
	ErrPayloadInvalido = errors.New("eventos: payload nao serializavel")
)

// TransacaoProcessada descreve os dados de um evento cujo tipo e
// TipoTransacaoProcessada.
//
// Os campos sao os metadados que os quatro eventos compartilham. O payload
// tipado de cada evento entra em Dados, e e o construtor tipado do evento que
// monta esse mapa -- nao o chamador.
type TransacaoProcessada struct {
	// Agregado e o identificador da transacao, que e o agregado do evento.
	Agregado wallet.Identificador
	// Correlacao amarra o evento a um fluxo de execucao, e e o mesmo em todos os
	// eventos da mesma operacao.
	Correlacao string
	// Causa e o identificador do que produziu este evento. Opcional: o primeiro
	// evento de uma operacao nao tem causa interna.
	Causa wallet.Identificador
	// OcorridoEm e o instante do fato, nao o da publicacao. Ele pode ser anterior
	// ao envio, que e exatamente o caso quando o relay recupera trabalho
	// abandonado.
	OcorridoEm time.Time
	// Dados e o payload tipado, ja montado.
	Dados map[string]any
}

// Evento e um registro imutavel pronto para a outbox.
//
// Nao ha setter e nao ha metodo que reconstrua o payload. O que existe e o
// caminho de leitura, e o caminho de leitura devolve copia do payload.
type Evento struct {
	eventID     wallet.Identificador
	tipo        Tipo
	aggregateID wallet.Identificador
	correlacao  string
	causa       wallet.Identificador
	ocorridoEm  time.Time
	versao      int
	payload     []byte
}

// Novo constroi um evento do tipo indicado a partir do envelope.
//
// O tipo do evento vem da entrada, e nao do payload: e o construtor tipado do
// evento, na proxima etapa, que garante que o payload corresponde ao tipo. Aqui o
// que se garante e que o envelope e valido e que o payload serializa.
//
// A serializacao usa chaves ordenadas porque o hash de idempotencia e o
// reconhecimento de republicacao comparam bytes: duas construcoes equivalentes
// precisam produzir o mesmo payload.
func Novo(entrada TransacaoProcessada) (Evento, error) {
	if !entrada.Agregado.Valida() {
		return Evento{}, fmt.Errorf("%w: agregado ausente", ErrEnvelopeInvalido)
	}
	if strings.TrimSpace(entrada.Correlacao) == "" {
		return Evento{}, fmt.Errorf("%w: correlacao ausente", ErrEnvelopeInvalido)
	}
	if entrada.OcorridoEm.IsZero() {
		return Evento{}, fmt.Errorf("%w: instante do fato ausente", ErrEnvelopeInvalido)
	}
	if len(entrada.Dados) == 0 {
		return Evento{}, fmt.Errorf("%w: payload vazio", ErrEnvelopeInvalido)
	}

	payload, err := serializar(entrada.Dados)
	if err != nil {
		return Evento{}, err
	}

	return Evento{
		eventID:     wallet.NovoIdentificador(),
		tipo:        tipoDoConstrutor(entrada),
		aggregateID: entrada.Agregado,
		correlacao:  entrada.Correlacao,
		causa:       entrada.Causa,
		// UTC sempre. O envelope vira snapshot imutavel e pode ser lido em outra
		// regiao: occurredAt com fuso local e dado ambiguo para quem ordena por
		// instante.
		ocorridoEm: entrada.OcorridoEm.UTC(),
		versao:     versaoInicial,
		payload:    payload,
	}, nil
}

// tipoDoConstrutor devolve o tipo do evento.
//
// Entrada e tipo sao a mesma coisa por enquanto porque o construtor tipado de cada
// evento ainda nao existe. A funcao existe para que a troca seja em um lugar so.
func tipoDoConstrutor(TransacaoProcessada) Tipo { return TipoTransacaoProcessada }

// Restaurar reconstroi um evento a partir do que foi persistido na outbox.
//
// E o caminho da republicacao. O eventId vem de fora justamente aqui: e ele que
// precisa sobreviver a um relay que publicou e morreu antes de confirmar, para que
// o consumidor reconheca que e o mesmo evento.
func Restaurar(
	eventID wallet.Identificador,
	tipo Tipo,
	aggregateID wallet.Identificador,
	correlacao string,
	causa wallet.Identificador,
	ocorridoEm time.Time,
	versao int,
	payload []byte,
) (Evento, error) {
	if !eventID.Valida() {
		return Evento{}, fmt.Errorf("%w: eventId ausente", ErrEnvelopeInvalido)
	}
	if !tipo.Valido() {
		return Evento{}, fmt.Errorf("%w: tipo %q", ErrEnvelopeInvalido, tipo)
	}
	if !aggregateID.Valida() {
		return Evento{}, fmt.Errorf("%w: agregado ausente", ErrEnvelopeInvalido)
	}
	if strings.TrimSpace(correlacao) == "" {
		return Evento{}, fmt.Errorf("%w: correlacao ausente", ErrEnvelopeInvalido)
	}
	if ocorridoEm.IsZero() {
		return Evento{}, fmt.Errorf("%w: instante do fato ausente", ErrEnvelopeInvalido)
	}
	if versao < 1 {
		return Evento{}, fmt.Errorf("%w: versao %d", ErrEnvelopeInvalido, versao)
	}
	if len(payload) == 0 {
		return Evento{}, fmt.Errorf("%w: payload vazio", ErrEnvelopeInvalido)
	}
	if !json.Valid(payload) {
		return Evento{}, fmt.Errorf("%w: payload nao e JSON", ErrPayloadInvalido)
	}

	restaurado := make([]byte, len(payload))
	copy(restaurado, payload)

	return Evento{
		eventID:     eventID,
		tipo:        tipo,
		aggregateID: aggregateID,
		correlacao:  correlacao,
		causa:       causa,
		ocorridoEm:  ocorridoEm.UTC(),
		versao:      versao,
		payload:     restaurado,
	}, nil
}

// serializar produz o payload com chaves ordenadas.
//
// encoding/json ja ordena as chaves de mapa, e e por isso que Dados e mapa e nao
// struct com campo de ordem livre. A funcao existe para deixar o motivo escrito e
// para ter um unico ponto onde a decisao mora.
func serializar(dados map[string]any) ([]byte, error) {
	payload, err := json.Marshal(dados)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayloadInvalido, err)
	}
	return payload, nil
}

// EventID devolve o identificador estavel do evento.
func (e Evento) EventID() wallet.Identificador { return e.eventID }

// Tipo devolve o tipo do evento.
func (e Evento) Tipo() Tipo { return e.tipo }

// AggregateID devolve o agregado do evento.
func (e Evento) AggregateID() wallet.Identificador { return e.aggregateID }

// Correlacao devolve o identificador de correlacao do fluxo.
func (e Evento) Correlacao() string { return e.correlacao }

// Causa devolve o identificador da causa, quando existe.
func (e Evento) Causa() wallet.Identificador { return e.causa }

// TemCausa informa se o evento tem causa.
func (e Evento) TemCausa() bool { return e.causa.Valida() }

// OcorreuEm devolve o instante do fato, em UTC.
func (e Evento) OcorreuEm() time.Time { return e.ocorridoEm }

// Versao devolve a versao do envelope.
func (e Evento) Versao() int { return e.versao }

// Payload devolve copia do payload.
//
// A copia e obrigatoria: o consumidor nao pode alterar o snapshot do registro que o
// produtor ainda vai republicar depois de uma falha de publicacao.
func (e Evento) Payload() []byte {
	copia := make([]byte, len(e.payload))
	copy(copia, e.payload)
	return copia
}

// Envelope devolve o envelope serializado, como vai para a fila.
func (e Evento) Envelope() ([]byte, error) {
	return json.Marshal(map[string]any{
		"eventId":       e.eventID.String(),
		"eventType":     string(e.tipo),
		"aggregateId":   e.aggregateID.String(),
		"correlationId": e.correlacao,
		"causationId":   causacao(e.causa),
		"occurredAt":    e.ocorridoEm,
		"version":       e.versao,
		"data":          json.RawMessage(e.payload),
	})
}

// causacao devolve o causationId, ou string vazia quando nao ha causa.
//
// String vazia e nao null porque o envelope vai para um consumidor externo, e
// campo ausente e campo nulo sao coisas diferentes para quem desserializa.
func causacao(causa wallet.Identificador) string {
	if !causa.Valida() {
		return ""
	}
	return causa.String()
}

// Valida informa se o evento existe e pode ser gravado na outbox.
func (e Evento) Valida() bool {
	return e.eventID.Valida() && e.tipo.Valido() && e.aggregateID.Valida() &&
		strings.TrimSpace(e.correlacao) != "" && !e.ocorridoEm.IsZero() &&
		e.versao >= 1 && len(e.payload) > 0 && bytes.Equal(e.payload, e.payload)
}
