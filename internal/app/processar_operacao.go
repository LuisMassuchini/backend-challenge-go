package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// RequisicaoOperacao e o comando de operacao de apostas.
//
// Os campos de identidade externa vem prontos do transporte ja resolvido: o
// `Idempotency-Key` do header e o `data.idempotencyKey` da mensagem SQS chegam no
// mesmo campo, e e o que faz HTTP e SQS compartilharem a mesma garantia.
type RequisicaoOperacao struct {
	// Provedor e o provedor que envia a operacao. Precisa ser o provedor
	// autorizado do ator: e a checagem que impede um provedor de escrever em nome
	// de outro.
	Provedor wagering.Provedor
	// TransacaoExterna e o identificador da operacao no provedor.
	TransacaoExterna wagering.Externo
	// Chave e a chave de idempotencia recebida. O servidor nao substitui a chave
	// recebida por outra calculada: o provedor pode escolher o esquema, e um
	// servidor que recalcula rejeita o cliente legitimo cuja chave tem outro
	// formato.
	Chave wagering.Chave
	// Fingerprint e o resumo canonico dos campos de negocio. E o que distingue
	// replay legitimo de conflito.
	Fingerprint wagering.Hash
	// Carteira e a carteira alvo.
	Carteira wallet.Identificador
	// Jogador e o jogador dono da carteira.
	Jogador wallet.Identificador
	// Rodada e a rodada de jogos.
	Rodada wagering.Rodada
	// Jogo e o jogo.
	Jogo wagering.Jogo
	// Tipo e o tipo da operacao.
	Tipo wagering.Tipo
	// Valor e o valor da operacao.
	Valor money.Money
	// Referencia e a referencia externa, obrigatoria em REFUND e ROLLBACK.
	Referencia wagering.Referencia
	// Correlacao amarra os eventos a um fluxo.
	Correlacao string
	// MensagemID e o identificador da mensagem quando a origem e SQS. Vazio em
	// HTTP.
	MensagemID string
	// Consumidor e o nome registrado na inbox. Vazio em HTTP.
	Consumidor string
}

// RespostaOperacao e o resultado da operacao.
type RespostaOperacao struct {
	// Estado e o estado final da transacao.
	Estado wagering.Estado
	// Saldo e o saldo devolvido ao provedor: o observado no processamento
	// original, e nao o saldo atual da carteira.
	Saldo money.Money
	// CodigoFalha e o motivo, quando houve rejeicao ou falha.
	CodigoFalha wagering.CodigoFalha
	// TransacaoID e o identificador interno da transacao.
	TransacaoID wallet.Identificador
	// Replay informa que a operacao ja existia e o resultado persistido foi
	// devolvido sem reaplicar nada.
	Replay bool
}

