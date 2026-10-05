package app

import (
	"context"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// Politica de retentativa.
//
// O intervalo dobra a cada tentativa ate o teto. E a escolha padrao porque a causa
// mais comum de a referencia nao estar la e o produtor ter enviado as duas mensagens
// fora de ordem, e o dobro por tentativa resolve isso em poucas voltas.
//
// O teto e o que impede a pendencia de existir para sempre. Uma referencia que
// nunca chega tem de virar recusa em algum momento: um provedor esperando para sempre
// e pior do que um provedor com uma resposta definitiva.
type Politica struct {
	// IntervaloInicial e a espera da primeira retentativa.
	IntervaloInicial time.Duration

	// IntervaloMaximo e o teto do crescimento.
	IntervaloMaximo time.Duration

	// MaximoDeTentativas e quantas vezes insistir antes de expirar.
	MaximoDeTentativas int
}

// PoliticaPadrao e a politica usada quando ninguem informa outra.
func PoliticaPadrao() Politica {
	return Politica{
		IntervaloInicial:   2 * time.Second,
		IntervaloMaximo:    5 * time.Minute,
		MaximoDeTentativas: 10,
	}
}

// proximaTentativa devolve o instante da proxima tentativa e se ainda vale tentar.
//
// O numero de tentativas vem do banco e nao de memoria do processo: o worker pode
// morrer e subir de novo, e uma contagem em memoria zeraria a cada reinicio, o que
// tornaria a expiracao inalcancavel.
func (p Politica) ProximaTentativa(tentativasFeitas int, agora time.Time) (time.Time, bool) {
	if tentativasFeitas >= p.MaximoDeTentativas {
		return time.Time{}, false
	}

	espera := p.IntervaloInicial
	for i := 0; i < tentativasFeitas && espera < p.IntervaloMaximo; i++ {
		espera *= 2
	}
	if espera > p.IntervaloMaximo {
		espera = p.IntervaloMaximo
	}

	return agora.Add(espera), true
}

// RetomarPendencias reserva e retoma pendencias, e devolve quantas foram tratadas.
//
// A reserva e o trabalho acontecem na MESMA unidade, uma pendencia por vez. Isso e o
// que torna a operacao segura com tres instancias: o `FOR UPDATE SKIP LOCKED` so
// protege enquanto a transacao esta aberta, e reservar em uma unidade e trabalhar em
// outra liberaria o lock no meio do caminho -- o instante exato em que a segunda
// instancia leria a mesma linha.
//
// Uma pendencia por unidade, e nao um lote, porque uma que falha nao pode reverter as
// outras: as pendencias sao independentes, e um lote seria um ponto unico de falha
// turning uma falha de infraestrutura em varias.
func RetomarPendencias(
	ctx context.Context,
	s Servicos,
	politica Politica,
	limite int,
) (int, error) {
	if err := s.verifica("unidade", "carteiras", "ledger", "transacoes", "outbox"); err != nil {
		return 0, err
	}
	if limite <= 0 {
		limite = 20
	}

	tratadas := 0
	for i := 0; i < limite; i++ {
		if ctx.Err() != nil {
			return tratadas, nil
		}

		achou, err := s.retomarUma(ctx, politica)
		if err != nil {
			return tratadas, err
		}
		if !achou {
			// Nao ha mais pendencia pronta. Um laco que insistisse aqui transformaria
			// o worker em um laco ocioso sem fim.
			return tratadas, nil
		}
		tratadas++
	}

	return tratadas, nil
}

// retomarUma reserva e trata uma pendencia em uma unica unidade.
//
// Devolve false quando nao havia pendencia pronta, e true quando havia, mesmo que o
// resultado tenha sido reagendamento: a pendencia foi vista e tratada, e o teste de
// "ainda ha o que fazer" precisa dessa distincao.
func (s Servicos) retomarUma(ctx context.Context, politica Politica) (bool, error) {
	var tratada bool

	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		agora := s.agora()

		reservadas, err := s.Transacoes.ClaimPendencias(ctx, q, agora, 1)
		if err != nil {
			return err
		}
		if len(reservadas) == 0 {
			return nil
		}

		pendencia := reservadas[0]
		tratada = true

		transacao, err := s.Transacoes.BuscarPorID(ctx, q, pendencia.TransacaoID)
		if err != nil {
			return fmt.Errorf("pendencia: %w", err)
		}

		// A pendencia pode ter sido resolvida entre a reserva e a leitura, por uma
		// leitura feita depois. Devolver "nada a fazer" e o desfecho correto.
		if transacao.Estado().Terminal() {
			return nil
		}
		if transacao.Estado() != wagering.EstadoPendenteReferencia {
			return fmt.Errorf("%w: pendencia em %s", ErrRequisicaoInvalida, transacao.Estado())
		}

		carteira, err := s.Carteiras.LerParaAtualizar(ctx, q, transacao.Carteira())
		if err != nil {
			return fmt.Errorf("carteira: %w", err)
		}

		resultado, err := resolver(ctx, s, q, transacao, carteira, agora)
		if err != nil {
			// Falha de infraestrutura e o unico caminho em que a pendencia volta.
			// Regra de negocio e conclusao, e falha de banco e para a proxima volta.
			return s.reagendar(ctx, q, pendencia, agora, politica, err.Error())
		}

		switch resultado.tipo {
		case desfechoPendente:
			// A referencia continua sem chegar. Reagendar e o unico resultado
			// possivel, e por isso que a expiracao conta tentativas e nao tempo.
			return s.reagendar(ctx, q, pendencia, agora, politica, "referencia ausente")

		case desfechoRecusada:
			// A referencia chegou mas a regra recusa: por exemplo, a reversao ja foi
			// aplicada. Isso e conclusao definitiva, e nao espera.
			return s.confirmar(ctx, q, transacao, carteira, resultado,
				RequisicaoOperacao{}, s.correlacao(""), agora)
		}

		return s.confirmar(ctx, q, transacao, carteira, resultado,
			RequisicaoOperacao{}, s.correlacao(""), agora)
	})
	if err != nil {
		return false, err
	}

	return tratada, nil
}

// reagendar empurra a pendencia para a proxima tentativa ou a expira.
//
// A expiracao grava FAILED com codigo proprio, e nao REJECTED. A diferenca importa
// para quem consulta: REJECTED e a resposta do provedor, e FAILED e a resposta do
// sistema para uma referencia que nunca chegou. O provedor corrige a primeira com
// outra chamada; a segunda nao tem o que corrigir do lado dele.
func (s Servicos) reagendar(
	ctx context.Context,
	q pg.Querente,
	pendencia pg.Pendencia,
	agora time.Time,
	politica Politica,
	motivo string,
) error {
	proximaEm, valeTentar := politica.ProximaTentativa(pendencia.Tentativas, agora)

	if !valeTentar {
		if err := s.Transacoes.ConcluirPendencia(ctx, q, pendencia.TransacaoID,
			wagering.EstadoFalhou, wagering.CodigoFalhaReferenciaNuncaChegou, agora); err != nil {
			return err
		}
		return nil
	}

	return s.Transacoes.AgendarRetentativa(ctx, q, pendencia.TransacaoID, proximaEm, motivo)
}

// ContarPendencias devolve quantas transacoes estao no estado informado.
func ContarPendencias(ctx context.Context, s Servicos, estado wagering.Estado) (int, error) {
	if err := s.verifica("unidade", "transacoes"); err != nil {
		return 0, err
	}

	var total int
	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		var err error
		total, err = s.Transacoes.ContarPendencias(ctx, q, estado)
		return err
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
