package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/fingerprint"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// tamanhoMaximoDeCorpo limita o corpo lido.
//
// O limite nao e decorativo: sem ele um cliente pode mandar um corpo de gigabytes
// e o processo le ate o fim da memoria antes de decidir que o payload nao presta.
const tamanhoMaximoDeCorpo = 1 << 20

// requisicaoAbertura e o corpo de POST /wallets.
type requisicaoAbertura struct {
	// PlayerId e o jogador dono da carteira.
	PlayerId string `json:"playerId"`

	// InitialBalance e o saldo inicial.
	InitialBalance dinheiroNoCorpo `json:"initialBalance"`
}

// respostaCarteira e o corpo de leitura de carteira.
type respostaCarteira struct {
	// ID e o identificador da carteira.
	ID string `json:"id"`

	// PlayerId e o jogador dono.
	PlayerId string `json:"playerId"`

	// Balance e o saldo atual.
	Balance dinheiroNoCorpo `json:"balance"`

	// Version e a versao do saldo, que incrementa a cada movimentacao.
	Version int64 `json:"version"`

	// Currency e a moeda da carteira, repetida fora do saldo para que o cliente
	// saiba a moeda sem abrir o objeto do dinheiro.
	Currency string `json:"currency"`
}

// respostaOperacao e o corpo de POST /wagering/transactions.
type respostaOperacao struct {
	// TransactionId e a transacao interna criada.
	TransactionId string `json:"transactionId"`

	// Status e o estado final, ou o pendente.
	Status string `json:"status"`

	// Balance e o saldo devolvido. No replay e o saldo do processamento original,
	// nao o saldo atual da carteira.
	Balance *dinheiroNoCorpo `json:"balance,omitempty"`

	// IdempotentReplay informa que o resultado veio do banco.
	IdempotentReplay bool `json:"idempotentReplay"`

	// FailureCode e o codigo da recusa, quando houve.
	FailureCode string `json:"failureCode,omitempty"`
}

// requisicaoOperacao e o corpo de POST /wagering/transactions.
type requisicaoOperacao struct {
	// ProviderId e o provedor que envia.
	ProviderId string `json:"providerId"`

	// ExternalTransactionId e o identificador no provedor.
	ExternalTransactionId string `json:"externalTransactionId"`

	// PlayerId e o jogador dono da carteira.
	PlayerId string `json:"playerId"`

	// WalletId e a carteira alvo.
	WalletId string `json:"walletId"`

	// RoundId e a rodada de jogos.
	RoundId string `json:"roundId"`

	// GameId e o jogo.
	GameId string `json:"gameId"`

	// Kind e o tipo da operacao.
	Kind string `json:"kind"`

	// Money e o valor.
	Money dinheiroNoCorpo `json:"money"`

	// ReferenceExternalTransactionId aponta a operacao que a reversao desfaz.
	ReferenceExternalTransactionId string `json:"referenceExternalTransactionId,omitempty"`
}

// respostaTransacao e o corpo de leitura de transacao.
type respostaTransacao struct {
	// Id e a transacao interna.
	Id string `json:"id"`

	// ProviderId e o provedor.
	ProviderId string `json:"providerId"`

	// ExternalTransactionId e o identificador no provedor. Vazio nas transacoes de
	// origem interna, que nao tem origem externa.
	ExternalTransactionId string `json:"externalTransactionId,omitempty"`

	// Kind e o tipo.
	Kind string `json:"kind"`

	// State e o estado.
	State string `json:"state"`

	// Result e o saldo observado no processamento, quando houve.
	Result *dinheiroNoCorpo `json:"result,omitempty"`

	// FailureCode e o codigo de recusa, quando houve.
	FailureCode string `json:"failureCode,omitempty"`

	// ReferenceInternalId e a transacao referenciada, quando resolvida.
	ReferenceInternalId string `json:"referenceInternalId,omitempty"`
}

// respostaLedger e o corpo de GET /wallets/{id}/ledger.
type respostaLedger struct {
	// Entries sao os lancamentos, do mais recente para o mais antigo.
	Entries []respostaLancamento `json:"entries"`

	// NextCursor e o cursor da proxima pagina. Vazio quando acabou.
	NextCursor string `json:"nextCursor,omitempty"`

	// HasMore informa se existe pagina seguinte.
	HasMore bool `json:"hasMore"`
}

