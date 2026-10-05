package obs

import (
	"strconv"
	"time"
)

// Metricas e o conjunto nomeado que o enunciado exige.
//
// Existe como struct, e nao como uma lista de `Contador` soltos pelo codigo, por dois
// motivos. O primeiro e o uso: quem mede chama `m.Operacoes.Inc("estado", ...)` em vez
// de lembrar o nome e a ajuda da metrica. O segundo e o inventario: a lista do
// enunciado cabe em uma pagina aqui, e uma metrica que ninguem lembra de declarar
// nunca e declarada.
//
// Todos os rotulos aqui sao de conjunto fechado. `providerId`, `walletId` e
// `transactionId` sao recusados pelo Registro, e o motivo esta em
// `nomesDeRotuloProibidos`: cardinalidade sem limite derruba o Prometheus.
type Metricas struct {
	// Operacoes conta desfecho por estado.
	//
	// O estado e o rotulo porque e o que o enunciado pede e porque o conjunto e
	// fechado: PENDING, PENDING_REFERENCE, PROCESSED, REJECTED e FAILED. Um
	// `kind` de operacao entraria aqui como segundo rotulo quando fizer sentido.
	Operacoes *Contador

	// Duplicatas conta reentregas reconhecidas pela idempotencia.
	//
	// E a metrica que prova a garantia em producao: um numero alto e o comportamento
	// esperado do at-least-once, e um numero zero com trafego real significaria que a
	// deteccao de replay nao esta funcionando.
	Duplicatas *Contador

	// ConflitosChave conta chave de idempotencia reusada com conteudo diferente.
	//
	// E o sinal de que um cliente mudou o payload mantendo a chave. Differe de
	// duplicata: aqui o problema e do cliente, e a operacao nao foi aplicada.
	ConflitosChave *Contador

	// Retentativas conta as falhas que voltaram para a fila.
	Retentativas *Contador

	// MensagensFilaMorta conta as mensagens que esgotaram as tentativas.
	MensagensFilaMorta *Contador

	// ConflitosLock conta as transacoes que perderam a disputa pelo lock da carteira.
	//
	// E o que mostra que a concorrencia existe e esta sendo resolvida por lock e nao
	// por perdida. Um numero alto e o enunciado cumprindo; zero com concorrencia
	// significaria que ninguem esta disputando ou que o lock sumiu.
	ConflitosLock *Contador

	// AtrasoOutbox e o maior atraso, em segundos, entre o instante do evento e o da
	// publicacao.
	//
	// E gauge e nao contador porque quando a fila esvazia o atraso e zero. Um contador
	// nunca voltaria a zero, e um contador que nunca volta a zero e um contador que
	// ninguem consegue ler.
	AtrasoOutbox *Gauge

	// EventosPublicados conta eventos publicados pelo relay.
	EventosPublicados *Contador

	// EventosDesistidos conta eventos que esgotaram as tentativas de publicacao.
	//
	// Differe de `MensagensFilaMorta` em proposito: um e evento que nao conseguiu
	// sair do sistema, o outro e mensagem que nao conseguiu ser consumida. Sao
	// falhas em lados opostos do broker e precisam de alarmes separados.
	EventosDesistidos *Contador

	// Divergencias conta reconciliacoes que encontraram saldo diferente do ledger.
	//
	// E a metrica mais grave do conjunto: ela sozinha invalida a integridade
	// financeira do processo.
	Divergencias *Contador

	// LatenciaOperacao mede quanto a operacao levou, por desfecho.
	//
	// O histograma e o que responde p95 e p99, que o enunciado pede no relatorio de
	// carga. Os buckets vao ate cinco segundos porque e o teto de `statement_timeout`
	// do papel de runtime: uma operacao acima disso falhou, e o bucket `+Inf` e onde
	// ela cai.
	LatenciaOperacao *Histograma

	// LatenciaRequisicao mede quanto o atendimento HTTP levou, por caminho.
	//
	// E separado da latencia da operacao porque as duas respondem perguntas
	// diferentes: uma e o tempo de negocio e a outra e o tempo de fila do servidor
	// HTTP. Misturar as duas daria um numero que sobe quando o numero de clientes
	// sobe, e o operador procuraria lentidao no banco.
	LatenciaRequisicao *Histograma
}

