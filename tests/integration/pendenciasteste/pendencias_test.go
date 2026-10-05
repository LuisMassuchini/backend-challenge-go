//go:build integration

// Package pendenciasteste prova que uma reversao entregue antes da aposta e retomada
// depois, e que uma referencia que nunca chega vira recusa em vez de espera eterna.
package pendenciasteste

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LuisMassuchini/backend-challenge-go/internal/app"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// dsnRuntime e o papel de menor privilegio.
const dsnRuntime = "postgres://wager_app:wager_app@localhost:5432/wager?sslmode=disable"

// servicosDeTeste monta os casos de uso com um relogio controlavel.
func servicosDeTeste(t *testing.T) (app.Servicos, *relogioDeTeste) {
	t.Helper()

	pool, err := pg.AbrirPool(contexto(t), dsnRuntime, pg.Opcoes{MaxConexoes: 8})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	relogio := &relogioDeTeste{agora: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}

	return app.Servicos{
		Unidade:    pg.NovaUnidade(pool),
		Carteiras:  pg.NovaRepositorioCarteira(),
		Ledger:     pg.NovaRepositorioLedger(),
		Transacoes: pg.NovaRepositorioTransacoes(),
		Inbox:      pg.NovaRepositorioInbox(),
		Outbox:     pg.NovaRepositorioOutbox(),
		Relogio:    relogio,
		Correlacao: func() string { return uuid.NewString() },
	}, relogio
}

// relogioDeTeste e um relogio que o teste controla.
//
// O worker agendaria a proxima tentativa em minutos. Sem poder mover o tempo, o teste
// teria de esperar minutos de verdade, e um teste que leva minutos e um teste que
// ninguem roda.
type relogioDeTeste struct{ agora time.Time }

func (r *relogioDeTeste) Agora() time.Time { return r.agora }

func (r *relogioDeTeste) avanca(por time.Duration) { r.agora = r.agora.Add(por) }

// atorInterno e o cliente de servico.
func atorInterno() app.Ator {
	return app.Ator{
		Cliente: "wager-service",
		Escopos: []string{app.EscopoAberturaCarteira, app.EscopoReconciliacao},
	}
}