// respostaLancamento e um lancamento do ledger.
type respostaLancamento struct {
	// ID e o identificador do lancamento.
	ID string `json:"id"`

	// TransactionId e a transacao que produziu o lancamento.
	TransactionId string `json:"transactionId"`

	// Direction e CREDIT ou DEBIT.
	Direction string `json:"direction"`

	// Amount e o valor.
	Amount dinheiroNoCorpo `json:"amount"`

	// BalanceBefore e o saldo antes.
	BalanceBefore dinheiroNoCorpo `json:"balanceBefore"`

	// BalanceAfter e o saldo depois.
	BalanceAfter dinheiroNoCorpo `json:"balanceAfter"`

	// CreatedAt e o instante do lancamento.
	CreatedAt string `json:"createdAt"`
}

// respostaReconciliacao e o corpo de POST /wallets/{id}/reconciliation.
type respostaReconciliacao struct {
	// WalletId e a carteira conferida.
	WalletId string `json:"walletId"`

	// StoredBalance e o saldo gravado.
	StoredBalance dinheiroNoCorpo `json:"storedBalance"`

	// CalculatedBalance e o saldo reconstruido a partir do ledger.
	CalculatedBalance dinheiroNoCorpo `json:"calculatedBalance"`

	// Difference e o saldo gravado menos o reconstruido.
	Difference dinheiroNoCorpo `json:"difference"`

	// Consistent informa se as duas fontes concordam.
	Consistent bool `json:"consistent"`

	// CheckedEntries e quantos lancamentos entraram na conta.
	CheckedEntries int64 `json:"checkedEntries"`
}

// abrirCarteira atende POST /wallets.
func abrirCarteira(d Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var corpo requisicaoAbertura
		if err := lerCorpo(w, r, &corpo); err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		jogador, err := wallet.IdentificadorDe(corpo.PlayerId)
		if err != nil {
			escreverErro(r.Context(), w, fmt.Errorf("%w: playerId invalido", app.ErrRequisicaoInvalida))
			return
		}
		saldo, err := dinheiroParaDominio(corpo.InitialBalance)
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		ator, _ := AtorDoContexto(r.Context())
		resposta, err := app.AbrirCarteira(r.Context(), d.Servicos, ator, app.RequisicaoAbertura{
			Jogador:      jogador,
			SaldoInicial: saldo,
		})
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		// 201 e o codigo certo porque a carteira foi criada, e o Location aponta
		// para a leitura que o cliente vai fazer a seguir.
		w.Header().Set("Location", "/wallets/"+resposta.Carteira.ID().String())
		responderJSON(w, http.StatusCreated, carteiraNoCorpo(resposta.Carteira))
	}
}

// lerCarteira atende GET /wallets/{walletId}.
func lerCarteira(d Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identificador, err := wallet.IdentificadorDe(r.PathValue("walletId"))
		if err != nil {
			escreverErro(r.Context(), w, fmt.Errorf("%w: walletId invalido", app.ErrRequisicaoInvalida))
			return
		}

		ator, _ := AtorDoContexto(r.Context())
		carteira, err := app.LerCarteira(r.Context(), d.Servicos, ator, identificador)
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		responderJSON(w, http.StatusOK, carteiraNoCorpo(carteira))
	}
}

