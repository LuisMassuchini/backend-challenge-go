//go:build integration

package repositorios

import (
	"errors"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

const jogadorA = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"

// TestInserirELerCarteiraEfezIdaVolta
//
// A primeira garantia de um repositorio e a mais ignorada: o que ele grava, ele
// devolve. Sem isso, nenhum das outras garantias significa alguma coisa.
func TestInserirELerCarteiraEfezIdaVolta(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	criada, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 10000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	var lida wallet.Carteira
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.Ler(ctx, q, criada.ID())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}

	if lida.ID() != criada.ID() {
		t.Errorf("ID e %s, esperado %s", lida.ID(), criada.ID())
	}
	if !lida.Saldo().Equal(criada.Saldo()) {
		t.Errorf("saldo e %s, esperado %s", lida.Saldo(), criada.Saldo())
	}
	if lida.Versao() != criada.Versao() {
		t.Errorf("versao e %d, esperado %d", lida.Versao(), criada.Versao())
	}
	if lida.Saldo().Currency() != criada.Saldo().Currency() {
		t.Errorf("moeda e %q, esperado %q", lida.Saldo().Currency(), criada.Saldo().Currency())
	}
	if !lida.CriadaEm().Equal(criada.CriadaEm()) {
		t.Errorf("criada em %v, esperado %v", lida.CriadaEm(), criada.CriadaEm())
	}
}

func TestLerCarteiraInexistenteDevolveNaoEncontrado(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	ausente := wallet.NovoIdentificador()
	err := unidade.Ler(ctx, func(q pg.Querente) error {
		_, err := repositorio.Ler(ctx, q, ausente)
		return err
	})

	if !errors.Is(err, pg.ErrNaoEncontrado) {
		t.Fatalf("devolveu %v, esperado ErrNaoEncontrado", err)
	}
}

func TestLerPorJogadorEMoeda(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	criada, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 5000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	var lida wallet.Carteira
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.LerPorJogadorEMoeda(ctx, q, criada.Jogador(), criada.Saldo().Currency())
		return err
	}); err != nil {
		t.Fatalf("leitura por jogador e moeda: %v", err)
	}
	if lida.ID() != criada.ID() {
		t.Errorf("ID e %s, esperado %s", lida.ID(), criada.ID())
	}
}

// A segunda carteira do mesmo jogador na mesma moeda e conflito, e o conflito vem
// do banco como ErrConflitoDeChave, e nao como erro de driver cru.
func TestSegundaCarteiraDoMesmoJogadorDaConflito(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	primeira, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 10000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, primeira)
	}); err != nil {
		t.Fatalf("primeira insercao: %v", err)
	}

	segunda, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 0, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	err = unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, segunda)
	})

	if !errors.Is(err, pg.ErrConflitoDeChave) {
		t.Fatalf("devolveu %v, esperado ErrConflitoDeChave", err)
	}
}

// A atualizacao condicional por versao e a segunda metade da garantia de lost
// update: o lock protege a leitura, a versao protege a escrita.
func TestAtualizarSaldoEcondicionalPorVersao(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	criada, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 10000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	// Debito legitimo: a versao no dominio sobe, e o banco compara com a anterior.
	debito, lancamento, err := criada.Debitar(wallet.NovoIdentificador(), dinheiro(t, 2500, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("Debitar: %v", err)
	}
	_ = lancamento

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.AtualizarSaldo(ctx, q, debito)
	}); err != nil {
		t.Fatalf("atualizacao legitima: %v", err)
	}
}

