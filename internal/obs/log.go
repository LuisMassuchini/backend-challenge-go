// Package obs e a observabilidade: log correlacionado e metricas.
//
// A divisao em um pacote e o que permite ao resto do sistema registrar sem conhecer
// o formato de saida. Um caso de uso que chamasse `slog.Info` direto nao teria como
// carregar a correlacao sem depender de HTTP, e um worker que montasse o log na mao
// escreveria as chaves errado em metade das linhas.
//
// DECISAO DE PRODUTO, registrada aqui e no ARCHITECTURE.md: o log correlacionado e
// as metricas sao entregues **no comeco da E16**, e nao no fim dela, depois de
// observabilidade. A ordem invertida foi pedida pelo usuario por intermedio, para
// que o projeto seja entregue mais rapido. E a unica forma de ver, ainda na E16, se o
// log correlacionado ja ajuda a diagnosticar um problema do proprio relay e do
// consumidor de fila que a E15 e a E13 empacotaram.
//
// O preco e conhecido e aceito, e vale deixar escrito: o log correlacionado e
// codificado contra o que existe hoje, e um ponto de log novo pode nao pegar a
// correlacao enquanto os pontos antigos continuarem sem ela. A alternativa -- adiar
// para depois da E17 -- teria custado etapas de depuracao sem correlacao em cima de
// codigo que existe justamente para servir de objeto a essas depuracoes.
package obs

import (
	"context"
	"log/slog"
)

// Identificadores que viajam no contexto.
//
// Sao os cinco que o enunciado nomeia. Cada um vira um campo do log JSON com o mesmo
// nome, e e essa correspondencia entre nome de estrutura e nome de campo que faz a
// busca por texto funcionar sem um mapa de traducao.
const (
	chaveCorrelacao = "correlationId"
	chaveMensagem   = "messageId"
	chaveTransacao  = "transactionId"
	chaveCarteira   = "walletId"
	chaveProvedor   = "providerId"
	chaveMovimento  = "movimento"
	chaveEvento     = "eventId"
)

// tamanhoMaximoDeValor limita um identificador gravado no log.
//
// O teto existe porque identificador externo vem do cliente, e o volume de log tem de
// crescer com o que o servico fez e nao com o que o cliente mandou. 256 e folgado para
// qualquer identificador real e curto para um corpo de requisicao.
const tamanhoMaximoDeValor = 256

// Contexto e o log com os identificadores de um fluxo.
//
// E value type e nao interface porque ele e construido em cadeia --
// `De(ctx).ComCorrelacao(c).ComCarteira(w)` -- e um ponteiro nesse meio exigiria
// `nil` em cada etapa. Cada metodo devolve o Contexto ja com o campo a mais, e o
// valor zero e usavel: quem nao tem nenhum identificador ainda registra.
type Contexto struct {
	// pai e o contexto de cancelamento da operacao.
	//
	// Guardar os identificadores aqui e nao no pai e deliberado: os workers criam
	// Contexto a partir de um contexto que sera cancelado no shutdown, e se a leitura
	// dos identificadores passasse pelo pai, um log emitido logo apos o cancelamento
	// perderia a correlacao.
	pai context.Context

	// campos sao os identificadores ja conhecidos, na ordem em que foram
	// preenchidos.
	//
	// E slice e nao mapa porque a ordem das chaves no JSON de log e o que torna duas
	// linhas do mesmo fluxo visualmente iguais, e um mapa daria ordem aleatoria.
	campos []campo
}

// campo e um par chave-valor do log.
type campo struct {
	chave string
	valor string
}

// De extrai os identificadores de um contexto de operacao.
//
// Aceita nil de proposito: `De(nil)` tem que funcionar, porque ha log antes da
// primeira requisicao -- o relay subindo, o consumidor se anunciando.
//
// A correlacao anexada ao contexto entra sozinha, e e o que faz a rastreabilidade
// atravessar a borda sem que cada camada passe o valor adiante. Os demais
// identificadores sao acrescentados com `ComX`, porque quem os conhece e quem lida
// com aquele dado: o caso de uso sabe a carteira, o consumidor sabe a mensagem.
//
// A correlacao do contexto so entra se o Contexto ainda nao tem uma. O consumidor
// anexa a chave de idempotencia como fallback, e sem este cuidado o `De` sobrescreve
// a correlacao do cliente -- e o log da operacao responderia a um identificador que o
// cliente nunca viu.
func De(ctx context.Context) Contexto {
	if ctx == nil {
		return Contexto{pai: context.Background()}
	}

	contexto := Contexto{pai: ctx}
	if contexto.Correlacao() != "" {
		return contexto
	}
	if correlacao, ok := ctx.Value(chaveDeCorrelacao{}).(string); ok {
		contexto = contexto.ComCorrelacao(correlacao)
	}
	return contexto
}

