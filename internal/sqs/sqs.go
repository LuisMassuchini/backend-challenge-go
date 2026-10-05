// Package sqs e o cliente da fila.
//
// Existe como pacote proprio porque o resto do sistema nao deve conhecer a API da
// AWS. O consumidor fala em mensagens e em receipts; quem traduz e este pacote.
package sqs

import (
	"context"
	"encoding/json"
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
//
// Nao ha campo de long polling aqui, e a ausencia e deliberada. Havia um `EsperaMaxima`,
// preenchido pelo grafo com dez segundos, documentado como "o limite de espera do long
// polling" -- e nunca lido. `ReceiveMessage` nao montava `WaitTimeSeconds`, entao o campo
// nao controlava nada.
//
// Implementar o campo como estava teria sido uma REGRESSAO, e nao a correcao que o nome
// sugeria: o long polling de verdade ja vinha do atributo da fila, com vinte segundos em
// `deploy/localstack/init/00-filas.sh`, e um `WaitTimeSeconds` de dez na chamada da API
// SOBRESCREVE o atributo. O efeito seria trocar vinte por dez e mais requisicoes.
//
// O shutdown nao depende de teto de espera nenhum: `Receber` recebe o contexto do worker e
// a SDK aborta o long poll no cancelamento, e `Worker.Rodar` ainda confere `ctx.Err()`
// antes de cada chamada.
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

	// FilaEventos e o nome da fila de saida dos eventos.
	//
	// So o publicador usa este campo. O cliente de operacoes nao, porque ele nao
	// publica evento: quem publica evento e o relay, e ele tem o proprio cliente.
	FilaEventos string
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

// Publicador de eventos.
//
// E um cliente separado do de operacoes porque os dois destinos tem contratos
// diferentes: a fila de operacoes leva comandos de jogo e a de eventos leva envelope
// com eventId. Misturar os dois em um cliente so tornaria impossivel responder "qual
// fila esta atrasada" sem olhar o conteiro.
type Publicador struct {
	api *sqs.Client

	urlEventos string
}

// NovoPublicador constroi o publicador de eventos.
func NovoPublicador(ctx context.Context, o Opcoes) (*Publicador, error) {
	if o.FilaEventos == "" {
		return nil, fmt.Errorf("%w: o nome da fila de eventos e obrigatorio", ErrFilaInexistente)
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
			opcoes.BaseEndpoint = &o.Endpoint
		}
	})

	publicador := &Publicador{api: api}
	if publicador.urlEventos, err = publicador.resolverURL(ctx, o.FilaEventos); err != nil {
		return nil, err
	}
	return publicador, nil
}

// resolverURL descobre o endereco de uma fila pelo nome.
func (p *Publicador) resolverURL(ctx context.Context, nome string) (string, error) {
	resposta, err := p.api.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: &nome})
	if err != nil {
		if errosDeFilaInexistente(err) {
			return "", fmt.Errorf("%w: %s", ErrFilaInexistente, nome)
		}
		return "", fmt.Errorf("sqs: endereco de %s: %w", nome, err)
	}
	return *resposta.QueueUrl, nil
}

// Publicar envia um evento para a fila de saida.
//
// A chave de particao e o agregado do evento. E o que define a ordem na fila: eventos
// do mesmo agregado nao saem em paralelo, e o consumidor ve a transacao processada
// antes do saldo alterado, que e a ordem em que os fatos aconteceram.
//
// A deduplicacao do broker fica DESLIGADA de conteudo, e a MessageDeduplicationId leva o
// eventId. A diferenca importa: a deduplicacao por conteudo do SQS e uma janela de
// cinco minutos, e uma republicacao por falha de confirmacao pode acontecer muito
// depois. Com o eventId como chave, o broker absorve a republicacao dentro da janela
// e, fora dela, o consumidor reconhece o evento pelo eventId -- que e o contrato que
// o enunciado pede, e nao a deduplicacao do broker.
func (p *Publicador) Publicar(ctx context.Context, chaveDeParticao, corpo string) error {
	eventID, err := eventIDDoEnvelope(corpo)
	if err != nil {
		return err
	}

	entrada := &sqs.SendMessageInput{
		QueueUrl:               &p.urlEventos,
		MessageBody:            &corpo,
		MessageGroupId:         &chaveDeParticao,
		MessageDeduplicationId: &eventID,
	}
	if _, err := p.api.SendMessage(ctx, entrada); err != nil {
		return fmt.Errorf("sqs: envio de evento: %w", err)
	}
	return nil
}

// eventIDDoEnvelope extrai o eventId do envelope.
//
// E o MessageDeduplicationId, e nao o corpo inteiro: a deduplicacao do broker
// compara a chave que recebe, e o que precisa ser reconhecido como o mesmo evento
// entre duas republicacoes e a identidade dele, nao a serializacao. Usar o corpo
// inteiro faria a chave mudar se o envelope fosse re-serializado, e a republicacao
// viraria duplicata.
func eventIDDoEnvelope(corpo string) (string, error) {
	var envelope struct {
		EventID string `json:"eventId"`
	}
	if err := json.Unmarshal([]byte(corpo), &envelope); err != nil {
		return "", fmt.Errorf("%w: envelope nao e JSON: %v", ErrMensagemInvalida, err)
	}
	if envelope.EventID == "" {
		return "", fmt.Errorf("%w: envelope sem eventId", ErrMensagemInvalida)
	}
	return envelope.EventID, nil
}

// URL devolve o endereco da fila de saida, para o log e os testes.
func (p *Publicador) URL() string { return p.urlEventos }

// Existe informa se a fila de eventos responde.
func (p *Publicador) Existe(ctx context.Context) error {
	entrada := &sqs.GetQueueAttributesInput{
		QueueUrl:       &p.urlEventos,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	}
	if _, err := p.api.GetQueueAttributes(ctx, entrada); err != nil {
		if errosDeFilaInexistente(err) {
			return fmt.Errorf("%w: %s", ErrFilaInexistente, p.urlEventos)
		}
		return fmt.Errorf("sqs: fila de eventos: %w", err)
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
