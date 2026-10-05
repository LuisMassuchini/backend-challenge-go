// Package httpapi e a borda HTTP do servico.
//
// A borda traduz tres coisas e nada mais: JSON em comando de caso de uso, erro de
// caso de uso em status HTTP e token em ator autorizado. Nenhuma regra de negocio
// mora aqui. Quando um handler precisa pensar em saldo, em idempotencia ou em
// reversao, a regra esta no lugar errado.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/obs"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
)

// dinheiroNoCorpo e o formato de valor que o contrato HTTP usa.
//
// O valor e string e nao numero por um motivo que ja custou dinheiro a muita
// aplicacao: um float de 64 bits nao representa 0.01 de forma exata, e o round
// trip da serializacao pode transformar 25.00 em 24.999999999999996. String
// mantem o texto que o cliente mandou e deixa a conversao exata para o tipo de
// dominio.
type dinheiroNoCorpo struct {
	// Amount e o valor decimal com duas casas.
	Amount string `json:"amount"`

	// Currency e o codigo ISO de tres letras.
	Currency string `json:"currency"`
}

// dinheiroParaDominio converte o valor do corpo.
//
// O erro sai daqui como erro de entrada da requisicao, e nao como erro de dominio:
// o valor malformado e problema do corpo, e o provedor precisa de um 400 com o
// caminho do campo, nao de um 500.
func dinheiroParaDominio(valor dinheiroNoCorpo) (money.Money, error) {
	m, err := money.Parse(valor.Amount, money.Currency(valor.Currency))
	if err != nil {
		return money.Money{}, fmt.Errorf("%w: money: %v", app.ErrRequisicaoInvalida, err)
	}
	return m, nil
}

// dinheiroDoDominio converte para o formato de resposta.
func dinheiroDoDominio(m money.Money) dinheiroNoCorpo {
	return dinheiroNoCorpo{Amount: m.Decimal(), Currency: string(m.Currency())}
}

// respostaErro e o corpo de erro do contrato.
//
// Os quatro campos existem para que o cliente decida o que fazer sem adivinhar:
// o codigo estavel para maquinas, o status para o transporte, o detalhe para o
// humano e o que remplir na resposta de 4xx.
type respostaErro struct {
	// Erro e o codigo estavel, distinguivel por maquina.
	Erro string `json:"error"`

	// Codigo de status HTTP repetido no corpo. E redundante e mesmo assim: um
	// intermediario que registra so o corpo ainda tem o status.
	Codigo int `json:"status"`

	// Detalhe explica o motivo em texto.
	Detalhe string `json:"detail,omitempty"`

	// Campo aponta o campo do corpo quando o problema e de conteudo.
	Campo string `json:"field,omitempty"`

	// CodigoDeFalha e o codigo do dominio, quando a recusa foi do negocio.
	//
	// Distingue "a requisicao esta errada" de "a requisicao esta certa e a regra
	// recusou", que sao coisas diferentes para o provedor decidir se corrige o
	// payload ou se espera deposito.
	CodigoDeFalha string `json:"failureCode,omitempty"`
}

// escreverErro responde com o status e o corpo correspondentes.
//
// A traducao de erro para status esta em uma funcao so, e nao espalhada pelos
// handlers, porque o contrato do enunciado pede que entrada invalida, conflito,
// recusa de negocio, pendencia e indisponibilidade sejam distinguiveis. Uma tabela em
// um lugar e o que garante que dois handlers nao classifiquem o mesmo erro de formas
// diferentes.
//
// O log da falha e aqui, e nao em cada handler, pelo mesmo motivo: um unico ponto de
// log garante que toda resposta de erro tem linha no log, com a mesma correlacao da
// requisicao. Um log por handler e um log que esquece o handler novo.
func escreverErro(ctx context.Context, w http.ResponseWriter, err error) {
	status, corpo := classificarErro(err)

	// A linha so sai a partir de 500. Um 422 por saldo insuficiente e a resposta
	// esperada de um jogo em andamento, e logar como erro transformaria o volume
	// normal de operacao em alarms -- e o alarme que ninguem acredita deixa de
	// avisar quando importa.
	if status >= http.StatusInternalServerError {
		obs.Log(obs.De(ctx)).Error("requisicao recusada por falha",
			"status", status,
			obs.ErroCom(err),
		)
	} else {
		obs.Log(obs.De(ctx)).Info("requisicao nao atendida",
			"status", status,
			"erro", corpo.Erro,
		)
	}

	responderJSON(w, status, corpo)
}