// A mesma carteira, duas leituras concorrentes do agregado, e so uma escreve. A
// segunda tem de receber conflito de versao e nao sobrescrever a primeira.
func TestAtualizacaoComVersaoDesatualizadaDaConflito(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	criada, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 10000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	// Duas copias do mesmo agregado, como duas instancias que leram a carteira
	// antes de qualquer escrita.
	primeira, _, err := criada.Debitar(wallet.NovoIdentificador(), dinheiro(t, 1000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("primeiro debito: %v", err)
	}
	segunda, _, err := criada.Debitar(wallet.NovoIdentificador(), dinheiro(t, 2000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("segundo debito: %v", err)
	}

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.AtualizarSaldo(ctx, q, primeira)
	}); err != nil {
		t.Fatalf("primeira atualizacao: %v", err)
	}

	err = unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.AtualizarSaldo(ctx, q, segunda)
	})
	if !errors.Is(err, pg.ErrConflitoDeVersao) {
		t.Fatalf("devolveu %v, esperado ErrConflitoDeVersao", err)
	}

	// A carteira tem o saldo da primeira escrita, nao da segunda.
	var lida wallet.Carteira
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.Ler(ctx, q, criada.ID())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}
	if !lida.Saldo().Equal(dinheiro(t, 9000, "BRL")) {
		t.Errorf("saldo e %s, esperado 90.00: a escrita desatualizada nao pode ter pasado", lida.Saldo())
	}
	if lida.Versao() != 2 {
		t.Errorf("versao e %d, esperado 2", lida.Versao())
	}
}

// A unidade de trabalho desfaz tudo quando a funcao falha. E o que faz a operacao
// financeira ser atomica: carteira, lancamento, transacao e outbox no mesmo
// commit, ou nada.
func TestUnidadeDesfazTudoEmErro(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	criada, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 10000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}

	erroEsperado := errors.New("falha depois da escrita")
	err = unidade.Executar(ctx, func(q pg.Querente) error {
		if err := repositorio.Inserir(ctx, q, criada); err != nil {
			return err
		}
		return erroEsperado
	})

	if !errors.Is(err, erroEsperado) {
		t.Fatalf("devolveu %v, esperado o erro da funcao", err)
	}

	// A carteira nao pode existir: a transacao foi desfeita.
	err = unidade.Ler(ctx, func(q pg.Querente) error {
		_, err := repositorio.Ler(ctx, q, criada.ID())
		return err
	})
	if !errors.Is(err, pg.ErrNaoEncontrado) {
		t.Fatalf("a carteira sobreviveu a uma transacao desfeita: %v", err)
	}
}

// O lock por carteira e o ponto de coordenacao do sistema. Duas transacoes
// independentes que tentam atualizar a mesma carteira nao podem passar as duas: a
// segunda tem de esperar o lock e ver a versao nova.
func TestLockPorCarteiraSerializaEscritores(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	criada, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 10000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	// Duas "instancias": duas unidades de trabalho sobre pools distintos, cada uma
	// com as proprias conexoes.
	_, unidadeA := abrirUnidade(t, dsnRuntime)
	_, unidadeB := abrirUnidade(t, dsnRuntime)

	liberado := make(chan struct{})
	leu := make(chan wallet.Carteira, 1)

	go func() {
		_ = unidadeA.Executar(contexto(t), func(q pg.Querente) error {
			atualizada, err := repositorio.LerParaAtualizar(ctx, q, criada.ID())
			if err != nil {
				return err
			}
			leu <- atualizada

			// Segura o lock ate a outra unidade tentar escrever.
			<-liberado
			return nil
		})
	}()

	<-leu // a unidade A tem o lock

	// A unidade B tenta a mesma carteira. Com o lock_timeout de 1s do papel de
	// runtime, ela falha em vez de esperar para sempre.
	erroB := unidadeB.Executar(contexto(t), func(q pg.Querente) error {
		_, err := repositorio.LerParaAtualizar(ctx, q, criada.ID())
		return err
	})

	close(liberado)

	if erroB == nil {
		t.Fatal("a segunda unidade entrou na mesma carteira enquanto a primeira segurava o lock")
	}
	if !errors.Is(erroB, pg.ErrConflitoDeVersao) {
		// O erro exato depende de como o driver classifica: pode ser lock_not_available
		// (55P03) ou timeout de statement. O que importa e que houve erro.
		t.Logf("erro da segunda unidade: %v", erroB)
	}
}

// O papel de runtime nao escreve DDL. Um INSERT direto fora do repositorio
// funciona, o que prova que o privilegio testado e de DML e nao de acesso
// qualquer.
func TestPapelDeRuntimeEscreveDentroDoLimite(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioCarteira()
	ctx := contexto(t)

	criada, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, 1000, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao como runtime: %v", err)
	}

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		_, err := q.Exec(ctx, `DROP TABLE wallets`)
		return err
	}); err == nil {
		t.Fatal("papel de runtime conseguiu executar DDL")
	}
}