// ProcessarOperacao executa uma operacao externa de forma transacional.
//
// A ordem dentro da unidade e: idempotencia, transacao, lock da carteira, regra,
// saldo, lancamento, conclusao, outbox, inbox. E o que faz a operacao atomica: se
// qualquer passo falhar, nenhum dos anteriores permanece.
//
// O lock vem antes de qualquer leitura de saldo porque a regra depende do saldo, e
// saldo sem lock e uma leitura que pode estar velha no instante da decisao.
func ProcessarOperacao(ctx context.Context, s Servicos, ator Ator, req RequisicaoOperacao) (RespostaOperacao, error) {
	if err := s.verifica("unidade", "carteiras", "ledger", "transacoes", "outbox", "correlacao"); err != nil {
		return RespostaOperacao{}, err
	}
	if err := validarAtorOperacao(ator, req); err != nil {
		return RespostaOperacao{}, err
	}
	if err := validarOperacao(req); err != nil {
		return RespostaOperacao{}, err
	}

	agora := s.agora()
	correlacao := s.correlacao(req.Correlacao)

	var resposta RespostaOperacao

	registro := wagering.Registro{
		Provedor:          req.Provedor,
		TransacaoExterna:  req.TransacaoExterna,
		ChaveIdempotencia: req.Chave,
		HashConteudo:      req.Fingerprint,
		Carteira:          req.Carteira,
		Jogador:           req.Jogador,
		Rodada:            req.Rodada,
		Jogo:              req.Jogo,
		Tipo:              req.Tipo,
		Valor:             req.Valor,
		Referencia:        req.Referencia,
		CriadaEm:          agora,
	}

	err := s.Unidade.Executar(ctx, func(q pg.Querente) error {
		// A idempotencia e resolvida antes de qualquer escrita. E a unica ordem
		// segura: gravar a transacao primeiro e so depois consultar ja teria criado
		// a linha que a idempotencia existe para impedir.
		existente, err := s.Transacoes.BuscarPorChave(ctx, q, req.Chave)
		switch {
		case err == nil:
			resposta, err = responderReplay(existente, req)
			return err
		case errors.Is(err, pg.ErrNaoEncontrado):
			// Segue: a chave e nova.
		default:
			return fmt.Errorf("idempotencia: %w", err)
		}

		// O segundo indice de idempotencia: a mesma operacao externa ja registrada
		// com outra chave e conflito, e nao replay.
		peloExterno, err := s.Transacoes.BuscarPorProvedorEExterno(ctx, q, req.Provedor, req.TransacaoExterna)
		switch {
		case err == nil:
			resposta, err = responderReplay(peloExterno, req)
			return err
		case errors.Is(err, pg.ErrNaoEncontrado):
			// Segue: a operacao externa e nova.
		default:
			return fmt.Errorf("operacao externa: %w", err)
		}

		// A transacao entra primeiro, em PENDING, e e concluida no fim. A ordem
		// oposta -- concluir e so depois gravar -- deixaria a operacao aplicada sem
		// registro, e a reconciliacao veria saldo sem transacao.
		transacao, err := wagering.Registrar(registro)
		if err != nil {
			return traduzirFalhaDeRegra(err)
		}
		if err := s.Transacoes.Inserir(ctx, q, transacao); err != nil {
			return err
		}

		carteira, err := s.Carteiras.LerParaAtualizar(ctx, q, req.Carteira)
		if err != nil {
			return fmt.Errorf("carteira: %w", err)
		}

		desfecho, err := resolver(ctx, s, q, transacao, carteira, agora)
		if err != nil {
			return err
		}

		switch desfecho.tipo {
		case desfechoRecusada:
			// A recusa e uma conclusao legitima: a transacao fica REJECTED com o
			// codigo, o evento de rejeicao e gravado, e o commit acontece. E o que o
			// provedor consulta depois, e desfaze-la deixaria a transacao em
			// PENDING para sempre.
			rejeitada, err := transacao.Rejeitar(desfecho.codigo, agora)
			if err != nil {
				return err
			}
			if err := s.Transacoes.Concluir(ctx, q, rejeitada.ID(), pg.ResultadoGravado{
				Estado:      rejeitada.Estado(),
				CodigoFalha: rejeitada.CodigoFalha(),
			}); err != nil {
				return err
			}
			if err := registrarEventoRejeicao(ctx, s, q, rejeitada, correlacao, agora); err != nil {
				return err
			}
			if err := s.registrarInbox(ctx, q, req, agora); err != nil {
				return err
			}
			resposta = RespostaOperacao{
				Estado:      rejeitada.Estado(),
				Saldo:       carteira.Saldo(),
				CodigoFalha: rejeitada.CodigoFalha(),
				TransacaoID: rejeitada.ID(),
			}
			return nil

		case desfechoPendente:
			// A referencia ainda nao chegou. Isso nao e recusa nem sucesso: e
			// espera, e o worker de referencias assume depois.
			if err := s.Transacoes.MarcarPendentePorReferencia(ctx, q, transacao.ID()); err != nil {
				return err
			}
			if err := s.registrarInbox(ctx, q, req, agora); err != nil {
				return err
			}
			resposta = RespostaOperacao{
				// O estado vem da constante, e nao da transacao em memoria: em
				// memoria a transacao continua PENDING, e quem consulta e o banco,
				// onde o estado gravado e PENDING_REFERENCE.
				Estado:      wagering.EstadoPendenteReferencia,
				Saldo:       carteira.Saldo(),
				TransacaoID: transacao.ID(),
			}
			return nil
		}

		// Caminho feliz: o lancamento, a conclusao e o evento.
		if desfecho.lancamento.Valida() {
			if err := s.Ledger.Inserir(ctx, q, desfecho.lancamento); err != nil {
				return err
			}
			if err := s.Carteiras.AtualizarSaldo(ctx, q, desfecho.carteira); err != nil {
				return err
			}
			if err := registrarEventoDeSaldo(ctx, s, q, desfecho.lancamento, transacao,
				desfecho.carteira.Versao(), correlacao, agora); err != nil {
				return err
			}
		}

		if err := s.Transacoes.Concluir(ctx, q, transacao.ID(), pg.ResultadoGravado{
			Estado:    wagering.EstadoProcessado,
			Resultado: desfecho.carteira.Saldo(),
		}); err != nil {
			return err
		}
		if err := registrarEventoProcessada(ctx, s, q, transacao, req,
			desfecho.carteira.Saldo(), correlacao, agora); err != nil {
			return err
		}
		if err := s.registrarInbox(ctx, q, req, agora); err != nil {
			return err
		}

		resposta = RespostaOperacao{
			Estado:      wagering.EstadoProcessado,
			Saldo:       desfecho.carteira.Saldo(),
			TransacaoID: transacao.ID(),
		}
		return nil
	})
	if err != nil {
		return RespostaOperacao{}, err
	}

	return resposta, nil
}

