//go:build integration

package repositorios

import (
	"errors"
	"testing"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wagering"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
	"github.com/LuisMassuchini/backend-challenge-go/internal/pg"
	"github.com/LuisMassuchini/backend-challenge-go/tests/integration/dbtest"
)

// betValida cria uma transacao externa valida para os testes de repositorio.
func betValida(t *testing.T, carteira wallet.Identificador, jogador wallet.Identificador, externo, chave, hash string, valor string) wagering.Transacao {
	t.Helper()

	tr, err := wagering.Registrar(wagering.Registro{
		Provedor:          wagering.Provedor("provider-a"),
		TransacaoExterna:  wagering.Externo(externo),
		ChaveIdempotencia: wagering.Chave(chave),
		HashConteudo:      wagering.Hash(hash),
		Carteira:          carteira,
		Jogador:           jogador,
		Rodada:            wagering.Rodada("round-987"),
		Jogo:              wagering.Jogo("fortune-chimp"),
		Tipo:              wagering.TipoBET,
		Valor:             dinheiro(t, centavos(t, valor), "BRL"),
		CriadaEm:          agoraTeste(),
	})
	if err != nil {
		t.Fatalf("wagering.Registrar: %v", err)
	}
	return tr
}

func centavos(t *testing.T, valor string) int64 {
	t.Helper()
	switch valor {
	case "25.00":
		return 2500
	case "80.00":
		return 8000
	}
	t.Fatalf("valor de teste desconhecido: %q", valor)
	return 0
}

func carteiraDeTeste(t *testing.T, d string, saldo int64) wallet.Carteira {
	t.Helper()
	c, err := wallet.Nova(idValido(t, jogadorA), dinheiro(t, saldo, "BRL"), agoraTeste())
	if err != nil {
		t.Fatalf("wallet.Nova: %v", err)
	}
	inserirCarteiraDireto(t, d, c)
	return c
}

func TestInserirEBuscarTransacaoPorChave(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	criada := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "provider-a:transaction-123", "hash-1", "25.00")

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	var lida wagering.Transacao
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.BuscarPorChave(ctx, q, criada.ChaveIdempotencia())
		return err
	}); err != nil {
		t.Fatalf("busca por chave: %v", err)
	}

	if lida.ID() != criada.ID() {
		t.Errorf("ID e %s, esperado %s", lida.ID(), criada.ID())
	}
	if lida.Provedor() != criada.Provedor() {
		t.Errorf("provedor e %q, esperado %q", lida.Provedor(), criada.Provedor())
	}
	if lida.TransacaoExterna() != criada.TransacaoExterna() {
		t.Errorf("externo e %q, esperado %q", lida.TransacaoExterna(), criada.TransacaoExterna())
	}
	if !lida.Valor().Equal(criada.Valor()) {
		t.Errorf("valor e %s, esperado %s", lida.Valor(), criada.Valor())
	}
	if lida.Tipo() != criada.Tipo() {
		t.Errorf("tipo e %q, esperado %q", lida.Tipo(), criada.Tipo())
	}
	if lida.Estado() != wagering.EstadoPendente {
		t.Errorf("estado e %q, esperado %q", lida.Estado(), wagering.EstadoPendente)
	}
	if lida.Rodada() != criada.Rodada() || lida.Jogo() != criada.Jogo() {
		t.Error("rodada ou jogo perdidos na ida e volta")
	}
}

// A mesma chave duas vezes e conflito, e o erro e classificado. E o primeiro dos
// dois indices de idempotencia.
func TestChaveDeIdempotenciaRepetidaDaConflito(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	primeira := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-1", "chave-1", "hash-1", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, primeira)
	}); err != nil {
		t.Fatalf("primeira insercao: %v", err)
	}

	// Mesma chave, outra operacao externa: e o replay legitimo e a tentativa de
	// duplicar que a idempotencia precisa bloquear.
	segunda := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-2", "chave-1", "hash-2", "25.00")
	err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, segunda)
	})

	if !errors.Is(err, pg.ErrConflitoDeChave) {
		t.Fatalf("devolveu %v, esperado ErrConflitoDeChave", err)
	}
}

// A mesma operacao externa com outra chave e conflito tambem. E o segundo indice,
// e e o que pega o cliente que mudou o esquema da chave entre duas chamadas.
func TestMesmaOperacaoExternaComOutraChaveDaConflito(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	primeira := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, primeira)
	}); err != nil {
		t.Fatalf("primeira insercao: %v", err)
	}

	segunda := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-2", "hash-1", "25.00")
	err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, segunda)
	})

	if !errors.Is(err, pg.ErrConflitoDeChave) {
		t.Fatalf("devolveu %v, esperado ErrConflitoDeChave", err)
	}
}

func TestBuscarTransacaoInexistenteDevolveNaoEncontrado(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	ctx := contexto(t)

	err := unidade.Ler(ctx, func(q pg.Querente) error {
		_, err := repositorio.BuscarPorChave(ctx, q, "chave-que-nao-existe")
		return err
	})
	if !errors.Is(err, pg.ErrNaoEncontrado) {
		t.Fatalf("devolveu %v, esperado ErrNaoEncontrado", err)
	}
}

