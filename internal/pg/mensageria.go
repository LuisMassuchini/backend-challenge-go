package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/eventos"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// RepositorioInbox marca o que cada consumidor ja processou.
//
// A inbox e o que torna a entrega at-least-once segura. A mensagem pode ser
// entregue duas vezes, e a segunda entrega nao pode mover dinheiro de novo: e o que
// o registro da inbox garante, e ele e gravado na MESMA transacao do efeito.
type RepositorioInbox struct{}

// NovaRepositorioInbox constroi o repositorio.
func NovaRepositorioInbox() *RepositorioInbox { return &RepositorioInbox{} }

// Registrar marca a mensagem como recebida.
//
// INSERT ON CONFLICT DO NOTHING e proposital aqui, e e a unica vez em que ele
// aparece no projeto. Na inbox, "ja existia" nao e erro: e a resposta esperada
// para uma reentrega, e a transacao segue para devolver o resultado persistido. O
// contraste com a transacao de apostas e proposital -- ali conflito e erro, aqui
// conflito e o caso comum do at-least-once.
func (r RepositorioInbox) Registrar(
	ctx context.Context,
	q Querente,
	consumidor string,
	mensagemID string,
	hash string,
	agora time.Time,
) error {
	if consumidor == "" || mensagemID == "" {
		return fmt.Errorf("pg: consumidor ou mensagem ausente")
	}

	_, err := q.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, message_hash, received_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		consumidor, mensagemID, hash, agora,
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: registro na inbox: %w", err))
	}
	return nil
}

// Concluir marca a mensagem como processada com sucesso.
//
// completed_at preenchido significa 'esta mensagem ja produziu efeito'. A coluna
// existe separada de received_at porque o intervalo entre as duas e a medida de
// quanto trabalho deu trabalho -- e porque um consumidor que died no meio deixa a
// linha com received_at e sem completed_at, que e o estado de reentrega.
func (r RepositorioInbox) Concluir(
	ctx context.Context,
	q Querente,
	consumidor string,
	mensagemID string,
	agora time.Time,
) error {
	tag, err := q.Exec(ctx, `
		UPDATE inbox_messages
		   SET completed_at = $3
		 WHERE consumer_name = $1 AND message_id = $2`,
		consumidor, mensagemID, agora,
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: conclusao na inbox: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: mensagem %s do consumidor %s", ErrNaoEncontrado, mensagemID, consumidor)
	}
	return nil
}

// JaConcluida informa se a mensagem ja produziu efeito.
//
// E a consulta que o consumidor faz antes de reprocessar: se a resposta e sim, o
// caminho e o replay, que devolve o resultado persistido sem tocar em dinheiro.
func (r RepositorioInbox) JaConcluida(
	ctx context.Context,
	q Querente,
	consumidor string,
	mensagemID string,
) (bool, error) {
	var concluida *time.Time
	linha := q.QueryRow(ctx, `
		SELECT completed_at FROM inbox_messages
		 WHERE consumer_name = $1 AND message_id = $2`,
		consumidor, mensagemID)

	if err := linha.Scan(&concluida); err != nil {
		if isNenhumaLinha(err) {
			return false, nil
		}
		return false, fmt.Errorf("pg: leitura da inbox: %w", err)
	}
	return concluida != nil, nil
}

// RepositorioOutbox e a fila de publicacao.
type RepositorioOutbox struct{}

// NovaRepositorioOutbox constroi o repositorio.
func NovaRepositorioOutbox() *RepositorioOutbox { return &RepositorioOutbox{} }