// tipos de desfecho possiveis de uma operacao.
const (
	// desfechoConcluida e o caminho com efeito financeiro.
	desfechoConcluida = iota
	// desfechoRecusada e a recusa por regra de negocio.
	desfechoRecusada
	// desfechoPendente e a espera por referencia.
	desfechoPendente
)

// desfecho e o resultado interno da resolucao de uma operacao.
type desfecho struct {
	// tipo e o que aconteceu.
	tipo int
	// carteira e a carteira depois do efeito, quando houve.
	carteira wallet.Carteira
	// lancamento e o lancamento produzido, quando houve.
	lancamento wallet.Lancamento
	// codigo e o motivo da recusa.
	codigo wagering.CodigoFalha
}

// resolver aplica a regra e devolve o desfecho.
//
// Separado do caso de uso porque e aqui que mora a ordem entre "validar a regra" e
// "mover o dinheiro", e essa ordem e o que o enunciado exige: nenhuma movimentacao
// acontece sem a regra ter passado.
func resolver(
	ctx context.Context,
	s Servicos,
	q pg.Querente,
	transacao wagering.Transacao,
	carteira wallet.Carteira,
	agora time.Time,
) (desfecho, error) {
	// A regra de valor e de moeda vem do dominio, e vale para LOSS tambem: um LOSS
	// em moeda que a carteira nao tem nao produz lancamento que denuncie.
	if err := transacao.ValidarValor(); err != nil {
		return recusaDe(err)
	}
	if err := transacao.ValidarMoedaDaCarteira(carteira); err != nil {
		return recusaDe(err)
	}

	// referenciante e a transacao referenciada, quando a operacao e uma reversao.
	// Vive fora do if porque o dominio precisa dela no Aplicar.
	var referenciante *wagering.Transacao

	if transacao.Tipo().ExigeReferencia() {
		resolvida, referenciada, pendente, err := resolverReferencia(ctx, s, q, transacao, agora)
		if err != nil {
			return desfecho{}, err
		}
		if pendente {
			return desfecho{tipo: desfechoPendente, carteira: carteira}, nil
		}
		transacao = resolvida
		// A referenciada segue adiante porque o dominio usa o lancamento original
		// para montar o lancamento inverso. Passar nil aqui faria o proprio dominio
		// recusar a reversao, e a recusa chegaria ao provedor como se fosse saldo.
		referenciante = &referenciada
	}

	// O movimento. LOSS chega aqui sem efeito e devolve a carteira intacta, com
	// lancamento invalido.
	movida, lancamento, err := transacao.Aplicar(carteira, referenciante, agora)
	if err != nil {
		return recusaDe(err)
	}

	return desfecho{
		tipo:       desfechoConcluida,
		carteira:   movida,
		lancamento: lancamento,
	}, nil
}