// limitesPadraoLatencia sao os buckets de latencia, em milissegundos.
//
// Submilissegundo nao entra porque o enunciado mede operacao financeira, que tem pelo
// menos uma transacao SQL: um bucket de 1 ms so receberia o que falha antes de tocar
// o banco, e poluiria o inicio do histograma com o que nao e a operacao.
//
// Os buckets intermediarios seguem as ordens de grandeza que importam: lock timeout de
// 1s e `statement_timeout` de 3s, que e onde o sistema muda de comportamento.
var limitesPadraoLatencia = []float64{
	5, 10, 25, 50, 100, 250, 500, 1000, 3000, 5000,
}

// NovasMetricas declara o conjunto no registro informado.
//
// Declarar tudo de uma vez, e nao no primeiro uso, e o que faz `GET /metrics`
// responder com nome, tipo e ajuda de todas as series desde o primeiro scrape. Sem
// isso o painel comecaria vazio e so apareceria depois da primeira ocorrencia, e
// "sem dado" e indistinguivel de "zero" para quem le.
func NovasMetricas(registro *Registro) *Metricas {
	if registro == nil {
		return nil
	}

	return &Metricas{
		Operacoes: registro.Contador("wager_operacoes_total",
			"Operacoes de wagering por desfecho, por estado final", "estado"),

		Duplicatas: registro.Contador("wager_operacoes_duplicadas_total",
			"Entregas repetidas reconhecidas pela idempotencia, sem efeito financeiro", "via"),

		ConflitosChave: registro.Contador("wager_conflitos_chave_total",
			"Chaves de idempotencia reusadas com conteudo diferente", "via"),

		Retentativas: registro.Contador("wager_retentativas_total",
			"Falhas que voltaram para a fila para nova tentativa", "origem"),

		MensagensFilaMorta: registro.Contador("wager_mensagens_fila_morta_total",
			"Mensagens que esgotaram as tentativas e foram para a cartao morto", "fila"),

		ConflitosLock: registro.Contador("wager_conflitos_lock_total",
			"Transacoes que perderam a disputa pelo lock da carteira", "motivo"),

		AtrasoOutbox: registro.Gauge("wager_outbox_atraso_segundos",
			"Maior atraso entre o instante do evento e o da publicacao, em segundos"),

		EventosPublicados: registro.Contador("wager_eventos_publicados_total",
			"Eventos publicados pelo relay da outbox", "tipo"),

		EventosDesistidos: registro.Contador("wager_eventos_desistidos_total",
			"Eventos que esgotaram as tentativas de publicacao", "tipo"),

		Divergencias: registro.Contador("wager_reconciliacoes_divergentes_total",
			"Reconciliacoes que encontraram saldo gravado diferente do ledger"),

		LatenciaOperacao: registro.Histograma("wager_operacao_duracao_ms",
			"Duracao do processamento da operacao, do inicio ao commit, em milissegundos",
			limitesPadraoLatencia, "estado"),

		LatenciaRequisicao: registro.Histograma("wager_requisicao_duracao_ms",
			"Duracao do atendimento HTTP, em milissegundos",
			limitesPadraoLatencia, "metodo"),
	}
}

// ObservaOperacao registra a latencia de uma operacao pelo desfecho.
//
// `inicio` e o instante em que a operacao comecou, e nao a duracao: passar o
// instante e o que impede quem chama de medir a parte errada do caminho. Um relay que
// mede do comeco do ciclo contaria o tempo de espera da fila, que nao e latencia de
// operacao.
func (m *Metricas) ObservaOperacao(inicio time.Time, desfecho string) {
	if m == nil {
		return
	}
	m.LatenciaOperacao.Observe(float64(time.Since(inicio).Milliseconds()), "estado", desfecho)
}

// ObservaDivergencia conta uma reconciliacao divergente.
func (m *Metricas) ObservaDivergencia() {
	if m == nil {
		return
	}
	m.Divergencias.Inc()
}

// DefineAtrasoOutbox publica o atraso entre o fato e a publicacao.
//
// O atraso vem do `occurred_at` do registro da outbox, que e o instante do fato e nao
// o da publicacao. E a diferenca entre os dois que mede o relay, e medir a partir da
// escrita do registro daria sempre proximo de zero.
func (m *Metricas) DefineAtrasoOutbox(ocorridoEm, publicadoEm time.Time) {
	if m == nil {
		return
	}
	m.AtrasoOutbox.Define(publicadoEm.Sub(ocorridoEm).Seconds())
}

// String devolve o valor de um numero no formato de exposicao.
//
// Existe para quem monta rotulo a partir de numero, e nao o converte a parte do nome.
func String(valor float64) string { return strconv.FormatFloat(valor, 'g', -1, 64) }