// Inserir grava o evento para publicacao.
//
// E gravado na MESMA transacao que a alteracao financeira. E o que garante a
// propriedade de que nenhum evento e publicado antes do commit que o originou: se o
// registro nao existe ate a transacao confirmar, nao ha como publicar antes.
func (r RepositorioOutbox) Inserir(ctx context.Context, q Querente, e eventos.Evento) error {
	if !e.Valida() {
		return fmt.Errorf("pg: evento invalido")
	}

	envelope, err := e.Envelope()
	if err != nil {
		return fmt.Errorf("pg: serializacao do envelope: %w", err)
	}
	var campos map[string]any
	if err := json.Unmarshal(envelope, &campos); err != nil {
		return fmt.Errorf("pg: envelope invalido: %w", err)
	}

	causa, temCausa := campos["causationId"].(string)
	var causaUUID *uuid.UUID
	if temCausa && causa != "" {
		analisada, err := uuid.Parse(causa)
		if err != nil {
			return fmt.Errorf("pg: causationId invalido: %w", err)
		}
		causaUUID = &analisada
	}

	var (
		eventType = string(e.Tipo())
		agregado  = e.AggregateID().UUID()
		versao    = e.Versao()
	)

	if _, err := q.Exec(ctx, `
		INSERT INTO outbox_events (
			id, aggregate_id, event_type, payload, correlation_id, causation_id,
			version, occurred_at, attempts, next_attempt_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, $8)`,
		e.EventID().UUID(), agregado, eventType, e.Payload(),
		e.Correlacao(), causaUUID, versao, e.OcorreuEm(),
	); err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: insercao na outbox: %w", err))
	}
	return nil
}

// RegistroPendente e um evento que um publisher reservou para publicar.
type RegistroPendente struct {
	Evento       eventos.Evento
	Tentativas   int
	ReservadoAte time.Time
}

// Reservar marca os eventos pendentes como pertencentes a este publisher.
//
// E o ponto em que varios publishers sao seguros: FOR UPDATE SKIP LOCKED faz cada
// publisher pegar um conjunto diferente sem esperar o anterior publicar. Com
// espera -- FOR UPDATE sem SKIP LOCKED -- um publisher lento serializaria todos os
// outros, que e o oposto do que se quer de um relay.
//
// leased_until e a janela em que a reserva vale. Sem ela, um publisher que morreu
// depois de reservar deixa o registro preso para sempre; com ela, outro publisher
// assume depois do vencimento. E o que responde 'recuperacao de trabalho
// abandonado' do enunciado.
func (r RepositorioOutbox) Reservar(
	ctx context.Context,
	q Querente,
	editor string,
	limite int,
	janela time.Duration,
	agora time.Time,
) ([]RegistroPendente, error) {
	if limite <= 0 {
		limite = 50
	}
	if janela <= 0 {
		janela = 30 * time.Second
	}
	ate := agora.Add(janela)

	linhas, err := q.Query(ctx, `
		SELECT id, aggregate_id, event_type, payload, correlation_id, causation_id,
		       version, occurred_at, attempts
		  FROM outbox_events
		 WHERE published_at IS NULL
		   AND next_attempt_at <= $1
		   AND (leased_until IS NULL OR leased_until < $1)
		 ORDER BY occurred_at, id
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`,
		agora, limite,
	)
	if err != nil {
		return nil, fmt.Errorf("pg: reserva da outbox: %w", err)
	}
	defer linhas.Close()

	reservados := []RegistroPendente{}
	for linhas.Next() {
		var (
			id, agregado    uuid.UUID
			causa           *uuid.UUID
			tipo, corelacao string
			payload         []byte
			versao          int
			ocorridoEm      time.Time
			tentativas      int
		)
		if err := linhas.Scan(&id, &agregado, &tipo, &payload, &corelacao, &causa,
			&versao, &ocorridoEm, &tentativas); err != nil {
			return nil, fmt.Errorf("pg: varredura da outbox: %w", err)
		}

		evento, err := restaurarEvento(id, agregado, tipo, payload, corelacao, causa, versao, ocorridoEm)
		if err != nil {
			return nil, err
		}
		reservados = append(reservados, RegistroPendente{
			Evento:       evento,
			Tentativas:   tentativas,
			ReservadoAte: ate,
		})
	}
	if err := linhas.Err(); err != nil {
		return nil, fmt.Errorf("pg: varredura da outbox: %w", err)
	}

	if len(reservados) == 0 {
		return reservados, nil
	}

	ids := make([]uuid.UUID, 0, len(reservados))
	for _, r := range reservados {
		ids = append(ids, r.Evento.EventID().UUID())
	}

	tag, err := q.Exec(ctx, `
		UPDATE outbox_events
		   SET leased_until = $2, leased_by = $3
		 WHERE id = ANY($1)`,
		ids, ate, editor,
	)
	if err != nil {
		return nil, classificarErroDeEscrita(err, fmt.Errorf("pg: marca da reserva: %w", err))
	}
	if tag.RowsAffected() != int64(len(reservados)) {
		// Alguem reservou entre o SELECT e o UPDATE. Nao e erro: o registro ja
		// tem dono e sera publicado por ele.
		return nil, fmt.Errorf("%w: %d de %d reservados", ErrConflitoDeReserva, tag.RowsAffected(), len(reservados))
	}

	return reservados, nil
}