// abrirCarteira abre uma carteira.
func abrirCarteira(t *testing.T, s app.Servicos, centavos int64) wallet.Carteira {
	t.Helper()

	valor, err := money.Parse(fmt.Sprintf("%d.%02d", centavos/100, centavos%100), money.CurrencyBRL)
	if err != nil {
		t.Fatalf("money.Parse: %v", err)
	}

	resposta, err := app.AbrirCarteira(contexto(t), s, atorInterno(), app.RequisicaoAbertura{
		Jogador:      idDe(t, uuid.NewString()),
		SaldoInicial: valor,
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}
	return resposta.Carteira
}

// idDe converte texto em identificador.
func idDe(t *testing.T, texto string) wallet.Identificador {
	t.Helper()

	id, err := wallet.IdentificadorDe(texto)
	if err != nil {
		t.Fatalf("IdentificadorDe(%q): %v", texto, err)
	}
	return id
}

// resumoDe devolve um resumo de conteudo qualquer, e o suficiente porque a pendencia
// nao depende do resumo.
func resumoDe(conteudo string) wagering.Hash {
	return wagering.Hash("sha256:" + conteudo)
}

// comandoDe monta um comando de operacao.
func comandoDe(
	t *testing.T,
	carteira wallet.Carteira,
	externa, chave, tipo string,
	centavos int64,
	referencia string,
) app.RequisicaoOperacao {
	t.Helper()

	valor, err := money.Parse(fmt.Sprintf("%d.%02d", centavos/100, centavos%100), money.CurrencyBRL)
	if err != nil {
		t.Fatalf("money.Parse: %v", err)
	}

	return app.RequisicaoOperacao{
		Provedor:         wagering.Provedor("provider-a"),
		TransacaoExterna: wagering.Externo(externa),
		Chave:            wagering.Chave(chave),
		Fingerprint:      resumoDe(chave + tipo + externa),
		Carteira:         carteira.ID(),
		Jogador:          carteira.Jogador(),
		Rodada:           "round-987",
		Jogo:             "fortune-chimp",
		Tipo:             wagering.Tipo(tipo),
		Valor:            valor,
		Referencia:       wagering.Referencia{Externa: wagering.Externo(referencia)},
		Correlacao:       uuid.NewString(),
	}
}

// aplicar executa um comando como o provedor.
func aplicar(t *testing.T, s app.Servicos, comando app.RequisicaoOperacao) app.RespostaOperacao {
	t.Helper()

	ator := app.Ator{
		Cliente:  "provider-a",
		Provedor: "provider-a",
		Escopos:  []string{app.EscopoOperacoes},
	}

	resposta, err := app.ProcessarOperacao(contexto(t), s, ator, comando)
	if err != nil {
		t.Fatalf("ProcessarOperacao: %v", err)
	}
	return resposta
}

// rodarWorker executa um ciclo do worker.
//
// O caso de uso faz a reserva e o trabalho na mesma unidade, entao uma chamada do
// worker e exatamente o que o teste quer exercitar: a pendencia reservada e tratada
// antes de a transacao fechar.
func rodarWorker(t *testing.T, s app.Servicos, politica app.Politica) int {
	t.Helper()

	tratadas, err := app.RetomarPendencias(contexto(t), s, politica, 20)
	if err != nil {
		t.Fatalf("RetomarPendencias: %v", err)
	}
	return tratadas
}

// pendenciasTotais conta as pendencias.
func pendenciasTotais(t *testing.T, s app.Servicos) int {
	t.Helper()

	total, err := app.ContarPendencias(contexto(t), s, wagering.EstadoPendenteReferencia)
	if err != nil {
		t.Fatalf("ContarPendencias: %v", err)
	}
	return total
}

// saldoDe devolve o saldo da carteira.
func saldoDe(t *testing.T, s app.Servicos, carteira wallet.Carteira) string {
	t.Helper()

	lida, err := app.LerCarteira(contexto(t), s, atorInterno(), carteira.ID())
	if err != nil {
		t.Fatalf("LerCarteira: %v", err)
	}
	return lida.Saldo().Decimal()
}

// ---------------------------------------------------------------------------
// Retomada
// ---------------------------------------------------------------------------

// A reversao que chegou antes da aposta e retomada quando a aposta existe.
//
// E o cenario que o enunciado chama de "cenario de entrega invertida": o estorno
// chega antes da aposta e o sistema precisa esperar, sem perder nem duplicar.
func TestReversaoInvertidaERetomadaQuandoAApostaChega(t *testing.T) {
	dbtest.Limpa(t)
	s, _ := servicosDeTeste(t)

	carteira := abrirCarteira(t, s, 10000)

	// A reversao primeiro: a referencia ainda nao existe.
	pendente := aplicar(t, s, comandoDe(t, carteira, "ext-refund", "ch-refund", "REFUND", 2500, "ext-bet"))
	if pendente.Estado != wagering.EstadoPendenteReferencia {
		t.Fatalf("estado e %q, esperado %q", pendente.Estado, wagering.EstadoPendenteReferencia)
	}
	if got := saldoDe(t, s, carteira); got != "100.00" {
		t.Fatalf("saldo e %s, esperado 100.00: pendente nao move dinheiro", got)
	}
	if got := pendenciasTotais(t, s); got != 1 {
		t.Fatalf("pendencias e %d, esperado 1", got)
	}

	// A aposta chega.
	aplicar(t, s, comandoDe(t, carteira, "ext-bet", "ch-bet", "BET", 2500, ""))
	if got := saldoDe(t, s, carteira); got != "75.00" {
		t.Fatalf("saldo apos a aposta e %s, esperado 75.00", got)
	}

	// O worker encontra a referencia e estorna.
	rodarWorker(t, s, politicaImediata())

	if got := pendenciasTotais(t, s); got != 0 {
		t.Errorf("pendencias restantes e %d, esperado 0", got)
	}
	if got := saldoDe(t, s, carteira); got != "100.00" {
		t.Errorf("saldo e %s, esperado 100.00: o estorno devolveu os 25.00", got)
	}

	// A transacao foi concluida como PROCESSED e o estorno tem lancamento.
	transacao, err := app.LerTransacao(contexto(t), s, atorInterno(), pendente.TransacaoID)
	if err != nil {
		t.Fatalf("ler a transacao: %v", err)
	}
	if transacao.Estado() != wagering.EstadoProcessado {
		t.Errorf("estado da reversao e %q, esperado PROCESSED", transacao.Estado())
	}

	// Abertura, aposta e estorno: tres lancamentos.
	if got := contarLancamentos(t); got != 3 {
		t.Errorf("lancamentos e %d, esperado 3", got)
	}
}

// A retomada nao duplica dinheiro. Rodar o worker varias vezes sobre a mesma
// pendencia so pode mover o dinheiro uma vez.
func TestRetomadaNaoDuplicaDinheiro(t *testing.T) {
	dbtest.Limpa(t)
	s, _ := servicosDeTeste(t)

	carteira := abrirCarteira(t, s, 10000)
	aplicar(t, s, comandoDe(t, carteira, "ext-refund", "ch-refund", "REFUND", 2500, "ext-bet"))
	aplicar(t, s, comandoDe(t, carteira, "ext-bet", "ch-bet", "BET", 2500, ""))

	rodarWorker(t, s, politicaImediata())

	// Varios ciclos depois: nada muda.
	for i := 0; i < 3; i++ {
		rodarWorker(t, s, politicaImediata())
	}

	if got := saldoDe(t, s, carteira); got != "100.00" {
		t.Errorf("saldo e %s, esperado 100.00", got)
	}
	if got := contarLancamentos(t); got != 3 {
		t.Errorf("lancamentos e %d, esperado 3: ciclos extras nao lancaram", got)
	}
}

// A segunda reversao da mesma aposta e recusada na hora, sem passar por pendencia.
//
// A referencia existe e a politica proibe: um REFUND ou um ROLLBACK, nunca os dois.
// E recusa e nao espera, porque a espera pressupoe que a situacao possa se resolver
// sozinha, e esta nao se resolve.
func TestSegundaReversaoDaMesmaApostaERecusadaNaHora(t *testing.T) {
	dbtest.Limpa(t)
	s, _ := servicosDeTeste(t)

	carteira := abrirCarteira(t, s, 10000)
	aplicar(t, s, comandoDe(t, carteira, "ext-bet", "ch-bet", "BET", 2500, ""))

	// O primeiro estorno passa.
	aplicar(t, s, comandoDe(t, carteira, "ext-refund-1", "ch-r1", "REFUND", 2500, "ext-bet"))
	if got := saldoDe(t, s, carteira); got != "100.00" {
		t.Fatalf("saldo e %s, esperado 100.00", got)
	}

	// O segundo e recusado, e a recusa e persistida.
	segundo := aplicar(t, s, comandoDe(t, carteira, "ext-refund-2", "ch-r2", "REFUND", 2500, "ext-bet"))
	if segundo.Estado != wagering.EstadoRejeitado {
		t.Fatalf("estado e %q, esperado REJECTED", segundo.Estado)
	}
	if segundo.CodigoFalha != wagering.CodigoFalhaReversaoJaAplicada {
		t.Errorf("codigo e %q, esperado %q", segundo.CodigoFalha,
			wagering.CodigoFalhaReversaoJaAplicada)
	}

	// Nenhuma pendencia foi criada: recusar nao e esperar.
	if got := pendenciasTotais(t, s); got != 0 {
		t.Errorf("pendencias e %d, esperado 0: recusa nao cria pendencia", got)
	}

	// E o dinheiro nao se moveu.
	if got := saldoDe(t, s, carteira); got != "100.00" {
		t.Errorf("saldo e %s, esperado 100.00: a segunda reversao nao pode creditar", got)
	}
	if got := contarLancamentos(t); got != 3 {
		t.Errorf("lancamentos e %d, esperado 3", got)
	}
}

// ---------------------------------------------------------------------------
// Agendamento e expiracao
// ---------------------------------------------------------------------------

// A pendencia e reagendada com backoff quando a referencia ainda nao chegou.
//
// E o que impede o worker de ficar em laco apertado sobre a mesma pendencia.
func TestPendenciaSemReferenciaEAgendadaComIntervaloCrescente(t *testing.T) {
	dbtest.Limpa(t)
	s, relogio := servicosDeTeste(t)

	carteira := abrirCarteira(t, s, 10000)
	aplicar(t, s, comandoDe(t, carteira, "ext-refund", "ch-refund", "REFUND", 2500, "ext-inexistente"))

	politica := app.Politica{
		IntervaloInicial:   2 * time.Second,
		IntervaloMaximo:    time.Minute,
		MaximoDeTentativas: 10,
	}

	// O primeiro ciclo nao encontra referencia e reagenda.
	rodarWorker(t, s, politica)

	if got := pendenciasTotais(t, s); got != 1 {
		t.Fatalf("a pendencia sumiu: %d restantes", got)
	}

	// A proxima tentativa e depois do instante atual, e a tentativa foi contada.
	proxima, tentativas := agendamentoDe(t, "ext-refund")
	if tentativas != 1 {
		t.Errorf("tentativas e %d, esperado 1", tentativas)
	}
	if !proxima.After(relogio.agora) {
		t.Errorf("proxima tentativa e %s, que nao e depois de %s", proxima, relogio.agora)
	}
	if restante := proxima.Sub(relogio.agora); restante != 2*time.Second {
		t.Errorf("intervalo e %s, esperado 2s: o primeiro intervalo nao dobra", restante)
	}

	// Com o tempo parado, mais ciclos nao advancedem o agendamento: e o que impede
	// que a pendencia seja retentada em laco.
	for i := 0; i < 3; i++ {
		rodarWorker(t, s, politica)
	}
	_, tentativas = agendamentoDe(t, "ext-refund")
	if tentativas != 1 {
		t.Errorf("tentativas e %d, esperado 1: o agendamento impede novo ciclo", tentativas)
	}
}

// O intervalo dobra a cada tentativa, ate o teto.
func TestIntervaloDobraAteOTeto(t *testing.T) {
	politica := app.Politica{
		IntervaloInicial:   2 * time.Second,
		IntervaloMaximo:    8 * time.Second,
		MaximoDeTentativas: 10,
	}

	agora := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	esperados := []time.Duration{
		2 * time.Second, // nenhuma tentativa ainda
		4 * time.Second, // uma
		8 * time.Second, // duas
		8 * time.Second, // tres, ja no teto
		8 * time.Second, // quatro
	}

	for feitas, esperado := range esperados {
		proxima, vale := politica.ProximaTentativa(feitas, agora)
		if !vale {
			t.Fatalf("tentativa %d: politica recusou", feitas)
		}
		if obtida := proxima.Sub(agora); obtida != esperado {
			t.Errorf("tentativa %d: intervalo %s, esperado %s", feitas, obtida, esperado)
		}
	}
}

// A pendencia expira depois do maximo de tentativas, e vira FAILED com codigo
// proprio.
//
// Expirar e melhor do que esperar para sempre: um provedor com uma resposta
// definitiva pode decidir o que fazer, e um provedor esperando para sempre nao pode.
func TestPendenciaExpiraDepoisDoMaximoDeTentativas(t *testing.T) {
	dbtest.Limpa(t)
	s, relogio := servicosDeTeste(t)

	carteira := abrirCarteira(t, s, 10000)
	aplicar(t, s, comandoDe(t, carteira, "ext-refund", "ch-refund", "REFUND", 2500, "ext-inexistente"))

	politica := app.Politica{
		IntervaloInicial:   time.Second,
		IntervaloMaximo:    time.Second,
		MaximoDeTentativas: 3,
	}

	// Cada ciclo com o tempo avancado consome uma tentativa.
	for i := 0; i < 5; i++ {
		relogio.avanca(2 * time.Second)
		rodarWorker(t, s, politica)
	}

	if got := pendenciasTotais(t, s); got != 0 {
		t.Fatalf("a pendencia nao expirou: %d restantes", got)
	}

	transacao, err := app.LerTransacaoDoProvedor(contexto(t), s, atorInterno(),
		wagering.Provedor("provider-a"), wagering.Externo("ext-refund"))
	if err != nil {
		t.Fatalf("ler a transacao: %v", err)
	}
	if transacao.Estado() != wagering.EstadoFalhou {
		t.Errorf("estado e %q, esperado FAILED", transacao.Estado())
	}
	if transacao.CodigoFalha() != "REFERENCIA_NUNCA_CHEGOU" {
		t.Errorf("codigo e %q", transacao.CodigoFalha())
	}

	// A expiracao nao move dinheiro nem gera lancamento.
	if got := saldoDe(t, s, carteira); got != "100.00" {
		t.Errorf("saldo e %s, esperado 100.00", got)
	}
	if got := contarLancamentos(t); got != 1 {
		t.Errorf("lancamentos e %d, esperado 1: so a abertura", got)
	}
}

// Expirar e diferente de recusar: FAILED e a resposta do sistema para uma referencia
// que nunca chegou, e REJECTED e a resposta do provedor a uma operacao errada.
func TestExpiracaoNaoSeConfundeComRecusa(t *testing.T) {
	dbtest.Limpa(t)
	s, relogio := servicosDeTeste(t)

	carteira := abrirCarteira(t, s, 10000)

	// Recusa: nao ha saldo, e o provedor precisa corrigirOu esperar deposito.
	recusada := aplicar(t, s, comandoDe(t, carteira, "ext-ruim", "ch-ruim", "BET", 99999999, ""))
	if recusada.Estado != wagering.EstadoRejeitado {
		t.Errorf("estado da recusa e %q", recusada.Estado)
	}

	// Expiracao: a referencia nunca chega.
	aplicar(t, s, comandoDe(t, carteira, "ext-refund", "ch-refund", "REFUND", 2500, "ext-inexistente"))
	politica := app.Politica{
		IntervaloInicial:   time.Second,
		IntervaloMaximo:    time.Second,
		MaximoDeTentativas: 1,
	}
	relogio.avanca(2 * time.Second)
	rodarWorker(t, s, politica)

	expirada, err := app.LerTransacao(contexto(t), s, atorInterno(), recusada.TransacaoID)
	if err != nil {
		t.Fatalf("ler a recusada: %v", err)
	}
	if expirada.Estado() == wagering.EstadoFalhou {
		t.Error("a recusa do provedor foi marcada como expiracao do sistema")
	}
}

// ---------------------------------------------------------------------------
// Varias pendencias
// ---------------------------------------------------------------------------

// Varias pendencias sao retomadas no mesmo ciclo.
//
// Os estornos sao entregues ANTES das respectivas apostas, que e o que gera as
// pendencias: com a referencia ja existente, o estorno entraria direto e nao haveria
// o que retomar.
func TestVariasPendenciasSaoRetomadasNoMesmoCiclo(t *testing.T) {
	dbtest.Limpa(t)
	s, _ := servicosDeTeste(t)

	carteira := abrirCarteira(t, s, 100000)

	// Tres estornos, cada um esperando a sua aposta.
	aplicar(t, s, comandoDe(t, carteira, "ext-r-1", "ch-r1", "REFUND", 1000, "ext-bet-1"))
	aplicar(t, s, comandoDe(t, carteira, "ext-r-2", "ch-r2", "REFUND", 1000, "ext-bet-2"))
	aplicar(t, s, comandoDe(t, carteira, "ext-r-3", "ch-r3", "REFUND", 1000, "ext-bet-3"))

	if got := pendenciasTotais(t, s); got != 3 {
		t.Fatalf("pendencias e %d, esperado 3", got)
	}

	// As apostas chegam.
	aplicar(t, s, comandoDe(t, carteira, "ext-bet-1", "ch-b1", "BET", 1000, ""))
	aplicar(t, s, comandoDe(t, carteira, "ext-bet-2", "ch-b2", "BET", 1000, ""))
	aplicar(t, s, comandoDe(t, carteira, "ext-bet-3", "ch-b3", "BET", 1000, ""))

	rodarWorker(t, s, politicaImediata())

	if got := pendenciasTotais(t, s); got != 0 {
		t.Errorf("pendencias restantes e %d, esperado 0", got)
	}
	if got := saldoDe(t, s, carteira); got != "1000.00" {
		t.Errorf("saldo e %s, esperado 1000.00: as tres apostas foram estornadas", got)
	}
}

// Duas instancias nao retomam a mesma pendencia.
//
// E o que a reserva na mesma unidade do trabalho garante: o `FOR UPDATE SKIP LOCKED`
// so protege enquanto a transacao esta aberta, e e por isso que reserva e aplicacao
// compartilham uma unidade. Duas chamadas concorrentes pegam pendencias distintas, e
// o saldo final mostra que o estorno foi aplicado uma vez so.
func TestDuasInstanciasNaoRetomamOMesmaPendencia(t *testing.T) {
	dbtest.Limpa(t)
	s, _ := servicosDeTeste(t)

	carteira := abrirCarteira(t, s, 100000)

	// Tres estornos entregues antes das respectivas apostas: tres pendencias.
	aplicar(t, s, comandoDe(t, carteira, "ext-r-1", "ch-r1", "REFUND", 1000, "ext-bet-1"))
	aplicar(t, s, comandoDe(t, carteira, "ext-r-2", "ch-r2", "REFUND", 1000, "ext-bet-2"))
	aplicar(t, s, comandoDe(t, carteira, "ext-r-3", "ch-r3", "REFUND", 1000, "ext-bet-3"))

	if got := pendenciasTotais(t, s); got != 3 {
		t.Fatalf("pendencias e %d, esperado 3", got)
	}

	// As apostas chegam.
	aplicar(t, s, comandoDe(t, carteira, "ext-bet-1", "ch-b1", "BET", 1000, ""))
	aplicar(t, s, comandoDe(t, carteira, "ext-bet-2", "ch-b2", "BET", 1000, ""))
	aplicar(t, s, comandoDe(t, carteira, "ext-bet-3", "ch-b3", "BET", 1000, ""))

	// Duas chamadas concorrentes, como duas instancias do worker.
	var (
		espera   sync.WaitGroup
		protecao sync.Mutex
		feitas   int
	)

	for i := 0; i < 2; i++ {
		espera.Add(1)
		go func() {
			defer espera.Done()

			tratadas, err := app.RetomarPendencias(contexto(t), s, politicaImediata(), 20)
			if err != nil {
				t.Errorf("RetomarPendencias: %v", err)
				return
			}

			protecao.Lock()
			defer protecao.Unlock()
			feitas += tratadas
		}()
	}
	espera.Wait()

	// Cada pendencia foi tratada por uma das instancias, uma vez so.
	if feitas != 3 {
		t.Errorf("pendencias tratadas e %d, esperado 3", feitas)
	}
	if got := pendenciasTotais(t, s); got != 0 {
		t.Errorf("pendencias restantes e %d, esperado 0", got)
	}

	// 1000.00 menos tres apostas de 10.00, mais tres estornos de 10.00.
	if got := saldoDe(t, s, carteira); got != "1000.00" {
		t.Errorf("saldo e %s, esperado 1000.00", got)
	}
	// Abertura, tres apostas e tres estornos: sete lancamentos.
	if got := contarLancamentos(t); got != 7 {
		t.Errorf("lancamentos e %d, esperado 7: um estorno foi aplicado duas vezes", got)
	}
}