// resolverReferencia procura a transacao referenciada e valida a politica de
// reversao.
//
// Devolve pendente quando a referencia ainda nao chegou, que e espera e nao recusa:
// uma reversao entregue antes da aposta e exatamente o caso que o enunciado exige
// suportar.
//
// Devolve a reversao resolvida e a transacao referenciada. As duas sao necessarias:
// a primeira e o que vai para o banco, a segunda e o que o dominio usa para montar
// o lancamento inverso.
func resolverReferencia(
	ctx context.Context,
	s Servicos,
	q pg.Querente,
	transacao wagering.Transacao,
	agora time.Time,
) (wagering.Transacao, wagering.Transacao, bool, error) {
	referenciada, err := s.Transacoes.BuscarPorProvedorEExterno(
		ctx, q, transacao.Provedor(), transacao.Referencia().Externa,
	)
	if err != nil {
		if errors.Is(err, pg.ErrNaoEncontrado) {
			return transacao, wagering.Transacao{}, true, nil
		}
		return transacao, wagering.Transacao{}, false, fmt.Errorf("referencia: %w", err)
	}

	tiposAplicados, err := s.Transacoes.ReversoesSobre(ctx, q, referenciada.ID())
	if err != nil {
		return transacao, wagering.Transacao{}, false, err
	}

	// A resolucao vem antes da validacao, e a ordem e obrigatoria: a politica de
	// reversao recusa uma transacao cuja referencia interna ainda nao foi resolvida,
	// entao validar primeiro reprovaria toda reversao lega com
	// REFERENCIA_NAO_ENCONTRADA.
	resolvida, err := transacao.ResolverReferencia(referenciada.ID(), agora)
	if err != nil {
		return transacao, referenciada, false, err
	}

	if err := resolvida.ValidarReversao(referenciada, tiposAplicados); err != nil {
		return transacao, referenciada, false, traduzirFalhaDeRegra(err)
	}

	if err := s.Transacoes.ResolverReferencia(ctx, q, transacao.ID(), referenciada.ID()); err != nil {
		return transacao, referenciada, false, err
	}

	return resolvida, referenciada, false, nil
}

// recusaDe converte a recusa do dominio em desfecho recusado.
//
// Traduzir antes de decidir e o que faz a recusa virar desfecho em vez de erro. O
// dominio devolve a propria falha de regra, que so depois de traduzida tem o codigo
// que o caso de uso precisa gravar na transacao.
func recusaDe(err error) (desfecho, error) {
	traduzida := traduzirFalhaDeRegra(err)

	var falha *FalhaDeRegra
	if errors.As(traduzida, &falha) {
		return desfecho{tipo: desfechoRecusada, codigo: falha.Codigo}, nil
	}
	return desfecho{}, traduzida
}

// responderReplay devolve o resultado ja persistido.
//
// E o que impede movimentacao duplicada em duas situacoes que o enunciado trata
// como eliminatorias: recebimento repetido da mesma operacao e reenvio com a mesma
// chave depois de um processamento concluido.
func responderReplay(existente wagering.Transacao, req RequisicaoOperacao) (RespostaOperacao, error) {
	// Mesma chave com conteudo diferente e conflito, e nao replay. Sem esta
	// comparacao, um cliente que muda o valor da operacao e reenvia com a chave
	// antiga receberia o resultado da operacao antiga e acharia que passou.
	if existente.HashConteudo() != req.Fingerprint {
		return RespostaOperacao{}, fmt.Errorf(
			"%w: chave %q ja registrada com outro conteudo", ErrConflitoDeChave, req.Chave,
		)
	}

	if !existente.Estado().Terminal() {
		// Replay de operacao ainda nao concluida devolve o estado atual e nao um
		// resultado inventado. E o que permite ao provedor distinguir "ainda
		//(processando" de "concluido".
		return RespostaOperacao{
			Estado:      existente.Estado(),
			TransacaoID: existente.ID(),
			Replay:      true,
		}, nil
	}

	resposta := RespostaOperacao{
		Estado:      existente.Estado(),
		TransacaoID: existente.ID(),
		Replay:      true,
	}
	if existente.Resultado().Valida() {
		resposta.Saldo = existente.Resultado()
	}
	if existente.CodigoFalha().Presente() {
		resposta.CodigoFalha = existente.CodigoFalha()
	}
	return resposta, nil
}

// registrarInbox marca a mensagem como recebida, quando a origem e SQS.
//
// Registrar e nao concluir: a conclusao da inbox e o passo seguinte do consumidor,
// depois que o efeito ja foi confirmado. Registrar dentro da mesma transacao do
// efeito e o que garante que uma reentrega encontra o registro e devolve o
// resultado persistido em vez de mover dinheiro de novo.
func (s Servicos) registrarInbox(
	ctx context.Context,
	q pg.Querente,
	req RequisicaoOperacao,
	agora time.Time,
) error {
	if req.Consumidor == "" || req.MensagemID == "" {
		return nil
	}
	return s.Inbox.Registrar(ctx, q, req.Consumidor, req.MensagemID, req.Fingerprint.String(), agora)
}