// chaveDeCorrelacao e a chave de contexto da correlacao.
//
// E um tipo proprio e nao uma string: duas strings com o mesmo nome em pacotes
// diferentes colidiriam silenciosamente, e o sintoma seria um log sem correlacao em
// uma borda e com correlacao em outra.
type chaveDeCorrelacao struct{}

// AnexarCorrelacao coloca a correlacao no contexto de cancelamento.
//
// E a porta de entrada da rastreabilidade: a borda HTTP chama uma vez, e todo log
// subsequente le o valor daqui.
func AnexarCorrelacao(ctx context.Context, correlacao string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if correlacao == "" {
		return ctx
	}
	return context.WithValue(ctx, chaveDeCorrelacao{}, correlacao)
}

// CorrelacaoDoContexto devolve a correlacao de um contexto, ou string vazia.
func CorrelacaoDoContexto(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	correlacao, _ := ctx.Value(chaveDeCorrelacao{}).(string)
	return correlacao
}

// Anulado devolve o contexto de cancelamento.
//
// Quem chama precisa dele para passar para o proximo passo: e o contexto que o
// shutdown cancela, e perder a ligacao com ele faria o trabalho continuar depois do
// SIGTERM.
func (c Contexto) Anulado() context.Context {
	if c.pai == nil {
		return context.Background()
	}
	return c.pai
}

// Correlacao devolve o identificador de correlacao, ou string vazia.
func (c Contexto) Correlacao() string { return c.valor(chaveCorrelacao) }

// valor procura um campo.
func (c Contexto) valor(chave string) string {
	for _, campo := range c.campos {
		if campo.chave == chave {
			return campo.valor
		}
	}
	return ""
}

// com acrescenta um campo.
//
// Valor vazio nao vira campo. A razao e o custo de busca: um campo presente e vazio em
// toda linha de quem nao tem o dado polui a consulta e faz o operador desconfiar do
// campo. Ausente e diferente de vazio, e o agregador trata os dois de forma diferente.
func (c Contexto) com(chave, valor string) Contexto {
	if valor == "" {
		return c
	}
	if len(valor) > tamanhoMaximoDeValor {
		valor = valor[:tamanhoMaximoDeValor]
	}

	// Substitui em vez de acrescentar quando o campo ja existe: uma operacao que
	// resolve a transacao depois de um log com a transacao pendente deve ter um
	// campo so, com o valor final.
	for i, existente := range c.campos {
		if existente.chave == chave {
			campos := make([]campo, len(c.campos))
			copy(campos, c.campos)
			campos[i] = campo{chave: chave, valor: valor}
			return Contexto{pai: c.Anulado(), campos: campos}
		}
	}

	campos := make([]campo, 0, len(c.campos)+1)
	campos = append(campos, c.campos...)
	campos = append(campos, campo{chave: chave, valor: valor})

	return Contexto{pai: c.Anulado(), campos: campos}
}

// ComCorrelacao acrescenta o identificador de correlacao do fluxo.
func (c Contexto) ComCorrelacao(correlacao string) Contexto {
	return c.com(chaveCorrelacao, correlacao)
}

// ComMensagem acrescenta o identificador da mensagem SQS.
func (c Contexto) ComMensagem(mensagem string) Contexto {
	return c.com(chaveMensagem, mensagem)
}

// ComTransacao acrescenta o identificador da transacao de wagering.
func (c Contexto) ComTransacao(transacao string) Contexto {
	return c.com(chaveTransacao, transacao)
}

// ComCarteira acrescenta o identificador da carteira.
func (c Contexto) ComCarteira(carteira string) Contexto {
	return c.com(chaveCarteira, carteira)
}

// ComProvedor acrescenta o identificador do provedor.
func (c Contexto) ComProvedor(provedor string) Contexto {
	return c.com(chaveProvedor, provedor)
}

// ComMovimento acrescenta a direcao do movimento.
//
// E o campo que substitui o valor no log: "houve debito" responde o que o operador
// pergunta sem dizer quanto. O enunciado proibe log de payload financeiro completo, e
// este e o unico jeito de falar de dinheiro no log sem valor.
func (c Contexto) ComMovimento(direcao string) Contexto {
	return c.com(chaveMovimento, direcao)
}