// O resultado original e persistido e e ele que o replay devolve. Recalcular o
// saldo no replay daria a resposta de hoje para uma operacao de ontem.
func TestResultadoOriginalEPersistido(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	criada := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	observado := dinheiro(t, 7500, "BRL")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Concluir(ctx, q, criada.ID(), pg.ResultadoGravado{
			Estado:    wagering.EstadoProcessado,
			Resultado: observado,
		})
	}); err != nil {
		t.Fatalf("conclusao: %v", err)
	}

	var lida wagering.Transacao
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.BuscarPorChave(ctx, q, criada.ChaveIdempotencia())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}

	if lida.Estado() != wagering.EstadoProcessado {
		t.Errorf("estado e %q, esperado %q", lida.Estado(), wagering.EstadoProcessado)
	}
	if !lida.Resultado().Equal(observado) {
		t.Errorf("resultado e %s, esperado %s", lida.Resultado(), observado)
	}
}

// Uma transacao terminal nao volta. E o que impede que um worker reprocesse o que
// ja terminou, que e o caminho mais curto para movimentacao duplicada.
func TestTransacaoTerminalNaoVoltaAoNaoTerminal(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	criada := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Concluir(ctx, q, criada.ID(), pg.ResultadoGravado{
			Estado:    wagering.EstadoProcessado,
			Resultado: dinheiro(t, 7500, "BRL"),
		})
	}); err != nil {
		t.Fatalf("primeira conclusao: %v", err)
	}

	// A segunda conclusao tem de falhar, tanto no banco quanto no dominio.
	err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Concluir(ctx, q, criada.ID(), pg.ResultadoGravado{
			Estado:    wagering.EstadoProcessado,
			Resultado: dinheiro(t, 1000, "BRL"),
		})
	})
	if !errors.Is(err, pg.ErrConclusaoImpossivel) {
		t.Fatalf("devolveu %v, esperado ErrConclusaoImpossivel", err)
	}

	// E o estado persistido continua o primeiro.
	var lida wagering.Transacao
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.BuscarPorID(ctx, q, criada.ID())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}
	if !lida.Resultado().Equal(dinheiro(t, 7500, "BRL")) {
		t.Errorf("resultado e %s, esperado o primeiro: a segunda conclusao nao pode ter passado", lida.Resultado())
	}
}

// Rejeicao persiste o codigo de falha, e nao o resultado. E a distincao que
// responde "o cliente corrigiu a entrada ou o resultado e definitivo".
func TestRejeicaoPersisteCodigoDeFalha(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	criada := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Concluir(ctx, q, criada.ID(), pg.ResultadoGravado{
			Estado:      wagering.EstadoRejeitado,
			CodigoFalha: wagering.CodigoFalhaSemSaldo,
		})
	}); err != nil {
		t.Fatalf("rejeicao: %v", err)
	}

	var lida wagering.Transacao
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.BuscarPorChave(ctx, q, criada.ChaveIdempotencia())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}

	if lida.Estado() != wagering.EstadoRejeitado {
		t.Errorf("estado e %q, esperado %q", lida.Estado(), wagering.EstadoRejeitado)
	}
	if lida.CodigoFalha() != wagering.CodigoFalhaSemSaldo {
		t.Errorf("codigo e %q, esperado %q", lida.CodigoFalha(), wagering.CodigoFalhaSemSaldo)
	}
	if lida.Resultado().Valida() {
		t.Error("rejeicao gravou resultado financeiro")
	}
}

// Conclusao com dados que nao fecham com o estado e recusada antes de chegar ao
// banco. O CHECK do banco tambem recusaria, e o erro do dominio chega primeiro e
// nomeia o que faltou.
func TestConclusaoIncoerenteRecusada(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	criada := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	casos := map[string]pg.ResultadoGravado{
		"processada sem resultado": {Estado: wagering.EstadoProcessado},
		"rejeitada sem codigo":     {Estado: wagering.EstadoRejeitado},
		"estado nao terminal":      {Estado: wagering.EstadoPendente},
	}

	for nome, resultado := range casos {
		t.Run(nome, func(t *testing.T) {
			err := unidade.Executar(ctx, func(q pg.Querente) error {
				return repositorio.Concluir(ctx, q, criada.ID(), resultado)
			})
			if !errors.Is(err, pg.ErrEstadoInvalido) {
				t.Errorf("devolveu %v, esperado ErrEstadoInvalido", err)
			}
		})
	}
}

