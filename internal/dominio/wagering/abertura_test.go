package wagering

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
)

// OPENING e a unica operacao de origem interna. Ela nao vem de provedor, nao tem
// identificador externo, nao tem chave de idempotencia, nao tem rodada nem jogo,
// e nasce em PROCESSED: a abertura e confirmada na mesma transacao que cria a
// carteira.

// estes testes fixam a separacao entre origem interna e origem externa. A regra e
// eliminatoria no enunciado: OPENING enviado por HTTP ou SQS tem de ser recusado,
// e recusado com codigo estavel, e nao aceito e ignorado depois.

func TestAberturaPelaOrigemExternaRecusa(t *testing.T) {
	_, err := Registrar(Registro{
		Provedor:          Provedor(provedorA),
		TransacaoExterna:  Externo(externoA),
		ChaveIdempotencia: Chave(chaveA),
		HashConteudo:      Hash(hashA),
		Carteira:          deveID(t, carteiraA),
		Jogador:           deveID(t, jogadorA),
		Rodada:            Rodada(rodadaA),
		Jogo:              Jogo(jogoA),
		Tipo:              TipoAbertura,
		Valor:             deveParse(t, "1000.00"),
		CriadaEm:          instanteFixo(),
	})
	if !errors.Is(err, ErrTipoNaoSuportado) {
		t.Fatalf("devolveu %v, esperado ErrTipoNaoSuportado", err)
	}
}

// O consumidor da entrada precisa saber qual codigo de falha devolver. Por isso a
// recusa de OPENING externo tem codigo proprio, e nao o generico de registro
// invalido: sao coisas que o cliente corrige de jeitos diferentes.
func TestAberturaExternaTemCodigoDeFalhaProprio(t *testing.T) {
	_, err := Registrar(Registro{
		Provedor:          Provedor(provedorA),
		TransacaoExterna:  Externo(externoA),
		ChaveIdempotencia: Chave(chaveA),
		HashConteudo:      Hash(hashA),
		Carteira:          deveID(t, carteiraA),
		Jogador:           deveID(t, jogadorA),
		Rodada:            Rodada(rodadaA),
		Jogo:              Jogo(jogoA),
		Tipo:              TipoAbertura,
		Valor:             deveParse(t, "1000.00"),
		CriadaEm:          instanteFixo(),
	})

	var recusada *FalhaDeRegra
	if !errors.As(err, &recusada) {
		t.Fatalf("devolveu %T, esperado *FalhaDeRegra: a entrada precisa classificar a rejeicao", err)
	}
	if recusada.Codigo != CodigoFalhaTipoNaoSuportado {
		t.Errorf("Codigo e %q, esperado %q", recusada.Codigo, CodigoFalhaTipoNaoSuportado)
	}
}

// Todos os tipos externos legveis passam, e so OPENING nao. Se a recusa fosse
// feita por lista de permitidos, um tipo novo do enunciado seria barrado ate
// alguem lembrar de adicionar na lista.
func TestTiposExternosLegiveis(t *testing.T) {
	legiveis := []Tipo{TipoBET, TipoWIN, TipoLOSS, TipoREFUND, TipoROLLBACK}

	for _, tipo := range legiveis {
		t.Run(string(tipo), func(t *testing.T) {
			// LOSS e o tipo invertido: exige zero. Aqui o que se testa e que o
			// tipo chega a ser aceito como tipo, e nao a politica de valor, que
			// tem suite propria.
			valor := "25.00"
			if tipo == TipoLOSS {
				valor = "0.00"
			}
			registro := Registro{
				Provedor:          Provedor(provedorA),
				TransacaoExterna:  Externo(externoA),
				ChaveIdempotencia: Chave(chaveA),
				HashConteudo:      Hash(hashA),
				Carteira:          deveID(t, carteiraA),
				Jogador:           deveID(t, jogadorA),
				Rodada:            Rodada(rodadaA),
				Jogo:              Jogo(jogoA),
				Tipo:              tipo,
				Valor:             deveParse(t, valor),
				CriadaEm:          instanteFixo(),
			}
			if tipo.ExigeReferencia() {
				registro.Referencia = Referencia{Externa: Externo("transaction-123")}
			}

			if _, err := Registrar(registro); err != nil {
				t.Errorf("Registrar %s: %v", tipo, err)
			}
		})
	}
}

