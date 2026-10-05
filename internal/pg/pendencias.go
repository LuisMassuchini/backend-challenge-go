package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// Pendencia e uma transacao esperando a referencia que a desfaz.
//
// E um tipo do repositorio e nao do agregado porque backoff e expiracao sao politica
// de infraestrutura. O dominio sabe que uma reversao depende de outra operacao; ele
// nao sabe nem deve saber quantas vezes alguem tentou descobrir a referencia.
type Pendencia struct {
	// TransacaoID e a transacao pendente.
	TransacaoID wallet.Identificador

	// Provedor e o provedor que enviou a reversao.
	Provedor wagering.Provedor

	// ReferenciaExterna e a operacao externa que a reversao espera.
	ReferenciaExterna wagering.Externo

	// Tentativas e quantas vezes a retomada ja foi tentada.
	Tentativas int

	// ProximaEm e quando a proxima tentativa pode acontecer.
	ProximaEm time.Time
}

// ClaimPendencias reserva um lote de pendencias prontas.
//
// O `FOR UPDATE SKIP LOCKED` e o que permite tres instancias rodando este worker sem
// coordenacao e sem processar a mesma pendencia duas vezes. A alternativa, um lock
// global, serializaria as instancias e transformaria uma etapa de baixa contencao em
// um gargalo; e a segunda, permitir a mesma pendencia em duas instancias, faria as
// duas tentarem aplicar o mesmo estorno.
//
// A reserva e por lote e nao por pendencia para que o custo do lock nao seja por
// item: um `SELECT` por pendencia seria a mesma quantidade de idas ao banco.
func (r RepositorioTransacoes) ClaimPendencias(
	ctx context.Context,
	q Querente,
	agora time.Time,
	limite int,
) ([]Pendencia, error) {
	if limite <= 0 {
		limite = 10
	}

	linhas, err := q.Query(ctx, `
		SELECT id, provider_id, reference_external_id, retry_count
		  FROM wager_transactions
		 WHERE state = 'PENDING_REFERENCE'
		   AND next_retry_at <= $1
		 ORDER BY next_retry_at, id
		 LIMIT $2
		 FOR UPDATE SKIP LOCKED`,
		agora, limite)
	if err != nil {
		return nil, fmt.Errorf("pg: leitura das pendencias: %w", err)
	}
	defer linhas.Close()

	var pendencias []Pendencia
	for linhas.Next() {
		var (
			idBruto    [16]byte
			provedor   string
			externa    *string
			tentativas int
		)
		if err := linhas.Scan(&idBruto, &provedor, &externa, &tentativas); err != nil {
			return nil, fmt.Errorf("pg: varredura das pendencias: %w", err)
		}

		id, err := wallet.IdentificadorDe(formatarUUID(idBruto))
		if err != nil {
			return nil, fmt.Errorf("pg: id de pendencia invalido no banco: %w", err)
		}

		pendencias = append(pendencias, Pendencia{
			TransacaoID:       id,
			Provedor:          wagering.Provedor(provedor),
			ReferenciaExterna: wagering.Externo(awsTexto(externa)),
			Tentativas:        tentativas,
			ProximaEm:         agora,
		})
	}
	if err := linhas.Err(); err != nil {
		return nil, fmt.Errorf("pg: varredura das pendencias: %w", err)
	}

	return pendencias, nil
}

// AgendarRetentativa marca a proxima tentativa de uma pendencia.
//
// Agendar e uma operacao propria, e nao um efeito colateral de "tentar de novo",
// porque o agendamento precisa acontecer mesmo quando a tentativa falha. Se so fosse
// gravado no caminho do sucesso, uma falha de infraestrutura deixaria a pendencia
// com a data no passado e o worker a tentaria de volta em seguida, em laco.
func (r RepositorioTransacoes) AgendarRetentativa(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
	proximaEm time.Time,
	motivo string,
) error {
	// A nota da falha e sobrescrita a cada tentativa. O campo guarda o ultimo motivo,
	// e nao um historico: o historico esta no log, e duplicar log em tabela de
	// transacao seria o mesmo dado em dois lugares com dois donos, e o dono que perde
	// e sempre o mesmo: quem nao escreve.
	_, err := q.Exec(ctx, `
		UPDATE wager_transactions
		   SET retry_count     = retry_count + 1,
		       next_retry_at   = $2,
		       last_retry_error = $3,
		       updated_at      = now()
		 WHERE id = $1
		   AND state = 'PENDING_REFERENCE'`,
		id.UUID(), proximaEm, motivo)
	if err != nil {
		return fmt.Errorf("pg: agendamento da retentativa: %w", err)
	}
	return nil
}

// ConcluirPendencia marca a pendencia como expirada.
//
// Expirar e um desfecho legitimo e nao um erro: a referencia pode nunca chegar, e um
// provedor esperando para sempre e pior do que um provedor com uma recusa explicita.
// O codigo de falha e o que separa os dois casos para quem consulta.
func (r RepositorioTransacoes) ConcluirPendencia(
	ctx context.Context,
	q Querente,
	id wallet.Identificador,
	estado wagering.Estado,
	codigo wagering.CodigoFalha,
	agora time.Time,
) error {
	_, err := q.Exec(ctx, `
		UPDATE wager_transactions
		   SET state         = $2,
		       failure_code  = $3,
		       last_retry_error = last_retry_error,
		       updated_at    = $4
		 WHERE id = $1
		   AND state = 'PENDING_REFERENCE'`,
		id.UUID(), string(estado), string(codigo), agora)
	if err != nil {
		return fmt.Errorf("pg: conclusao da pendencia: %w", err)
	}
	return nil
}

// ContarPendencias conta as transacoes em um estado.
//
// Vive no repositorio e nao no caso de uso porque e leitura pura: nao ha regra, nao
// ha unidade e nao ha nada a decidir. E o que da o workere os testes uma visao do
// estado da fila de espera sem precisar de SQL.
func (r RepositorioTransacoes) ContarPendencias(ctx context.Context, q Querente, estado wagering.Estado) (int, error) {
	var total int
	linha := q.QueryRow(ctx,
		`SELECT count(*) FROM wager_transactions WHERE state = $1`, string(estado))
	if err := linha.Scan(&total); err != nil {
		return 0, fmt.Errorf("pg: contagem de pendencias: %w", err)
	}
	return total, nil
}

// awsTexto desempacota um texto que pode ser nulo.
func awsTexto(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