// Referencia pendente: o estado muda, e a tentativa de mudar de novo e recusada
// porque a transacao ja nao esta em PENDING.
func TestMarcaReferenciaPendente(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	reversao, err := wagering.Registrar(wagering.Registro{
		Provedor:          wagering.Provedor("provider-a"),
		TransacaoExterna:  wagering.Externo("transaction-999"),
		ChaveIdempotencia: wagering.Chave("chave-reversao"),
		HashConteudo:      wagering.Hash("hash-2"),
		Carteira:          carteira.ID(),
		Jogador:           carteira.Jogador(),
		Rodada:            wagering.Rodada("round-987"),
		Jogo:              wagering.Jogo("fortune-chimp"),
		Tipo:              wagering.TipoROLLBACK,
		Valor:             dinheiro(t, 2500, "BRL"),
		Referencia:        wagering.Referencia{Externa: wagering.Externo("transaction-123")},
		CriadaEm:          agoraTeste(),
	})
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, reversao)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.MarcarPendentePorReferencia(ctx, q, reversao.ID())
	}); err != nil {
		t.Fatalf("marcacao: %v", err)
	}

	var lida wagering.Transacao
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.BuscarPorID(ctx, q, reversao.ID())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}
	if lida.Estado() != wagering.EstadoPendenteReferencia {
		t.Errorf("estado e %q, esperado %q", lida.Estado(), wagering.EstadoPendenteReferencia)
	}
	if lida.Referencia().Externa != "transaction-123" {
		t.Errorf("referencia externa e %q", lida.Referencia().Externa)
	}

	// A segunda marcacao falha: ja nao esta em PENDING.
	err = unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.MarcarPendentePorReferencia(ctx, q, reversao.ID())
	})
	if !errors.Is(err, pg.ErrConclusaoImpossivel) {
		t.Errorf("devolveu %v, esperado ErrConclusaoImpossivel", err)
	}
}

// Resolver a referencia liga a reversao a transacao que ela reverte, e a leitura
// traz as duas: e o que permite ao worker decidir o movimento contrario.
func TestResolverReferencia(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	aposta := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, aposta)
	}); err != nil {
		t.Fatalf("insercao da aposta: %v", err)
	}

	reversao, err := wagering.Registrar(wagering.Registro{
		Provedor:          wagering.Provedor("provider-a"),
		TransacaoExterna:  wagering.Externo("transaction-999"),
		ChaveIdempotencia: wagering.Chave("chave-reversao"),
		HashConteudo:      wagering.Hash("hash-2"),
		Carteira:          carteira.ID(),
		Jogador:           carteira.Jogador(),
		Rodada:            wagering.Rodada("round-987"),
		Jogo:              wagering.Jogo("fortune-chimp"),
		Tipo:              wagering.TipoROLLBACK,
		Valor:             dinheiro(t, 2500, "BRL"),
		Referencia:        wagering.Referencia{Externa: wagering.Externo("transaction-123")},
		CriadaEm:          agoraTeste(),
	})
	if err != nil {
		t.Fatalf("Registrar: %v", err)
	}
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, reversao)
	}); err != nil {
		t.Fatalf("insercao da reversao: %v", err)
	}

	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.ResolverReferencia(ctx, q, reversao.ID(), aposta.ID())
	}); err != nil {
		t.Fatalf("resolucao: %v", err)
	}

	var lida wagering.Transacao
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.BuscarPorID(ctx, q, reversao.ID())
		return err
	}); err != nil {
		t.Fatalf("leitura: %v", err)
	}
	if lida.ReferenciaInterna() != aposta.ID() {
		t.Errorf("referencia interna e %s, esperado %s", lida.ReferenciaInterna(), aposta.ID())
	}
}

// A busca por provedor e identificador externo e o indice que impede a mesma
// operacao financeira de aparecer com duas chaves.
func TestBuscarPorProvedorEExterno(t *testing.T) {
	dbtest.Limpa(t)
	_, unidade := abrirUnidade(t, dsnRuntime)
	repositorio := pg.NovaRepositorioTransacoes()
	carteira := carteiraDeTeste(t, dsnDono, 10000)
	ctx := contexto(t)

	criada := betValida(t, carteira.ID(), carteira.Jogador(), "transaction-123", "chave-1", "hash-1", "25.00")
	if err := unidade.Executar(ctx, func(q pg.Querente) error {
		return repositorio.Inserir(ctx, q, criada)
	}); err != nil {
		t.Fatalf("insercao: %v", err)
	}

	var lida wagering.Transacao
	if err := unidade.Ler(ctx, func(q pg.Querente) error {
		var err error
		lida, err = repositorio.BuscarPorProvedorEExterno(ctx, q, "provider-a", "transaction-123")
		return err
	}); err != nil {
		t.Fatalf("busca: %v", err)
	}
	if lida.ID() != criada.ID() {
		t.Errorf("ID e %s, esperado %s", lida.ID(), criada.ID())
	}

	// Outro provedor nao enxerga a transacao: e o isolamento entre provedores
	// Check no nivel do dado.
	err := unidade.Ler(ctx, func(q pg.Querente) error {
		_, err := repositorio.BuscarPorProvedorEExterno(ctx, q, "provider-b", "transaction-123")
		return err
	})
	if !errors.Is(err, pg.ErrNaoEncontrado) {
		t.Errorf("provedor B achou a transacao do provedor A: %v", err)
	}
}