// ComEvento acrescenta o identificador do evento.
//
// E o quinto identificador que a rastreabilidade do relay precisa. A chave de
// idempotencia da operacao nao serve aqui: o relay nao conhece a operacao, ele
// conhece o evento. O `eventId` e o que sobrevive a republicacao, e por isso que e o
// identificador que o operador usa para acompanhar um evento que falha ao sair.
func (c Contexto) ComEvento(evento string) Contexto {
	return c.com(chaveEvento, evento)
}

// Log devolve o logger com os identificadores ja anexados.
//
// Os identificadores vao por `With`, e nao como argumento solto da chamada. A
// diferenca aparece quando o log e guardado antes dos campos existirem: `With` fixa
// os campos no logger e `Log(ctx)` continua funcionando depois, que e o caso comum
// quando o log e montado no worker e emitido no caso de uso.
//
// Atencao ao chamar `Log(ctx).Info(mensagem, argumentos...)`: nos argumentos da
// chamada o ultimo par a aparecer vence o `With`, e um par explicito com
// `correlationId` errado sobrescreveria a correlacao -- e perderia justamente a linha
// que o cliente usaria para achar o problema. Quando os argumentos vem em fatia, use
// `LogCom`, que resolve isso.
func Log(c Contexto) *slog.Logger {
	if len(c.campos) == 0 {
		return slog.Default()
	}
	return slog.Default().With(c.comoArgumentos()...)
}

// LogCom emite uma linha com os identificadores do contexto e os argumentos da
// chamada.
//
// O contexto vence o par explicito com a mesma chave, e a razao e defesa da
// rastreabilidade: um `correlationId` explicito errado perderia a busca pela
// correlacao do cliente justamente na linha que descreve o defeito.
//
// A precedencia vem de remover o par conflitante dos argumentos antes de juntar o
// contexto, e nao de repetir a chave. O `slog` aceita chave repetida e emite as duas no
// JSON, e o agregador usa a ultima -- funciona, mas deixa `correlationId` duplicado na
// linha, o que confunde quem le e quem parseia.
func LogCom(c Contexto, mensagem string, argumentos ...any) {
	conhecidos := c.chavesConhecidas()

	filtrados := make([]any, 0, len(argumentos)+len(c.campos)*2)
	for i := 0; i+1 < len(argumentos); i += 2 {
		chave, _ := argumentos[i].(string)
		if _, conflito := conhecidos[chave]; conflito {
			continue
		}
		filtrados = append(filtrados, argumentos[i], argumentos[i+1])
	}
	// Um argumento final sem o par vem de quem montou a fatia errada. Preservar e
	// melhor do que descartar: o log ainda sai, e a linha mostra o que houve.
	if len(argumentos)%2 == 1 {
		filtrados = append(filtrados, argumentos[len(argumentos)-1])
	}

	slog.Info(mensagem, append(filtrados, c.comoArgumentos()...)...)
}

// chavesConhecidas devolve o conjunto de chaves que o contexto preenche.
func (c Contexto) chavesConhecidas() map[string]struct{} {
	conjunto := make(map[string]struct{}, len(c.campos))
	for _, campo := range c.campos {
		conjunto[campo.chave] = struct{}{}
	}
	return conjunto
}

// comoArgumentos devolve os campos do contexto como pares chave-valor.
func (c Contexto) comoArgumentos() []any {
	argumentos := make([]any, 0, len(c.campos)*2)
	for _, campo := range c.campos {
		argumentos = append(argumentos, campo.chave, campo.valor)
	}
	return argumentos
}

// MsgCom devolve um par chave-valor para log montado a mao.
//
// Existe para o log de quem nao tem Contexto -- o relay e o consumidor na subida -- e
// para as etiquetas de contagem. O ponto e que a chave sai do parametro e o valor do
// valor, sem que o chamador precise lembrar a ordem.
func MsgCom(chave string, valor any) slog.Attr {
	return slog.Any(chave, valor)
}

// ErroCom devolve o par de um erro.
//
// Existe para o log de falha ter sempre a chave `erro`. Um agregador que indexa por
// chave encontra toda falha no mesmo campo, e nao em `err`, `error` e `motivo`, que e
// como o log cresce quando cada chamador inventa o nome.
func ErroCom(err error) slog.Attr {
	if err == nil {
		return slog.String("erro", "")
	}
	return slog.String("erro", err.Error())
}

// Duracao devolve o par de uma duracao em milissegundos.
//
// A unidade e milissegundo porque e a que o operador le sem calculo, e porque o log
// fica comparavel com a latencia da metrica, que tambem e em milissegundos.
func Duracao(chave string, d int64) slog.Attr {
	return slog.Int64(chave, d)
}
