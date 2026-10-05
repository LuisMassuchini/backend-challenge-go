// Package sqs e o cliente da fila.
//
// Existe como pacote proprio porque o resto do sistema nao deve conhecer a API da
// AWS. O consumidor fala em mensagens e em receipts; quem traduz e este pacote.
package sqs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Erros do cliente.
var (
	// ErrFilaInexistente e a fila pedida que nao existe.
	ErrFilaInexistente = errors.New("sqs: fila inexistente")

	// ErrMensagemInvalida e corpo de mensagem que nao e o JSON esperado.
	ErrMensagemInvalida = errors.New("sqs: corpo da mensagem invalido")
)

// Cliente fala com o SQS.
type Cliente struct {
	// api e o cliente da SDK.
	api *sqs.Client

	// urlOperacoes e o endereco da fila de operacoes.
	urlOperacoes string

	// urlCartaoMorto e o endereco da fila de cartao morto.
	urlCartaoMorto string
}

// Opcoes e a configuracao do cliente.
type Opcoes struct {
	// Endpoint e o endereco do SQS. Vazio usa o endpoint real da AWS.
	Endpoint string

	// Regiao e a regiao de assinatura.
	Regiao string

	// ChaveDeAcesso e o identificador de credencial.
	ChaveDeAcesso string

	// SegredoDeAcesso e o segredo da credencial.
	SegredoDeAcesso string

	// FilaOperacoes e o nome da fila de operacoes.
	FilaOperacoes string

	// FilaDeadLetter e o nome da fila de cartao morto.
	FilaDeadLetter string

	// EsperaMaxima e o limite de espera do long polling.
	//
	// O limite existe porque o SDK aumentaria o valor e a chamada passaria a esperar
	// muito mais que o tempo de espera do ciclo, e um worker que para de percorrer a
	// fila no shutdown e um worker que perde mensagens ate o timeout de visibilidade
	// expirar.
	EsperaMaxima time.Duration
}

// Novo constroi o cliente.
func Novo(ctx context.Context, o Opcoes) (*Cliente, error) {
	if o.FilaOperacoes == "" || o.FilaDeadLetter == "" {
		return nil, fmt.Errorf("%w: os dois nomes de fila sao obrigatorios", ErrFilaInexistente)
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(o.Regiao),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			o.ChaveDeAcesso, o.SegredoDeAcesso, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("sqs: configuracao: %w", err)
	}

	api := sqs.NewFromConfig(cfg, func(opcoes *sqs.Options) {
		if o.Endpoint != "" {
			// O endpoint do LocalStack e explicito porque a assinatura e feita para a
			// AWS real, e sem ele o SDK tentaria resolver o host publico e o teste
			// passaria a depender da internet.
			opcoes.BaseEndpoint = &o.Endpoint
		}
	})

	cliente := &Cliente{
		api:            api,
		urlOperacoes:   "",
		urlCartaoMorto: "",
	}

	if cliente.urlOperacoes, err = cliente.resolverURL(ctx, o.FilaOperacoes); err != nil {
		return nil, err
	}
	if cliente.urlCartaoMorto, err = cliente.resolverURL(ctx, o.FilaDeadLetter); err != nil {
		return nil, err
	}

	return cliente, nil
}

// resolverURL descobre o endereco de uma fila pelo nome.
func (c *Cliente) resolverURL(ctx context.Context, nome string) (string, error) {
	resposta, err := c.api.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: &nome})
	if err != nil {
		if errosDeFilaInexistente(err) {
			return "", fmt.Errorf("%w: %s", ErrFilaInexistente, nome)
		}
		return "", fmt.Errorf("sqs: endereco de %s: %w", nome, err)
	}
	return *resposta.QueueUrl, nil
}

// Mensagem e uma mensagem recebida.
type Mensagem struct {
	// ID e o identificador da mensagem na fila.
	ID string

	// Corpo e o conteudo enviado pelo produtor.
	Corpo string

	// ReceiptHandle e o recibo que autoriza apagar ou estender a visibilidade.
	//
	// E o que impede que um consumidor apague a mensagem que outro esta
	// processando: cada recebimento gera um recibo proprio, e apagar exige o recibo
	// daquele recebimento.
	ReceiptHandle string

	// Tentativas e quantas vezes a mensagem ja foi recebida.
	//
	// Vem do receive count da fila e e o que permite ao consumidor decidir se vale
	// tentar de novo ou se a mensagem ja falhou vezes demais.
	Tentativas int

	// ChaveDeParticao e o MessageGroupId.
	//
	// E o que define a ordem: em uma fila FIFO, mensagens do mesmo grupo nunca saem
	// em paralelo. E por isso que a chave de particao e a carteira, e nao o provedor:
	// duas apostas na mesma carteira precisam ser aplicadas em ordem.
	ChaveDeParticao string
}

// Receber busca ate o limite de mensagens.
//
// Uma fila vazia devolve lista vazia sem erro. Distinguir "vazia" de "erro" aqui
// importa porque o long polling devolve vazio por desenho quando nada chega no
// tempo, e tratar isso como falha derrubaria o consumidor a cada ciclo ocioso.
func (c *Cliente) Receber(ctx context.Context, limite int32) ([]Mensagem, error) {
	limiteInt32 := limite
	if limiteInt32 < 1 {
		limiteInt32 = 1
	}
	if limiteInt32 > 10 {
		// O SQS limita a dez por chamada. Pedir mais nao da erro, devolve dez, e o
		// consumidor acharia que a fila esvaziou.
		limiteInt32 = 10
	}

	saida, err := c.api.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            &c.urlOperacoes,
		MaxNumberOfMessages: limiteInt32,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameMessageGroupId,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("sqs: recebimento: %w", err)
	}

	mensagens := make([]Mensagem, 0, len(saida.Messages))
	for _, bruta := range saida.Messages {
		mensagem := Mensagem{
			ID:            awsBruta(bruta.MessageId),
			Corpo:         awsBruta(bruta.Body),
			ReceiptHandle: awsBruta(bruta.ReceiptHandle),
		}
		if bruta.Attributes != nil {
			mensagem.Tentativas = lerTentativas(bruta.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
		}
		if bruta.MessageAttributes != nil {
			mensagem.ChaveDeParticao = bruta.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		}
		mensagens = append(mensagens, mensagem)
	}
	return mensagens, nil
}

