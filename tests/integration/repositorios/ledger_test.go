//go:build integration

package repositorios

import (
	"errors"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// carteiraComLancamentos cria a carteira e a movementa, com N lancamentos de 10.00
// em debito, um por minuto.
func carteiraComLancamentos(t *testing.T, unidade *pg.Unidade, d string, n int) (wallet.Carteira, []wallet.Lancamento) {
	t.Helper()

	criada, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 100000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	inserirCarteiraDireto(t, d, criada)

	repositorio := pg.NovaRepositorioLedger()
	ctx := contexto(t)

	// A abertura e o primeiro lancamento, porque e assim que a carteira existe no
	// sistema de verdade, e e ela que faz a soma do ledger fechar com o saldo.
	abertura, lancamento, err := criada.Creditar(wallet.NovoIdentificador(), dinheiro(t, 100000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("credito da abertura: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := repositorio.Inserir(ctx, q, lancamento); err != nil {
			return err
		}
		return pg.NovaRepositorioCarteira().AtualizarSaldo(ctx, q, abertura)
	}); err != nil {
		t.Fatalf("abertura: %v", err)
	}

	lancamentos := []wallet.Lancamento{lancamento}
	atual := abertura
	for i := 1; i <= n; i++ {
		debito, lancamento, err := atual.Debitar(
			wallet.NovoIdentificador(), dinheiro(t, 1000, "BRL"),
			agoraTeste().Add(time.Duration(i)*time.Minute),
		)
		if err != nil {
			t.Fatalf("debito %d: %v", i, err)
		}
		if err := unidade.Executar(ctx, func(q pg.Querente) error {
			if err := repositorio.Inserir(ctx, q, lancamento); err != nil {
				return err
			}
			return pg.NovaRepositorioCarteira().AtualizarSaldo(ctx, q, debito)
		}); err != nil {
			t.Fatalf("movimentacao %d: %v", i, err)
		}
		lancamentos = append(lancamentos, lancamento)
		atual = debito
	}

	return atual, lancamentos
}

func TestInserirELerLancamento(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioLedger()
	carteiras := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	inicial, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 10000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}

	creditada, lancamento, err := inicial.Creditar(wallet.NovoIdentificador(), dinheiro(t, 10000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("Creditar: %v", err)
	}

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := carteiras.Inserir(ctx, q, inicial); err != nil {
			return err
		}
		if err := repositorio.Inserir(ctx, q, lancamento); err != nil {
			return err
		}
		return carteiras.AtualizarSaldo(ctx, q, creditada)
	}); err != nil {
		t.Fatalf("gravacao: %v", err)
	}

	var pagina pg.PaginaE
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		pagina, err = repositorio.Listar(ctx, q, inicial.ID(), pg.CursorNulo(), 10)
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}

	if len(pagina.Lancamentos) != 1 {
		t.Fatalf("lancamentos e %d, esperado 1", len(pagina.Lancamentos))
	}
	lido := pagina.Lancamentos[0]
	if lido.ID() != lancamento.ID() {
		t.Errorf("ID e %s, esperado %s", lido.ID(), lancamento.ID())
	}
	if !lido.Valor().Equal(lancamento.Valor()) {
		t.Errorf("valor e %s, esperado %s", lido.Valor(), lancamento.Valor())
	}
	if lido.Direcao() != lancamento.Direcao() {
		t.Errorf("direcao e %q, esperado %q", lido.Direcao(), lancamento.Direcao())
	}
	if !lido.SaldoAnterior().Equal(lancamento.SaldoAnterior()) {
		t.Errorf("saldo anterior e %s, esperado %s", lido.SaldoAnterior(), lancamento.SaldoAnterior())
	}
	if !lido.SaldoPosterior().Equal(lancamento.SaldoPosterior()) {
		t.Errorf("saldo posterior e %s, esperado %s", lido.SaldoPosterior(), lancamento.SaldoPosterior())
	}
}