// A abertura interna nasce em PROCESSED e carrega o saldo resultante. O
// enunciado pede exatamente isso: a abertura, o lancamento de credito e os
// eventos nascem no mesmo commit da carteira.
func TestAbrirCarteiraCriaAberturaProcessada(t *testing.T) {
	abertura, err := AbrirCarteira(Abertura{
		Carteira: deveID(t, carteiraA),
		Jogador:  deveID(t, jogadorA),
		Valor:    deveParse(t, "1000.00"),
		CriadaEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}

	if abertura.Tipo() != TipoAbertura {
		t.Errorf("Tipo e %q, esperado %q", abertura.Tipo(), TipoAbertura)
	}
	if abertura.Estado() != EstadoProcessado {
		t.Errorf("Estado e %q, esperado %q", abertura.Estado(), EstadoProcessado)
	}
	if !abertura.Resultado().Equal(deveParse(t, "1000.00")) {
		t.Errorf("Resultado e %s, esperado 1000.00", abertura.Resultado())
	}
	if !abertura.ID().Valida() {
		t.Error("abertura sem id interno valido")
	}
}

// A identidade interna e estavel e gerada pelo servico. A abertura nao tem
// identificador externo porque nao veio de fora, e nao tem chave de idempotencia
// porque nao ha provedor para chavear.
func TestAberturaNaoCarregaIdentidadeExterna(t *testing.T) {
	abertura, err := AbrirCarteira(Abertura{
		Carteira: deveID(t, carteiraA),
		Jogador:  deveID(t, jogadorA),
		Valor:    deveParse(t, "1000.00"),
		CriadaEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}

	if abertura.Provedor().Valida() {
		t.Errorf("abertura tem provedor %q", abertura.Provedor())
	}
	if abertura.TransacaoExterna().Valida() {
		t.Errorf("abertura tem transacao externa %q", abertura.TransacaoExterna())
	}
	if abertura.ChaveIdempotencia().Valida() {
		t.Errorf("abertura tem chave de idempotencia %q", abertura.ChaveIdempotencia())
	}
	if abertura.HashConteudo().Valida() {
		t.Errorf("abertura tem hash de conteudo %q", abertura.HashConteudo())
	}
	if abertura.Rodada().Valida() {
		t.Errorf("abertura tem rodada %q", abertura.Rodada())
	}
	if abertura.Jogo().Valida() {
		t.Errorf("abertura tem jogo %q", abertura.Jogo())
	}
	if !abertura.Referencia().Vazia() {
		t.Errorf("abertura tem referencia %v", abertura.Referencia())
	}
	if abertura.CodigoFalha().Presente() {
		t.Errorf("abertura processada tem codigo de falha %q", abertura.CodigoFalha())
	}
}

// Saldo inicial zero nao cria OPENING, nem lancamento, nem evento. Criar uma
// transacao sem efeito aqui inflaria o ledger e o numero de eventos de um
// OPENING por jogador sem nenhum movimento por tras.
func TestAbrirCarteiraComSaldoZeroRecusa(t *testing.T) {
	_, err := AbrirCarteira(Abertura{
		Carteira: deveID(t, carteiraA),
		Jogador:  deveID(t, jogadorA),
		Valor:    money.Zero(money.CurrencyBRL),
		CriadaEm: instanteFixo(),
	})
	if !errors.Is(err, ErrAberturaSemSaldo) {
		t.Fatalf("devolveu %v, esperado ErrAberturaSemSaldo", err)
	}
}

func TestAbrirCarteiraRecusaValorInvalido(t *testing.T) {
	casos := map[string]Abertura{
		"valor nao inicializado": {
			Carteira: deveID(t, carteiraA),
			Jogador:  deveID(t, jogadorA),
			CriadaEm: instanteFixo(),
		},
		"carteira ausente": {
			Jogador:  deveID(t, jogadorA),
			Valor:    deveParse(t, "10.00"),
			CriadaEm: instanteFixo(),
		},
		"jogador ausente": {
			Carteira: deveID(t, carteiraA),
			Valor:    deveParse(t, "10.00"),
			CriadaEm: instanteFixo(),
		},
		"instante ausente": {
			Carteira: deveID(t, carteiraA),
			Jogador:  deveID(t, jogadorA),
			Valor:    deveParse(t, "10.00"),
		},
	}

	for nome, abertura := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := AbrirCarteira(abertura); err == nil {
				t.Fatal("AbrirCarteira aceitou dados invalidos")
			}
		})
	}
}