// Concluir apaga a mensagem.
//
// Apagar so depois que o efeito ja foi confirmado e o que fecha o ciclo: enquanto a
// mensagem esta visivel, ela volta; apagada, o SQS esquece. Se o processo morrer
// entre confirmar no banco e apagar, a mensagem volta e o consumidor encontra o
// registro na inbox e devolve o resultado sem mover dinheiro de novo.
func (c *Cliente) Concluir(ctx context.Context, recibo string) error {
	entrada := &sqs.DeleteMessageInput{
		QueueUrl:      &c.urlOperacoes,
		ReceiptHandle: &recibo,
	}
	if _, err := c.api.DeleteMessage(ctx, entrada); err != nil {
		return fmt.Errorf("sqs: apagando a mensagem: %w", err)
	}
	return nil
}

// EstenderVisibilidade segura a mensagem por mais tempo.
//
// E o escape para o processamento que passou do tempo normal sem ter falhado. Sem
// isso, uma operacao lenta veria a mensagem ser entregue a outra instancia no meio
// do processamento, e as duas moveriam dinheiro. A segunda encontraria a chave de
// idempotencia e devolveria o resultado da primeira, entao o dinheiro nao se move
// duas vezes, mas o trabalho e feito duas vezes e a primeira ainda pode perder o
// direito de apagar a mensagem, cujo recibo ja expirou.
func (c *Cliente) EstenderVisibilidade(ctx context.Context, recibo string, por time.Duration) error {
	segundos := int32(por.Seconds())
	if segundos < 1 {
		segundos = 1
	}

	entrada := &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          &c.urlOperacoes,
		ReceiptHandle:     &recibo,
		VisibilityTimeout: segundos,
	}
	if _, err := c.api.ChangeMessageVisibility(ctx, entrada); err != nil {
		return fmt.Errorf("sqs: estendendo a visibilidade: %w", err)
	}
	return nil
}

// Publicar envia uma mensagem para a fila de operacoes.
//
// Em uma fila FIFO, o MessageGroupId define a ordem e e obrigatorio, e o
// MessageDeduplicationId e o que impede a duplicata em uma janela de cinco minutos.
// A deduplicacao por conteudo fica desligada na fila porque a chave de idempotencia
// ja vem no corpo e no cabecalho: a deduplicacao do SQS e uma janela de cinco
// minutos, e uma operacao pode ser reenviada com a mesma chave muito depois, quando
// essa janela ja passou.
func (c *Cliente) Publicar(ctx context.Context, grupo, deduplicacao string, segundos int32, corpo string) error {
	entrada := &sqs.SendMessageInput{
		QueueUrl:    &c.urlOperacoes,
		MessageBody: &corpo,
		// FIFO nao aceita parametro de atraso: a ordem se perderia. O atraso se
		// resolve no produtor, que nao envia antes da hora.
		MessageGroupId:         &grupo,
		MessageDeduplicationId: &deduplicacao,
	}
	if _, err := c.api.SendMessage(ctx, entrada); err != nil {
		return fmt.Errorf("sqs: envio: %w", err)
	}
	return nil
}

// Existe informa se a fila responde.
//
// E o que o health check de readiness consulta: um consumidor sem fila nao tem o
// que consumir, e reportar pronto seria mentira.
func (c *Cliente) Existe(ctx context.Context) error {
	entrada := &sqs.GetQueueAttributesInput{
		QueueUrl:       &c.urlOperacoes,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	}
	if _, err := c.api.GetQueueAttributes(ctx, entrada); err != nil {
		if errosDeFilaInexistente(err) {
			return fmt.Errorf("%w: %s", ErrFilaInexistente, "wager-transactions.fifo")
		}
		return fmt.Errorf("sqs: fila: %w", err)
	}
	return nil
}

// URL devolve o endereco da fila de operacoes, para os testes e o log.
func (c *Cliente) URL() string { return c.urlOperacoes }

// lerTentativas converte o contador de recebimentos da fila.
//
// Texto que nao e numero vira zero, e nao um erro: o contador e informacao para
// decidir se vale tentar de novo, e tratar um valor inesperado como falha
// descartaria a mensagem em vez de tentar.
func lerTentativas(bruto string) int {
	valor, err := strconv.Atoi(bruto)
	if err != nil || valor < 1 {
		return 1
	}
	return valor
}

// awsBruta desempacota um ponteiro de string da SDK.
//
// A SDK devolve ponteiro para string, e um campo opcional ausente vem nulo. Ler o
// ponteiro de uma string vazia e correto e ler um ponteiro nulo nao e, entao os dois
// casos caem no mesmo lugar.
func awsBruta(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// errosDeFilaInexistente diz se o erro e de fila inexistente.
func errosDeFilaInexistente(err error) bool {
	var inexistente *types.QueueDoesNotExist
	return errors.As(err, &inexistente)
}