// A paginacao por cursor tem de percorrer o ledger inteiro sem repetir e sem pular
// linha. E a propriedade que um cliente usando cursor para reconciliar depende.
func TestPaginacaoPercorreTodoOLedgerSemRepetirNemPular(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioLedger()
	carteira, criados := carteiraComLancamentos(t, unidade, dsnDono, 12)
	ctx := contexto(t)

	vistos := map[string]int{}
	var (
		anterior *wallet.Lancamento
		paginas  int
	)

	cursor := pg.CursorNulo()
	for {
		var pagina pg.PaginaE
		if err := unidade.Ler(ctx, func(q pg.Querente) error {
			var err error
			pagina, err = repositorio.Listar(ctx, q, carteira.ID(), cursor, 5)
			return err
		}); err != nil {
			t.Fatalf("pagina %d: %v", paginas, err)
		}

		paginas++
		for i, l := range pagina.Lancamentos {
			vistos[l.ID().String()]++
			if anterior != nil {
				// Ordenacao estritamente decrescente: cada linha da pagina
				// seguinte e mais antiga que a ultima da anterior.
				if !l.CriadoEm().Before(anterior.CriadoEm()) {
					t.Fatalf("pagina %d linha %d: %v nao e anterior a %v",
						paginas, i, l.CriadoEm(), anterior.CriadoEm())
				}
			}
			ultimo := l
			anterior = &ultimo
		}

		if !pagina.TemMais {
			break
		}
		if pagina.ProximoCursor.Texto() == "" {
			t.Fatal("pagina com mais elementos e cursor proximo vazio")
		}
		cursor = pagina.ProximoCursor

		if paginas > 20 {
			t.Fatal("a paginacao nao terminou")
		}
	}

	if len(vistos) != len(criados) {
		t.Errorf("viu %d lancamentos distintos, esperado %d", len(vistos), len(criados))
	}
	for id, vezes := range vistos {
		if vezes != 1 {
			t.Errorf("lancamento %s apareceu %d vezes", id, vezes)
		}
	}
	if paginas < 3 {
		t.Errorf("paginas e %d, esperado ao menos 3 para 13 lancamentos com limite 5", paginas)
	}
}

// Dois lancamentos com o mesmo instante e o caso que quebra cursor so por tempo:
// sem o id no cursor, a pagina seguinte repetiria ou pularia a linha do limite.
func TestPaginacaoComLancamentosNoMesmoInstante(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioLedger()
	carteiras := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	inicial, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 3000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return carteiras.Inserir(ctx, q, inicial)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	// Tres lancamentos com o mesmo instante: e o que acontece quando a abertura e
	// o primeiro movimento acontecem no mesmo tick.
	atual := inicial
	instante := agoraTeste()
	for i := 0; i < 3; i++ {
		debito, lancamento, err := atual.Debitar(wallet.NovoIdentificador(), dinheiro(t, 1000, "BRL"), instante)
		if err != nil {
			t.Fatalf("debito %d: %v", i, err)
		}
		if err := unidade.Executar(ctx, func(q pg.Querente) error {
			if err := repositorio.Inserir(ctx, q, lancamento); err != nil {
				return err
			}
			return carteiras.AtualizarSaldo(ctx, q, debito)
		}); err != nil {
			t.Fatalf("movimentacao %d: %v", i, err)
		}
		atual = debito
	}

	vistos := map[string]int{}
	cursor := pg.CursorNulo()
	for {
		var pagina pg.PaginaE
		if err := unidade.Ler(ctx, func(q pg.Querente) error {
			var err error
			pagina, err = repositorio.Listar(ctx, q, inicial.ID(), cursor, 2)
			return err
		}); err != nil {
			t.Fatalf("leitura: %v", err)
		}
		for _, l := range pagina.Lancamentos {
			vistos[l.ID().String()]++
		}
		if !pagina.TemMais {
			break
		}
		cursor = pagina.ProximoCursor
	}

	if len(vistos) != 3 {
		t.Errorf("viu %d lancamentos, esperado 3: %v", len(vistos), vistos)
	}
	for id, vezes := range vistos {
		if vezes != 1 {
			t.Errorf("lancamento %s apareceu %d vezes no mesmo instante", id, vezes)
		}
	}
}