// listarLedger atende GET /wallets/{walletId}/ledger.
func listarLedger(d Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identificador, err := wallet.IdentificadorDe(r.PathValue("walletId"))
		if err != nil {
			escreverErro(r.Context(), w, fmt.Errorf("%w: walletId invalido", app.ErrRequisicaoInvalida))
			return
		}

		// O limite vem do cliente e por isso tem teto. Um limit alto demais em uma
		// carteira com muito movimento consome memoria do processo e derruba a
		// requisicao em vez de recusar um numero absurdo.
		limite := 0
		if bruto := r.URL.Query().Get("limit"); bruto != "" {
			limite, err = strconv.Atoi(bruto)
			if err != nil || limite <= 0 {
				escreverErro(r.Context(), w, fmt.Errorf("%w: limit precisa ser um inteiro positivo", app.ErrRequisicaoInvalida))
				return
			}
		}

		cursor, err := pg.DecodificarCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			escreverErro(r.Context(), w, fmt.Errorf("%w: %v", app.ErrRequisicaoInvalida, err))
			return
		}

		ator, _ := AtorDoContexto(r.Context())
		pagina, err := app.ListarLedger(r.Context(), d.Servicos, ator,
			app.RequisicaoLedger{Carteira: identificador}, cursor, limite)
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		corpo := respostaLedger{Entries: []respostaLancamento{}, HasMore: pagina.TemMais}
		for _, lancamento := range pagina.Lancamentos {
			corpo.Entries = append(corpo.Entries, respostaLancamento{
				ID:            lancamento.ID().String(),
				TransactionId: lancamento.Transacao().String(),
				Direction:     string(lancamento.Direcao()),
				Amount:        dinheiroDoDominio(lancamento.Valor()),
				BalanceBefore: dinheiroDoDominio(lancamento.SaldoAnterior()),
				BalanceAfter:  dinheiroDoDominio(lancamento.SaldoPosterior()),
				CreatedAt:     lancamento.CriadoEm().UTC().Format("2006-01-02T15:04:05.000Z"),
			})
		}
		if pagina.TemMais {
			corpo.NextCursor = pagina.ProximoCursor.Texto()
		}

		responderJSON(w, http.StatusOK, corpo)
	}
}

// reconciliar atende POST /wallets/{walletId}/reconciliation.
func reconciliar(d Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identificador, err := wallet.IdentificadorDe(r.PathValue("walletId"))
		if err != nil {
			escreverErro(r.Context(), w, fmt.Errorf("%w: walletId invalido", app.ErrRequisicaoInvalida))
			return
		}

		ator, _ := AtorDoContexto(r.Context())
		resposta, err := app.Reconciliar(r.Context(), d.Servicos, ator, app.RequisicaoReconciliacao{
			Carteira: identificador,
		})
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		corpo := respostaReconciliacao{
			WalletId:          identificador.String(),
			StoredBalance:     dinheiroDoDominio(resposta.SaldoGravado),
			Consistent:        !resposta.Divergente,
			CheckedEntries:    resposta.Lancamentos,
			CalculatedBalance: dinheiroDoDominio(resposta.SaldoDoLedger),
			Difference:        dinheiroDoDominio(resposta.Diferenca),
		}

		// Divergencia e uma resposta bem-sucedida com o achado dentro. Devolver 409
		// faria o cliente tratar uma leitura que funcionou como falha.
		responderJSON(w, http.StatusOK, corpo)
	}
}

// enviarOperacao atende POST /wagering/transactions.
func enviarOperacao(d Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chave, err := chaveDeIdempotencia(r)
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		var corpo requisicaoOperacao
		if err := lerCorpo(w, r, &corpo); err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		comando, err := comandoDaOperacao(chave, CorrelacaoDoContexto(r.Context()), corpo)
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		ator, _ := AtorDoContexto(r.Context())
		resposta, err := app.ProcessarOperacao(r.Context(), d.Servicos, ator, comando)
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		responderJSON(w, statusDaOperacao(resposta), transacaoNoCorpo(resposta, comando))
	}
}

// lerTransacao atende GET /wagering/transactions/{transactionId}.
func lerTransacao(d Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identificador, err := wallet.IdentificadorDe(r.PathValue("transactionId"))
		if err != nil {
			escreverErro(r.Context(), w, fmt.Errorf("%w: transactionId invalido", app.ErrRequisicaoInvalida))
			return
		}

		ator, _ := AtorDoContexto(r.Context())
		transacao, err := app.LerTransacao(r.Context(), d.Servicos, ator, identificador)
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		responderJSON(w, http.StatusOK, transacaoParaCorpo(transacao))
	}
}

