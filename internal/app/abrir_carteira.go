package app

import (
	"context"
	"fmt"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/eventos"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// RequisicaoAbertura e o comando de abertura de carteira.
type RequisicaoAbertura struct {
	// Jogador e o dono da carteira.
	Jogador wallet.Identificador
	// SaldoInicial e o saldo com que a carteira nasce. Zero e aceito.
	SaldoInicial money.Money
	// Correlacao amarra os eventos desta abertura a um fluxo. Vazio gera uma.
	Correlacao string
}

// RespostaAbertura e o resultado da abertura.
type RespostaAbertura struct {
	// Carteira e a carteira criada.
	Carteira wallet.Carteira
	// Replay informa que a carteira ja existia e nada foi criado.
	//
	// O enunciado trata a segunda abertura do mesmo jogador e moeda como conflito.
	// O caso de uso devolve conflito; este campo existe para que a camada de
	// aplicacao, que pode querer ser idempotente por outro motivo, saiba
	// distinguir.
	Replay bool
}

// AbrirCarteira cria a carteira, e com ela a transacao de abertura, o lancamento de
// credito e os dois eventos, tudo no mesmo commit.
//
// A ordem dentro da transacao e esta: carteira, OPENING, lancamento, saldo, outbox.
// O trigger de coerencia do ledger e deferido justamente para que a ordem nao
// precise ser esta -- o que importa e que tudo confirme junto.
//
// Saldo inicial zero nao cria OPENING, nem lancamento, nem evento. Criar a
// transacao assim daria um OPENING por jogador sem nenhum movimento por tras, e o
// ledger passaria a contar uma operacao que nao aconteceu.
func AbrirCarteira(ctx context.Context, s Servicos, ator Ator, req RequisicaoAbertura) (RespostaAbertura, error) {
	if err := s.verifica("unidade", "carteiras", "ledger", "transacoes", "outbox", "correlacao"); err != nil {
		return RespostaAbertura{}, err
	}

	// Abertura de carteira e operacao interna. Um provedor com escopo de operacoes
	// nao abre carteira: e a separacao que impede o provedor de criar saldo para
	// si mesmo.
	if !ator.Interno() {
		return RespostaAbertura{}, fmt.Errorf("%w: abertura de carteira exige o cliente interno", ErrNaoAutorizado)
	}
	if !ator.TemEscopo(EscopoAberturaCarteira) {
		return RespostaAbertura{}, fmt.Errorf("%w: escopo %s ausente", ErrNaoAutorizado, EscopoAberturaCarteira)
	}
	if err := garantirJogador(req.Jogador); err != nil {
		return RespostaAbertura{}, err
	}
	if err := req.SaldoInicial.Validar(); err != nil {
		return RespostaAbertura{}, fmt.Errorf("%w: saldo inicial: %v", ErrRequisicaoInvalida, err)
	}
	if req.SaldoInicial.IsNegative() {
		return RespostaAbertura{}, fmt.Errorf("%w: saldo inicial negativo %s", ErrRequisicaoInvalida, req.SaldoInicial)
	}

	agora := s.agora()
	correlacao := s.correlacao(req.Correlacao)

	var criada wallet.Carteira
	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		// O identificador da carteira e gerado aqui, e nao pelo banco, porque a
		// transacao de abertura precisa citar a carteira que ela abre e o
		// repositorio so devolve valores prontos.
		nova, err := wallet.Nova(req.Jogador, req.SaldoInicial, agora)
		if err != nil {
			return fmt.Errorf("abertura: %w", err)
		}
		criada = nova

		if err := s.Carteiras.Inserir(ctx, q, nova); err != nil {
			return fmt.Errorf("carteira: %w", err)
		}

		// Saldo zero: a carteira existe e nao ha movimentacao. O enunciado e
		// explicito, e a alternativa seria um OPENING sem lancamento, que o
		// trigger de coerencia do ledger nao tem como aceitar.
		if nova.Saldo().IsZero() {
			return nil
		}

		// A carteira nasce com o saldo inicial e com a versao 1, e o enunciado e
		// explicito: a versao da carteira na abertura e 1. Por isso nao ha UPDATE de
		// saldo aqui. A criacao nao e uma mudanca de saldo -- e o estado inicial --
		// e a versao que subisse na abertura faria a primeira operacao do jogador
		// encontrar uma versao que ninguem esperava.
		//
		// O lancamento existe e registra o credito a partir de zero, que e o que
		// fecha a reconstrucao do ledger: sem ele, a soma de creditos menos debitos
		// nao reproduziria o saldo.
		abertura, err := wagering.AbrirCarteira(wagering.Abertura{
			Carteira: nova.ID(),
			Jogador:  req.Jogador,
			Valor:    req.SaldoInicial,
			CriadaEm: agora,
		})
		if err != nil {
			return fmt.Errorf("abertura interna: %w", err)
		}
		if err := s.Transacoes.Inserir(ctx, q, abertura); err != nil {
			return fmt.Errorf("transacao de abertura: %w", err)
		}

		lancamento, err := wallet.NovoLancamento(
			wallet.NovoIdentificador(), nova.ID(), abertura.ID(),
			wallet.DirecaoCredito, req.SaldoInicial,
			money.Zero(req.SaldoInicial.Currency()), req.SaldoInicial,
			agora,
		)
		if err != nil {
			return fmt.Errorf("lancamento de abertura: %w", err)
		}
		if err := s.Ledger.Inserir(ctx, q, lancamento); err != nil {
			return fmt.Errorf("lancamento de abertura: %w", err)
		}

		// Os dois eventos de origem interna. Nao exigem os metadados externos
		// inaplicaveis -- provedor, id externo, chave, rodada -- porque a origem
		// nao os tem, e o CHECK de origem do banco impede que a transacao de
		// abertura os carregue.
		processada, err := eventos.NovaTransacaoProcessada(eventos.DadosTransacaoProcessada{
			Transacao:     abertura.ID(),
			OrigemInterna: true,
			Carteira:      nova.ID(),
			Tipo:          string(abertura.Tipo()),
			Resultado:     abertura.Resultado(),
			Correlacao:    correlacao,
			OcorridoEm:    agora,
		})
		if err != nil {
			return fmt.Errorf("evento de abertura processada: %w", err)
		}
		if err := s.Outbox.Inserir(ctx, q, processada); err != nil {
			return fmt.Errorf("outbox de abertura processada: %w", err)
		}

		saldoAlterado, err := eventos.NovaSaldoAlterado(eventos.DadosSaldoAlterado{
			Transacao:      abertura.ID(),
			Carteira:       nova.ID(),
			Direcao:        string(lancamento.Direcao()),
			Valor:          lancamento.Valor(),
			SaldoAnterior:  lancamento.SaldoAnterior(),
			SaldoPosterior: lancamento.SaldoPosterior(),
			VersaoCarteira: nova.Versao(),
			Correlacao:     correlacao,
			Causa:          abertura.ID(),
			OcorridoEm:     agora,
		})
		if err != nil {
			return fmt.Errorf("evento de mudanca de saldo: %w", err)
		}
		if err := s.Outbox.Inserir(ctx, q, saldoAlterado); err != nil {
			return fmt.Errorf("outbox de mudanca de saldo: %w", err)
		}

		return nil
	})
	if err != nil {
		return RespostaAbertura{}, err
	}

	return RespostaAbertura{Carteira: criada}, nil
}