// O cursor e opaco: o cliente nao monta nem edita. E erro de entrada devolver
// pagina vazia para cursor quebrado, porque o cliente receberia a primeira pagina
// sem saber que o resultado estava errado.
func TestCursorInvalidoDevolveErro(t *testing.T) {
	invalidos := []string{
		"nao-e-base64-!!!",
		"aGVsbG8",              // base64 valido, sem separador
		"bm90by1hLWluc3RhbnRl", // sem separador tambem
	}

	for _, texto := range invalidos {
		t.Run(texto, func(t *testing.T) {
			if _, err := pg.DecodificarCursor(texto); !errors.Is(err, pg.ErrCursorInvalido) {
				t.Errorf("devolveu %v, esperado ErrCursorInvalido", err)
			}
		})
	}

	// Cursor vazio e o cursor inicial, e nao erro.
	if _, err := pg.DecodificarCursor(""); err != nil {
		t.Errorf("cursor vazio devolveu %v", err)
	}
}

func TestCursorDeidaVolta(t *testing.T) {
	instante := agoraTeste().Add(90 * time.Second)
	id := wallet.NovoIdentificador()

	original := pg.CursorDe(instante, id)
	texto := original.Texto()
	if texto == "" {
		t.Fatal("cursor de nada e vazio")
	}

	lido, err := pg.DecodificarCursor(texto)
	if err != nil {
		t.Fatalf("decodificacao: %v", err)
	}
	if lido.Texto() != texto {
		t.Errorf("o cursor nao sobrevive a ida e a volta: %q e %q", lido.Texto(), texto)
	}
	if lido.Identificador() != id {
		t.Errorf("identificador e %s, esperado %s", lido.Identificador(), id)
	}
	if !lido.Instante().Equal(instante) {
		t.Errorf("instante e %v, esperado %v", lido.Instante(), instante)
	}
}

// O limite vem do cliente, e sem teto um `limit` enorme consome a memoria do
// processo. O teto recusa o numero absurdo e devolve a pagina pedida.
func TestLimiteDePaginaTemTeto(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioLedger()
	carteira, _ := carteiraComLancamentos(t, unidade, dsnDono, 3)
	ctx := contexto(t)

	var pagina pg.PaginaE
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		pagina, err = repositorio.Listar(ctx, q, carteira.ID(), pg.CursorNulo(), 1_000_000)
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}

	if len(pagina.Lancamentos) != 4 {
		t.Errorf("lancamentos e %d, esperado 4", len(pagina.Lancamentos))
	}
	if pagina.TemMais {
		t.Error("a pagina inteira nao pode dizer que tem mais")
	}
}

// O ledger de uma carteira nao vaza para a outra. O filtro por carteira e a
// garantia de que a leitura de uma nao revela movimento de outra.
func TestListagemNaoAtravessaCarteira(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioLedger()
	carteiras := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	const jogadorB = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2"

	primeira, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 1000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	segunda, err := wallet.Nova(idValido(t, jogadorB), dinheiro(t, 1000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		if err := carteiras.Inserir(ctx, q, primeira); err != nil {
			return err
		}
		return carteiras.Inserir(ctx, q, segunda)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	for _, c := range []wallet.Carteira{primeira, segunda} {
		creditada, lancamento, err := c.Creditar(wallet.NovoIdentificador(), dinheiro(t, 1000, "BRL"), agoraTeste())
		if err != nil {
			t.Fatalf("Creditar: %v", err)
		}
		if err := unidade.Executar(ctx, func(q pg.Querente) error {
			if err := repositorio.Inserir(ctx, q, lancamento); err != nil {
				return err
			}
			return carteiras.AtualizarSaldo(ctx, q, creditada)
		}); err != nil {
			t.Fatalf("movimentacao: %v", err)
		}
	}

	var pagina pg.PaginaE
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		pagina, err = repositorio.Listar(ctx, q, primeira.ID(), pg.CursorNulo(), 50)
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}

	if len(pagina.Lancamentos) != 1 {
		t.Fatalf("lancamentos e %d, esperado 1", len(pagina.Lancamentos))
	}
	if pagina.Lancamentos[0].Carteira() != primeira.ID() {
		t.Error("a listagem trouxe lancamento de outra carteira")
	}
}