// ConfirmarPublicacao marca o evento como publicado.
//
// O registro so e confirmado DEPOIS da publicacao, nunca antes: confirmar antes e
// o caminho para perder o evento, e perder um evento cujo registro foi confirmado
// no banco e eliminatorio no enunciado.
func (r RepositorioOutbox) ConfirmarPublicacao(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
	agora time.Time,
) error {
	tag, err := q.Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = $2, leased_until = NULL, leased_by = NULL
		 WHERE id = $1 AND published_at IS NULL`,
		id.UUID(), agora,
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: confirmacao da publicacao: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: evento %s", ErrNaoEncontrado, id)
	}
	return nil
}

// Reprogramar devolve o evento para a fila com backoff.
//
// O registro nao e apagado nem marcado como falha permanente: a proxima tentativa
// e sempre a mesma, com o prazo maior. E o que faz o relay recuperar de uma
// indisponibilidade temporaria do broker sem perder o evento.
func (r RepositorioOutbox) Reprogramar(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
	proximaTentativa time.Time,
) error {
	tag, err := q.Exec(ctx, `
		UPDATE outbox_events
		   SET attempts = attempts + 1,
		       next_attempt_at = $2,
		       leased_until = NULL,
		       leased_by = NULL
		 WHERE id = $1`,
		id.UUID(), proximaTentativa,
	)
	if err != nil {
		return classificarErroDeEscrita(err, fmt.Errorf("pg: reprogramacao: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: evento %s", ErrNaoEncontrado, id)
	}
	return nil
}

// restaurarEvento reconstitui o evento imutavel a partir do registro da outbox.
//
// restaurarEvento reconstitui o evento imutavel a partir do registro da outbox.
//
// O eventId vem do registro, e nao e gerado aqui: e ele que precisa sobreviver a um
// relay que publicou e morreu antes de confirmar, para que o consumidor reconheca
// que a republicacao e o mesmo evento.
func restaurarEvento(
	id, agregado uuid.UUID,
	tipo string,
	payload []byte,
	correlacao string,
	causa *uuid.UUID,
	versao int,
	ocorridoEm time.Time,
) (eventos.Evento, error) {
	identificador, err := wallet.IdentificadorDe(formatarUUID(id))
	if err != nil {
		return eventos.Evento{}, fmt.Errorf("pg: id de evento invalido no banco: %w", err)
	}
	agregadoID, err := wallet.IdentificadorDe(formatarUUID(agregado))
	if err != nil {
		return eventos.Evento{}, fmt.Errorf("pg: agregado invalido no banco: %w", err)
	}

	var causaID wallet.Identificador
	if causa != nil {
		causaID, err = wallet.IdentificadorDe(formatarUUID(*causa))
		if err != nil {
			return eventos.Evento{}, fmt.Errorf("pg: causa invalida no banco: %w", err)
		}
	}

	return eventos.Restaurar(identificador, eventos.Tipo(tipo), agregadoID, correlacao,
		causaID, ocorridoEm, versao, payload)
}
