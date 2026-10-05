package eventos

import (
	"fmt"
	"strings"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// Os quatro eventos que o enunciado exige, cada um com construtor proprio.
//
// O tipo do evento e decidido pelo construtor, nunca recebido como parametro. Um
// construtor que aceitasse o tipo permitiria um WagerTransactionProcessed com
// payload de rejeicao, e o consumidor processaria rejeicao como sucesso.

// DadosTransacaoProcessada sao os dados de entrada de WagerTransactionProcessed.
type DadosTransacaoProcessada struct {
	Transacao wallet.Identificador
	// OrigemInterna marca o evento que nao veio de provedor.
	//
	// Os eventos de origem interna -- a abertura de carteira -- nao tem provedor nem
	// identificador externo, e o enunciado diz que eles nao exigem os metadados
	// externos inaplicaveis. Sem esta marca, ou o construtor exigiria metadados que
	// nao existem, ou aceitaria um evento externo sem providerId e o consumidor nao
	// saberia de quem e o evento.
	OrigemInterna bool
	Provedor      string
	Externo       string
	Carteira      wallet.Identificador
	Tipo          string
	Resultado     money.Money
	Correlacao    string
	Causa         wallet.Identificador
	OcorridoEm    time.Time
}

// DadosTransacaoRejeitada sao os dados de entrada de WagerTransactionRejected.
type DadosTransacaoRejeitada struct {
	Transacao  wallet.Identificador
	Provedor   string
	Externo    string
	Carteira   wallet.Identificador
	Tipo       string
	Valor      money.Money
	Falha      string
	Correlacao string
	Causa      wallet.Identificador
	OcorridoEm time.Time
}

// DadosSaldoAlterado sao os dados de entrada de WalletBalanceChanged.
//
// O enunciado e explicito sobre o payload: walletId, transactionId, direcao,
// money, balanceBefore, balanceAfter e walletVersion. Todos os sete, sempre.
type DadosSaldoAlterado struct {
	Transacao      wallet.Identificador
	Carteira       wallet.Identificador
	Direcao        string
	Valor          money.Money
	SaldoAnterior  money.Money
	SaldoPosterior money.Money
	VersaoCarteira int64
	Correlacao     string
	Causa          wallet.Identificador
	OcorridoEm     time.Time
}

// DadosPendenteReferencia sao os dados de entrada de
// WagerTransactionPendingReference.
type DadosPendenteReferencia struct {
	Transacao  wallet.Identificador
	Provedor   string
	Externo    string
	Carteira   wallet.Identificador
	Tipo       string
	Referencia string
	Correlacao string
	Causa      wallet.Identificador
	OcorridoEm time.Time
}

// NovaTransacaoProcessada constroi o evento de operacao concluida com sucesso.
//
// O resultado e o saldo observado no processamento original. E ele que o replay
// devolve, e por isso vai no evento: quem recebe o evento precisa saber qual era o
// saldo naquele instante, e recalcular a partir do saldo de hoje daria outro
// numero.
func NovaTransacaoProcessada(d DadosTransacaoProcessada) (Evento, error) {
	if err := conferirComum(d.Transacao, d.Carteira, d.Correlacao, d.OcorridoEm); err != nil {
		return Evento{}, err
	}
	if !d.OrigemInterna {
		if err := conferirTexto("provedor", d.Provedor); err != nil {
			return Evento{}, err
		}
		if err := conferirTexto("transacao externa", d.Externo); err != nil {
			return Evento{}, err
		}
	}
	if err := conferirTexto("tipo", d.Tipo); err != nil {
		return Evento{}, err
	}
	if err := d.Resultado.Validar(); err != nil {
		return Evento{}, fmt.Errorf("%w: resultado: %v", ErrEnvelopeInvalido, err)
	}

	return novo(TipoTransacaoProcessada, d.Transacao, d.Correlacao, d.Causa, d.OcorridoEm, map[string]any{
		"transactionId":         d.Transacao.String(),
		"providerId":            d.Provedor,
		"externalTransactionId": d.Externo,
		"walletId":              d.Carteira.String(),
		"kind":                  d.Tipo,
		"status":                "PROCESSED",
		"balance":               d.Resultado.Decimal(),
		"currency":              string(d.Resultado.Currency()),
	})
}

// NovaTransacaoRejeitada constroi o evento de operacao recusada por regra.
//
// O codigo de falha e estavel e vai no payload com o nome failureCode: e o que o
// provedor usa para tratar a recusa, e um texto livre mudaria entre deploys.
func NovaTransacaoRejeitada(d DadosTransacaoRejeitada) (Evento, error) {
	if err := conferirComum(d.Transacao, d.Carteira, d.Correlacao, d.OcorridoEm); err != nil {
		return Evento{}, err
	}
	if err := conferirTexto("provedor", d.Provedor); err != nil {
		return Evento{}, err
	}
	if err := conferirTexto("transacao externa", d.Externo); err != nil {
		return Evento{}, err
	}
	if err := conferirTexto("tipo", d.Tipo); err != nil {
		return Evento{}, err
	}
	if err := d.Valor.Validar(); err != nil {
		return Evento{}, fmt.Errorf("%w: valor: %v", ErrEnvelopeInvalido, err)
	}
	if err := conferirTexto("codigo de falha", d.Falha); err != nil {
		return Evento{}, err
	}

	return novo(TipoTransacaoRejeitada, d.Transacao, d.Correlacao, d.Causa, d.OcorridoEm, map[string]any{
		"transactionId":         d.Transacao.String(),
		"providerId":            d.Provedor,
		"externalTransactionId": d.Externo,
		"walletId":              d.Carteira.String(),
		"kind":                  d.Tipo,
		"status":                "REJECTED",
		"failureCode":           d.Falha,
		"money":                 d.Valor.Decimal(),
		"currency":              string(d.Valor.Currency()),
	})
}

// NovaSaldoAlterado constroi o evento de mudanca efetiva de saldo.
//
// A coerencia entre direcao, valor e os dois saldos e conferida aqui, e nao
// apenas no lancamento do dominio: o consumidor deste evento nao vai refazer a
// conta, e um payload com saldo posterior errado vira divergencia na
// reconciliacao de quem consome.
func NovaSaldoAlterado(d DadosSaldoAlterado) (Evento, error) {
	if err := conferirComum(d.Transacao, d.Carteira, d.Correlacao, d.OcorridoEm); err != nil {
		return Evento{}, err
	}
	if d.Direcao != string(wallet.DirecaoDebito) && d.Direcao != string(wallet.DirecaoCredito) {
		return Evento{}, fmt.Errorf("%w: direcao %q", ErrEnvelopeInvalido, d.Direcao)
	}
	if !d.Valor.Valida() || !d.SaldoAnterior.Valida() || !d.SaldoPosterior.Valida() {
		return Evento{}, fmt.Errorf("%w: valor ou saldo nao inicializado", ErrEnvelopeInvalido)
	}
	if d.Valor.Currency() != d.SaldoAnterior.Currency() || d.Valor.Currency() != d.SaldoPosterior.Currency() {
		return Evento{}, fmt.Errorf("%w: moedas divergentes no evento de saldo", ErrEnvelopeInvalido)
	}
	if d.VersaoCarteira < 1 {
		return Evento{}, fmt.Errorf("%w: versao da carteira %d", ErrEnvelopeInvalido, d.VersaoCarteira)
	}

	var esperado money.Money
	var err error
	if wallet.Direcao(d.Direcao) == wallet.DirecaoCredito {
		esperado, err = d.SaldoAnterior.Add(d.Valor)
	} else {
		esperado, err = d.SaldoAnterior.Sub(d.Valor)
	}
	if err != nil {
		return Evento{}, fmt.Errorf("%w: %v", ErrEnvelopeInvalido, err)
	}
	if !esperado.Equal(d.SaldoPosterior) {
		return Evento{}, fmt.Errorf(
			"%w: %s %s sobre %s resulta em %s e nao em %s",
			ErrEnvelopeInvalido, d.Direcao, d.Valor, d.SaldoAnterior, esperado, d.SaldoPosterior,
		)
	}

	return novo(TipoSaldoAlterado, d.Transacao, d.Correlacao, d.Causa, d.OcorridoEm, map[string]any{
		"walletId":      d.Carteira.String(),
		"transactionId": d.Transacao.String(),
		"direction":     d.Direcao,
		"money":         d.Valor.Decimal(),
		"currency":      string(d.Valor.Currency()),
		"balanceBefore": d.SaldoAnterior.Decimal(),
		"balanceAfter":  d.SaldoPosterior.Decimal(),
		"walletVersion": d.VersaoCarteira,
	})
}

// NovaPendenteReferencia constroi o evento de espera por referencia.
//
// A referencia externa vai no payload porque e ela que o consumidor e o operador
// usam para reconciliar: sem ela, o evento diz apenas que algo falta.
func NovaPendenteReferencia(d DadosPendenteReferencia) (Evento, error) {
	if err := conferirComum(d.Transacao, d.Carteira, d.Correlacao, d.OcorridoEm); err != nil {
		return Evento{}, err
	}
	if err := conferirTexto("provedor", d.Provedor); err != nil {
		return Evento{}, err
	}
	if err := conferirTexto("transacao externa", d.Externo); err != nil {
		return Evento{}, err
	}
	if err := conferirTexto("tipo", d.Tipo); err != nil {
		return Evento{}, err
	}
	// A referencia e obrigatoria no evento: um evento de espera sem saber o que
	// se espera nao serve para nada, nem para o consumidor nem para o operador.
	if err := conferirTexto("referencia externa", d.Referencia); err != nil {
		return Evento{}, err
	}

	return novo(TipoPendenteReferencia, d.Transacao, d.Correlacao, d.Causa, d.OcorridoEm, map[string]any{
		"transactionId":                  d.Transacao.String(),
		"providerId":                     d.Provedor,
		"externalTransactionId":          d.Externo,
		"walletId":                       d.Carteira.String(),
		"kind":                           d.Tipo,
		"status":                         "PENDING_REFERENCE",
		"referenceExternalTransactionId": d.Referencia,
	})
}

// novo monta o evento com o tipo informado. E o ponto unico por onde os quatro
// construtores passam, e por isso que nenhum deles precisa repetir a conferida de
// envelope.
func novo(
	tipo Tipo,
	agregado wallet.Identificador,
	correlacao string,
	causa wallet.Identificador,
	ocorridoEm time.Time,
	dados map[string]any,
) (Evento, error) {
	return Novo(TransacaoProcessada{
		Agregado:   agregado,
		Correlacao: correlacao,
		Causa:      causa,
		OcorridoEm: ocorridoEm,
		Dados:      dados,
		comTipo:    tipo,
	})
}

// conferirComum valida o que os quatro eventos exigem.
func conferirComum(transacao, carteira wallet.Identificador, correlacao string, ocorridoEm time.Time) error {
	if !transacao.Valida() {
		return fmt.Errorf("%w: transacao ausente", ErrEnvelopeInvalido)
	}
	if !carteira.Valida() {
		return fmt.Errorf("%w: carteira ausente", ErrEnvelopeInvalido)
	}
	if strings.TrimSpace(correlacao) == "" {
		return fmt.Errorf("%w: correlacao ausente", ErrEnvelopeInvalido)
	}
	if ocorridoEm.IsZero() {
		return fmt.Errorf("%w: instante do fato ausente", ErrEnvelopeInvalido)
	}
	return nil
}

// conferirTexto exige texto nao vazio.
func conferirTexto(campo, valor string) error {
	if strings.TrimSpace(valor) == "" {
		return fmt.Errorf("%w: %s ausente", ErrEnvelopeInvalido, campo)
	}
	return nil
}