// classificarErro traduz um erro de caso de uso em status e corpo.
func classificarErro(err error) (int, respostaErro) {
	// A ordem das checagens importa: as mais especificas vem primeiro. FalhaDeRegra
	// embute o codigo de status que o dominio escolheu, e e preciso chegar nela
	// antes da traducao generica de nao autorizado.
	var falha *app.FalhaDeRegra
	if errors.As(err, &falha) {
		status := statusDaRegra(falha.Codigo)
		return status, respostaErro{
			Erro:          "regra_de_negocio_recusou",
			Codigo:        status,
			Detalhe:       falha.Motivo,
			CodigoDeFalha: string(falha.Codigo),
		}
	}

	// Recusa de dominio que nao passou pela traducao ainda chega aqui, e recebe o
	// mesmo tratamento. E a rede de seguranca que impede que uma recusa nova vire
	// 500 por esquecimento.
	var recusada *wagering.FalhaDeRegra
	if errors.As(err, &recusada) {
		return http.StatusUnprocessableEntity, respostaErro{
			Erro:          "regra_de_negocio_recusou",
			Codigo:        http.StatusUnprocessableEntity,
			Detalhe:       recusada.Motivo,
			CodigoDeFalha: string(recusada.Codigo),
		}
	}

	switch {
	case errors.Is(err, app.ErrRequisicaoInvalida):
		return http.StatusBadRequest, respostaErro{
			Erro:    "requisicao_invalida",
			Codigo:  http.StatusBadRequest,
			Detalhe: err.Error(),
		}

	case errors.Is(err, app.ErrProvedorDivergente):
		// O token e valido mas nao serve para aquele provedor. E 403 e nao 401: a
		// credencial funcionou.
		return http.StatusForbidden, respostaErro{
			Erro:    "provedor_divergente",
			Codigo:  http.StatusForbidden,
			Detalhe: err.Error(),
		}

	case errors.Is(err, app.ErrNaoAutorizado):
		return http.StatusForbidden, respostaErro{
			Erro:    "nao_autorizado",
			Codigo:  http.StatusForbidden,
			Detalhe: err.Error(),
		}

	case errors.Is(err, app.ErrConflitoDeChave), errors.Is(err, pg.ErrConflitoDeChave):
		// A mesma chave de idempotencia com conteudo diferente e conflito. O indice
		// unico de carteira por jogador e moeda chega pelo mesmo erro, e tambem e
		// conflito: o cliente pediu algo que ja existe.
		return http.StatusConflict, respostaErro{
			Erro:    "conflito",
			Codigo:  http.StatusConflict,
			Detalhe: err.Error(),
		}

	case errors.Is(err, pg.ErrNaoEncontrado):
		return http.StatusNotFound, respostaErro{
			Erro:    "nao_encontrado",
			Codigo:  http.StatusNotFound,
			Detalhe: err.Error(),
		}

	case errors.Is(err, wallet.ErrSaldoInsuficiente), errors.Is(err, pg.ErrSaldoInsuficiente):
		// Saldo insuficiente e recusa de regra, nao erro de infraestrutura, e o
		// provedor trata esperando deposito.
		return http.StatusUnprocessableEntity, respostaErro{
			Erro:          "regra_de_negocio_recusou",
			Codigo:        http.StatusUnprocessableEntity,
			Detalhe:       err.Error(),
			CodigoDeFalha: string(wagering.CodigoFalhaSemSaldo),
		}

	case errors.Is(err, pg.ErrInvarianteViolada):
		// O banco recusou o que o codigo pediu. Isso e falha nossa, nao do cliente.
		return http.StatusInternalServerError, respostaErro{
			Erro:    "erro_interno",
			Codigo:  http.StatusInternalServerError,
			Detalhe: err.Error(),
		}

	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		// Indisponibilidade transitoria e o unico caso que o cliente deve repetir.
		return http.StatusServiceUnavailable, respostaErro{
			Erro:    "indisponivel",
			Codigo:  http.StatusServiceUnavailable,
			Detalhe: "a requisicao excedeu o prazo; repetir e seguro quando a operacao tem chave de idempotencia",
		}
	}

	return http.StatusInternalServerError, respostaErro{
		Erro:    "erro_interno",
		Codigo:  http.StatusInternalServerError,
		Detalhe: err.Error(),
	}
}

// statusDaRegra escolhe o status a partir do codigo de regra.
//
// Nem toda recusa e 422. O enunciado pede que pendencia e recusa sejam
// distinguiveis, e a referencia ausente cai em 202: nao houve recusa, o trabalho
// esta esperando um dado que ainda vai chegar.
func statusDaRegra(codigo wagering.CodigoFalha) int {
	if codigo == wagering.CodigoFalhaReferenciaNaoEncontrada {
		return http.StatusAccepted
	}
	return http.StatusUnprocessableEntity
}

// responderJSON escreve a resposta em JSON.
func responderJSON(w http.ResponseWriter, status int, corpo any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if corpo == nil {
		return
	}
	// O erro de escrita aqui nao pode ser tratado: a resposta ja foi iniciada e
	// nao ha mais canal para corrigir. Perder bytes e melhor que tentar escrever
	// um segundo cabecalho.
	//nolint:errcheck
	json.NewEncoder(w).Encode(corpo)
}