// lerTransacaoDoProvedor atende
// GET /providers/{providerId}/wagering/transactions/{externalTransactionId}.
func lerTransacaoDoProvedor(d Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provedor := r.PathValue("providerId")
		externa := r.PathValue("externalTransactionId")

		// A checagem de divergencia acontece antes de qualquer leitura. O token do
		// provider-a perguntando pela operacao do provider-b tem de responder 403
		// sem que a resposta revele se aquela operacao existe.
		ator, _ := AtorDoContexto(r.Context())
		if ator.Provedor != provedor {
			responderJSON(w, http.StatusForbidden, respostaErro{
				Erro:    "provedor_divergente",
				Codigo:  http.StatusForbidden,
				Detalhe: fmt.Sprintf("o token e do provedor %q e a consulta e do provedor %q", ator.Provedor, provedor),
			})
			return
		}

		transacao, err := app.LerTransacaoDoProvedor(r.Context(), d.Servicos, ator,
			wagering.Provedor(provedor), wagering.Externo(externa))
		if err != nil {
			escreverErro(r.Context(), w, err)
			return
		}

		responderJSON(w, http.StatusOK, transacaoParaCorpo(transacao))
	}
}

// chaveDeIdempotencia le o cabecalho obrigatorio.
//
// A ausencia e 400 e nao 422: o payload pode estar perfeito, mas sem a chave o
// servidor nao tem como prometer que repetir a chamada nao move dinheiro duas
// vezes, e isso e problema de quem fez a chamada.
func chaveDeIdempotencia(r *http.Request) (string, error) {
	chave := r.Header.Get("Idempotency-Key")
	if chave == "" {
		return "", fmt.Errorf("%w: cabecalho Idempotency-Key obrigatorio", app.ErrRequisicaoInvalida)
	}
	// O teto evita uma chave de megabytes sendo gravada em uma coluna e indexada.
	if len(chave) > 255 {
		return "", fmt.Errorf("%w: Idempotency-Key com mais de 255 caracteres", app.ErrRequisicaoInvalida)
	}
	return chave, nil
}

// comandoDaOperacao monta o caso de uso a partir do corpo.
//
// O resumo do conteudo e calculado aqui, e nao no caso de uso, porque ele e
// responsabilidade do transporte: depende de quais campos o contrato considera de
// negocio, e essa lista muda com o contrato, nao com o dominio.
func comandoDaOperacao(chave, correlacao string, corpo requisicaoOperacao) (app.RequisicaoOperacao, error) {
	valor, err := dinheiroParaDominio(corpo.Money)
	if err != nil {
		return app.RequisicaoOperacao{}, err
	}

	jogador, err := wallet.IdentificadorDe(corpo.PlayerId)
	if err != nil {
		return app.RequisicaoOperacao{}, fmt.Errorf("%w: playerId invalido", app.ErrRequisicaoInvalida)
	}
	carteira, err := wallet.IdentificadorDe(corpo.WalletId)
	if err != nil {
		return app.RequisicaoOperacao{}, fmt.Errorf("%w: walletId invalido", app.ErrRequisicaoInvalida)
	}

	// O resumo usa o texto decimal canonico, e nao o que veio no corpo: "25.0" e
	// "25.00" sao o mesmo valor e precisam ser reconhecidos como a mesma operacao
	// em um reenvio.
	resumo := fingerprint.Calcular(fingerprint.Entrada{
		Provedor:   corpo.ProviderId,
		Externa:    corpo.ExternalTransactionId,
		Jogador:    corpo.PlayerId,
		Carteira:   corpo.WalletId,
		Rodada:     corpo.RoundId,
		Jogo:       corpo.GameId,
		Tipo:       corpo.Kind,
		Valor:      valor.Decimal(),
		Moeda:      string(valor.Currency()),
		Referencia: corpo.ReferenceExternalTransactionId,
	})

	return app.RequisicaoOperacao{
		Provedor:         wagering.Provedor(corpo.ProviderId),
		TransacaoExterna: wagering.Externo(corpo.ExternalTransactionId),
		Chave:            wagering.Chave(chave),
		Fingerprint:      wagering.Hash(resumo),
		Carteira:         carteira,
		Jogador:          jogador,
		Rodada:           wagering.Rodada(corpo.RoundId),
		Jogo:             wagering.Jogo(corpo.GameId),
		Tipo:             wagering.Tipo(corpo.Kind),
		Valor:            valor,
		Referencia:       wagering.Referencia{Externa: wagering.Externo(corpo.ReferenceExternalTransactionId)},
		Correlacao:       correlacao,
	}, nil
}