// A abertura nao pode ser reprocessada. Reexecutar a abertura de uma carteira ja
// aberta e o caminho para credito inicial duplicado, que o enunciado proibe
// explicitamente.
func TestAberturaProcessadaNaoAceitaTransicao(t *testing.T) {
	abertura, err := AbrirCarteira(Abertura{
		Carteira: deveID(t, carteiraA),
		Jogador:  deveID(t, jogadorA),
		Valor:    deveParse(t, "1000.00"),
		CriadaEm: instanteFixo(),
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}

	if _, err := abertura.Processar(deveParse(t, "1000.00"), instanteFixo()); !errors.Is(err, ErrEstadoTerminal) {
		t.Errorf("Processar devolveu %v, esperado ErrEstadoTerminal", err)
	}
	if _, err := abertura.Rejeitar(CodigoFalhaSemSaldo, instanteFixo()); !errors.Is(err, ErrEstadoTerminal) {
		t.Errorf("Rejeitar devolveu %v, esperado ErrEstadoTerminal", err)
	}
}

// A reidratacao de uma abertura precisa reconhecer que ela e interna. Se a
// reidratacao aceitasse OPENING com provedor, um registro com identidade externa
// poderia nascer de um backup adulterado.
func TestReidratacaoDeAberturaRecusaIdentidadeExterna(t *testing.T) {
	abertura := Dados{
		ID:           deveID(t, carteiraA),
		Carteira:     deveID(t, carteiraA),
		Jogador:      deveID(t, jogadorA),
		Tipo:         TipoAbertura,
		Valor:        deveParse(t, "1000.00"),
		Estado:       EstadoProcessado,
		Resultado:    deveParse(t, "1000.00"),
		CriadaEm:     instanteFixo(),
		AtualizadaEm: instanteFixo(),
	}

	if _, err := Reidratar(abertura); err != nil {
		t.Fatalf("abertura reidratada sem identidade externa foi recusada: %v", err)
	}

	// Cada campo externo, um por vez: a recusa precisa nomear o campo que encontrou,
	// e nao apenas dizer que algo nao estava certo.
	campos := map[string]func(*Dados){
		"provedor":          func(d *Dados) { d.Provedor = Provedor(provedorA) },
		"transacao externa": func(d *Dados) { d.TransacaoExterna = Externo(externoA) },
		"chave":             func(d *Dados) { d.ChaveIdempotencia = Chave(chaveA) },
		"hash":              func(d *Dados) { d.HashConteudo = Hash(hashA) },
		"rodada":            func(d *Dados) { d.Rodada = Rodada(rodadaA) },
		"jogo":              func(d *Dados) { d.Jogo = Jogo(jogoA) },
		"referencia":        func(d *Dados) { d.Referencia = Referencia{Externa: Externo(externoA)} },
	}

	for nome, aplicar := range campos {
		t.Run(nome, func(t *testing.T) {
			adulterada := abertura
			aplicar(&adulterada)

			_, err := Reidratar(adulterada)
			if !errors.Is(err, ErrRegistroInvalido) {
				t.Fatalf("devolveu %v, esperado ErrRegistroInvalido", err)
			}
			if !strings.Contains(err.Error(), nome[:4]) {
				t.Errorf("a mensagem nao nomeia o campo encontrado: %v", err)
			}
		})
	}
}

// A data da abertura e o instante do evento. Uma abertura com instante no futuro
// produz evento com occurredAt no futuro, e o consumidor do evento ordena por
// instante e processa a carteira antes de existir.
func TestAberturaUsaOCampoCriadoComoAtualizado(t *testing.T) {
	quando := instanteFixo().Add(time.Minute)

	abertura, err := AbrirCarteira(Abertura{
		Carteira: deveID(t, carteiraA),
		Jogador:  deveID(t, jogadorA),
		Valor:    deveParse(t, "10.00"),
		CriadaEm: quando,
	})
	if err != nil {
		t.Fatalf("AbrirCarteira: %v", err)
	}

	if !abertura.CriadaEm().Equal(quando) {
		t.Errorf("CriadaEm e %v, esperado %v", abertura.CriadaEm(), quando)
	}
	if !abertura.AtualizadaEm().Equal(quando) {
		t.Errorf("AtualizadaEm e %v, esperado %v", abertura.AtualizadaEm(), quando)
	}
}