// statusDaOperacao escolhe o status da resposta de operacao.
//
// A recusa de negocio e 422, e nao 200 com um campo de status. Um 200 seria lido
// como sucesso por qualquer painel que so olha o codigo, e o enunciado pede que
// recusa seja distinguivel de sucesso pelo contrato. O corpo continua sendo o da
// operacao, com status e failureCode, para que o cliente tenha os dois sinais.
//
// Pendencia e 202, e nao 409 nem 200: a operacao foi aceita e gravada, e sera
// retomada. O cliente nao precisa repetir o envio.
func statusDaOperacao(resposta app.RespostaOperacao) int {
	switch resposta.Estado {
	case wagering.EstadoRejeitado:
		return http.StatusUnprocessableEntity
	case wagering.EstadoPendenteReferencia:
		return http.StatusAccepted
	default:
		return http.StatusOK
	}
}

// transacaoNoCorpo monta a resposta de operacao.
func transacaoNoCorpo(resposta app.RespostaOperacao, comando app.RequisicaoOperacao) respostaOperacao {
	corpo := respostaOperacao{
		TransactionId:    resposta.TransacaoID.String(),
		Status:           string(resposta.Estado),
		IdempotentReplay: resposta.Replay,
		FailureCode:      string(resposta.CodigoFalha),
	}

	// O saldo so vai na resposta quando o caso de uso produziu um. Replay de
	// operacao pendente nao tem saldo porque nada foi decidido ainda.
	if resposta.Saldo.Valida() {
		valor := dinheiroDoDominio(resposta.Saldo)
		corpo.Balance = &valor
	}

	return corpo
}

// carteiraNoCorpo monta a resposta de carteira.
func carteiraNoCorpo(c wallet.Carteira) respostaCarteira {
	return respostaCarteira{
		ID:       c.ID().String(),
		PlayerId: c.Jogador().String(),
		Balance:  dinheiroDoDominio(c.Saldo()),
		Version:  c.Versao(),
		Currency: string(c.Moeda()),
	}
}

// transacaoParaCorpo monta a resposta de leitura de transacao.
func transacaoParaCorpo(t wagering.Transacao) respostaTransacao {
	corpo := respostaTransacao{
		Id:          t.ID().String(),
		ProviderId:  string(t.Provedor()),
		Kind:        string(t.Tipo()),
		State:       string(t.Estado()),
		FailureCode: string(t.CodigoFalha()),
	}

	if t.TransacaoExterna().Valida() {
		corpo.ExternalTransactionId = t.TransacaoExterna().String()
	}
	if t.Resultado().Valida() {
		valor := dinheiroDoDominio(t.Resultado())
		corpo.Result = &valor
	}
	if t.ReferenciaInterna().Valida() {
		corpo.ReferenceInternalId = t.ReferenciaInterna().String()
	}
	return corpo
}

// lerCorpo le e decodifica o corpo JSON.
//
// Um corpo que nao e JSON e 400 com o motivo, e nao 500: e problema do cliente.
func lerCorpo(w http.ResponseWriter, r *http.Request, destino any) error {
	if r.Body == nil {
		return fmt.Errorf("%w: corpo ausente", app.ErrRequisicaoInvalida)
	}

	// Content-Type obrigatorio em escrita impede um POST com corpo de outro
	// formato de ser interpretado como JSON. Sem essa checagem, um cliente que
	// manda form em vez de JSON receberia um erro de decodificacao, que e menos
	// claro do que "use application/json".
	if conteudo := r.Header.Get("Content-Type"); conteudo != "" &&
		!strings.HasPrefix(conteudo, "application/json") {
		return fmt.Errorf("%w: Content-Type deve ser application/json", app.ErrRequisicaoInvalida)
	}

	limitado := io.LimitReader(r.Body, tamanhoMaximoDeCorpo)
	dec := json.NewDecoder(limitado)

	if err := dec.Decode(destino); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: corpo vazio", app.ErrRequisicaoInvalida)
		}
		return fmt.Errorf("%w: corpo nao e JSON valido: %v", app.ErrRequisicaoInvalida, err)
	}
	return nil
}
