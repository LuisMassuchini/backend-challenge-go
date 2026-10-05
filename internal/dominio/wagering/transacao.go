// Package wagering e a operacao externa sobre a carteira do jogador.
//
// Uma Transacao carrega os dois identificadores do mesmo negocio -- o interno e
// o externo --, o provedor, a chave de idempotencia, o hash do conteudo, a
// carteira, o jogador, a rodada, o jogo, o tipo, o valor, a referencia externa,
// o estado e os instantes. Quando aplicavel, tambem a referencia interna
// resolvida, o codigo de falha e o resultado financeiro devolvido ao provedor.
//
// A maquina de estados tem cinco estados e tres deles terminais. A distincao
// entre rejeicao e falha e o que permite responder a operacao correta depois:
// rejeicao e regra de negocio, o cliente pode corrigir a entrada e reenviar;
// falha permanente e infraestrutura, reenviar nao muda o resultado e o cliente
// nao tem o que corrigir.
package wagering

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/money"
	"github.com/LuisMassuchini/backend-challenge-go/internal/dominio/wallet"
)

// Estado e a situacao da transacao.
type Estado string

const (
	// EstadoPendente significa registro aceito, com processamento ainda nao
	// concluido. E o estado de entrada de toda transacao externa.
	EstadoPendente Estado = "PENDING"
	// EstadoPendenteReferencia significa que a operacao depende de uma
	// referencia que ainda nao chegou.
	EstadoPendenteReferencia Estado = "PENDING_REFERENCE"
	// EstadoProcessado significa operacao concluida com sucesso. Terminal.
	EstadoProcessado Estado = "PROCESSED"
	// EstadoRejeitado significa operacao recusada por regra de negocio. Terminal.
	EstadoRejeitado Estado = "REJECTED"
	// EstadoFalhou significa falha permanente de infraestrutura, registrada para
	// auditoria. Terminal.
	EstadoFalhou Estado = "FAILED"
)

// Valido informa se o estado pertence ao conjunto conhecido.
//
// O conjunto e fechado. Um estado aceito por engano passaria a ser gravado e a
// aparecer em consulta de auditoria sem que ninguem soubesse como trata-lo.
func (e Estado) Valido() bool {
	switch e {
	case EstadoPendente, EstadoPendenteReferencia, EstadoProcessado, EstadoRejeitado, EstadoFalhou:
		return true
	}
	return false
}

// Terminal informa se o estado nao aceita mais transicoes.
//
// Reprocessar uma transacao terminal e o caminho mais curto para movimentacao
// duplicada: o debito ja foi gravado e o lancamento ja esta no ledger, e uma
// segunda execucao da regra produz o segundo debito.
func (e Estado) Terminal() bool {
	switch e {
	case EstadoProcessado, EstadoRejeitado, EstadoFalhou:
		return true
	}
	return false
}

// CodigoFalha explica, de forma estavel, por que a operacao nao foi concluida.
//
// O codigo e parte do contrato com o provedor e da auditoria. Ele e uma constante
// versionada, nunca um texto livre: um texto livre muda de grafia entre
// deploys e a consulta que agrupa por motivo para de agrupar.
type CodigoFalha string

const (
	// CodigoFalhaSemSaldo cobre aposta recusada por saldo insuficiente. E
	// corrigivel pelo provedor, que pode aguardar deposito.
	CodigoFalhaSemSaldo CodigoFalha = "BET_SEM_SALDO"

	// CodigoFalhaReverSaoSemSaldo cobre reversao que precisaria debitar mais que
	// o saldo disponivel. E um codigo diferente de CodigoFalhaSemSaldo de
	// proposito: no primeiro o jogador nao tinha dinheiro para a aposta, no
	// segundo o jogador tem saldo mas nao o suficiente para o movimento inverso.
	// Tratar os dois como o mesmo motivo esconde um problema contabil real.
	CodigoFalhaReverSaoSemSaldo CodigoFalha = "ROLLBACK_SEM_SALDO"

	// CodigoFalhaValorNaoPositivo cobre BET, WIN, REFUND ou ROLLBACK com valor
	// que nao e maior que zero.
	CodigoFalhaValorNaoPositivo CodigoFalha = "VALOR_NAO_POSITIVO"

	// CodigoFalhaValorDeZeroEsperado cobre LOSS com valor diferente de zero.
	CodigoFalhaValorDeZeroEsperado CodigoFalha = "LOSS_COM_VALOR_DIFERENTE_DE_ZERO"

	// CodigoFalhaReferenciaObrigatoria cobre REFUND ou ROLLBACK sem
	// referenceExternalTransactionId.
	CodigoFalhaReferenciaObrigatoria CodigoFalha = "REFERENCIA_OBRIGATORIA"

	// CodigoFalhaReferenciaNaoEncontrada cobre reversao cuja referencia nunca
	// chegou, depois de esgotadas as tentativas.
	CodigoFalhaReferenciaNaoEncontrada CodigoFalha = "REFERENCIA_NAO_ENCONTRADA"

	// CodigoFalhaReferenciaIncompativel cobre referencia que existe mas nao
	// concorda com a operacao em provedor, jogador, carteira, moeda ou rodada.
	CodigoFalhaReferenciaIncompativel CodigoFalha = "REFERENCIA_INCOMPATIVEL"

	// CodigoFalhaValorDivergente cobre reversao cujo valor difere do valor
	// referenciado. Reversao parcial nao faz parte do desafio.
	CodigoFalhaValorDivergente CodigoFalha = "VALOR_DIVERGENTE_DA_REFERENCIA"

	// CodigoFalhaReferenciaNaoProcessada cobre reversao de operacao que ainda
	// nao terminou com sucesso.
	CodigoFalhaReferenciaNaoProcessada CodigoFalha = "REFERENCIA_NAO_PROCESSADA"

	// CodigoFalhaReversaoJaAplicada cobre segunda reversao bem-sucedida sobre a
	// mesma referencia. Sem este codigo, uma devolucion repetida passaria como
	// se fosse uma operacao nova.
	CodigoFalhaReversaoJaAplicada CodigoFalha = "REVERSAO_JA_APLICADA"

	// CodigoFalhaTipoNaoSuportado cobre OPENING enviado por HTTP ou SQS.
	CodigoFalhaTipoNaoSuportado CodigoFalha = "TIPO_NAO_SUPORTADO_NA_ORIGEM_EXTERNA"

	// CodigoFalhaPersistencia cobre falha permanente de infraestrutura
	// registrada para auditoria.
	CodigoFalhaPersistencia CodigoFalha = "FALHA_DE_PERSISTENCIA"
)

// Erros do dominio.
var (
	// ErrEstadoTerminal cobre transicao a partir de estado terminal.
	ErrEstadoTerminal = errors.New("wagering: transicao a partir de estado terminal")
	// ErrEstadoInvalido cobre estado fora do conjunto ou dados incompativeis com
	// o estado.
	ErrEstadoInvalido = errors.New("wagering: estado invalido")
	// ErrRegistroInvalido cobre dado de entrada ausente ou malformado.
	ErrRegistroInvalido = errors.New("wagering: registro invalido")
	// ErrCodigoFalhaInvalido cobre codigo de falha fora do conjunto.
	ErrCodigoFalhaInvalido = errors.New("wagering: codigo de falha invalido")
	// ErrResultadoInvalido cobre resultado financeiro ausente ou invalido.
	ErrResultadoInvalido = errors.New("wagering: resultado invalido")
)

// Provedor identifica o provedor de jogos que enviou a operacao.
//
// E tipo proprio e nao string porque a identidade do provedor determina quais
// transacoes o provedor pode ver e quais pode escrever. Tratar como texto em
// qualquer ponto e o caminho para o acesso cruzado entre provedores.
type Provedor string

// String devolve o identificador do provedor.
func (p Provedor) String() string { return string(p) }

// Valida informa se o provedor foi informado.
func (p Provedor) Valida() bool { return strings.TrimSpace(string(p)) != "" }

// Externo e o identificador da operacao no provedor de origem.
type Externo string

// String devolve o identificador externo.
func (e Externo) String() string { return string(e) }

// Valida informa se o identificador externo foi informado.
func (e Externo) Valida() bool { return strings.TrimSpace(string(e)) != "" }

// Chave e a chave de idempotencia recebida do provedor.
//
// O servidor nao substitui a chave recebida por outra calculada: o provedor pode
// escolher o esquema, e um servidor que recalcula rejeita o cliente legitimo
// cuja chave tem outro formato.
type Chave string

// String devolve a chave de idempotencia.
func (c Chave) String() string { return string(c) }

// Valida informa se a chave foi informada.
func (c Chave) Valida() bool { return strings.TrimSpace(string(c)) != "" }

// Hash e o resumo deterministico dos campos de negocio da operacao.
//
// E o que distingue replay legitimo de conflito: mesma chave com conteudo
// diferente devolve conflito, e nao um resultado antigo.
type Hash string

// String devolve o hash do conteudo.
func (h Hash) String() string { return string(h) }

// Valida informa se o hash foi informado.
func (h Hash) Valida() bool { return strings.TrimSpace(string(h)) != "" }

// Rodada e o identificador da rodada de jogos.
type Rodada string

// String devolve o identificador da rodada.
func (r Rodada) String() string { return string(r) }

// Valida informa se a rodada foi informada.
func (r Rodada) Valida() bool { return strings.TrimSpace(string(r)) != "" }

// Jogo e o identificador do jogo.
type Jogo string

// String devolve o identificador do jogo.
func (j Jogo) String() string { return string(j) }

// Valida informa se o jogo foi informado.
func (j Jogo) Valida() bool { return strings.TrimSpace(string(j)) != "" }

// Referencia aponta para a operacao que uma reversao desfaz.
//
// Externa e o identificador no provedor, que e o que chega na entrada. Interna e
// a transacao resolvida, preenchida quando a referencia e encontrada. As duas
// coexistem porque a entrada e always externa e a resolucao e interna, e um
// unico campo obrigaria a perder uma das duas.
type Referencia struct {
	// Externa e o referenceExternalTransactionId informado pelo provedor.
	Externa Externo
	// Interna e a transacao referenciada, depois de resolvida.
	Interna wallet.Identificador
}

// Vazio informa se a referencia nao foi informada.
func (r Referencia) Vazia() bool { return !r.Externa.Valida() && !r.Interna.Valida() }

// Registro e o conjunto de dados de entrada de uma operacao externa.
//
// E um struct, e nao uma lista de argumentos, porque trocar dois campos do mesmo
// tipo compila e grava a operacao na carteira errada.
type Registro struct {
	Provedor          Provedor
	TransacaoExterna  Externo
	ChaveIdempotencia Chave
	HashConteudo      Hash
	Carteira          wallet.Identificador
	Jogador           wallet.Identificador
	Rodada            Rodada
	Jogo              Jogo
	Tipo              Tipo
	Valor             money.Money
	Referencia        Referencia
	CriadaEm          time.Time
}

// Dados e o conjunto completo de uma transacao persistida.
//
// Usado apenas na reidratacao. A reidratacao repoe o que o banco tem, sem
// recalcular e sem emitir evento.
type Dados struct {
	ID                wallet.Identificador
	Provedor          Provedor
	TransacaoExterna  Externo
	ChaveIdempotencia Chave
	HashConteudo      Hash
	Carteira          wallet.Identificador
	Jogador           wallet.Identificador
	Rodada            Rodada
	Jogo              Jogo
	Tipo              Tipo
	Valor             money.Money
	Referencia        Referencia
	Estado            Estado
	CodigoFalha       CodigoFalha
	ReferenciaInterna wallet.Identificador
	Resultado         money.Money
	CriadaEm          time.Time
	AtualizadaEm      time.Time
}

// Transacao e uma operacao financeira sobre a carteira.
//
// Imutavel: toda transicao devolve uma transacao nova. O valor zero nao e uma
// transacao valida, e nao ha caminho que o torne uma sem passar pelo construtor.
type Transacao struct {
	id                wallet.Identificador
	provedor          Provedor
	externa           Externo
	chave             Chave
	hash              Hash
	carteira          wallet.Identificador
	jogador           wallet.Identificador
	rodada            Rodada
	jogo              Jogo
	tipo              Tipo
	valor             money.Money
	referencia        Referencia
	estado            Estado
	codigoFalha       CodigoFalha
	referenciaInterna wallet.Identificador
	resultado         money.Money
	criadaEm          time.Time
	atualizadaEm      time.Time
}

// Registrar cria uma transacao externa em PENDING.
//
// Nao ha estado inicial alternativo: toda operacao nasce em PENDING e chega a um
// estado terminal a partir dali. A alternativa de criar direto em PROCESSED
// existia para evitar gravacao intermedia de aceite, e foi descartada porque o
// enunciado exige retomada duravel de todo PENDING confirmado -- e uma
// transacao que ja nasce processada nunca pode ser retomada.
func Registrar(r Registro) (Transacao, error) {
	if err := validarRegistro(r); err != nil {
		return Transacao{}, err
	}

	agora := r.CriadaEm
	return Transacao{
		id:           wallet.NovoIdentificador(),
		provedor:     r.Provedor,
		externa:      r.TransacaoExterna,
		chave:        r.ChaveIdempotencia,
		hash:         r.HashConteudo,
		carteira:     r.Carteira,
		jogador:      r.Jogador,
		rodada:       r.Rodada,
		jogo:         r.Jogo,
		tipo:         r.Tipo,
		valor:        r.Valor,
		referencia:   r.Referencia,
		estado:       EstadoPendente,
		criadaEm:     agora,
		atualizadaEm: agora,
	}, nil
}

func validarRegistro(r Registro) error {
	// OPENING e interna. A recusa vem antes de qualquer outra verificacao porque
	// o tipo invalido aqui nao e entrada malformada: e um provedor tentando
	// executar uma operacao que so o servico interno pode executar, e a resposta
	// precisa dizer isso com o codigo proprio.
	if r.Tipo == TipoAbertura {
		return fmt.Errorf(
			"%w: %w", ErrTipoNaoSuportado,
			&FalhaDeRegra{
				Codigo: CodigoFalhaTipoNaoSuportado,
				Motivo: "OPENING e reservado a abertura interna de carteira",
			},
		)
	}

	if !r.Provedor.Valida() {
		return fmt.Errorf("%w: provedor ausente", ErrRegistroInvalido)
	}
	if !r.TransacaoExterna.Valida() {
		return fmt.Errorf("%w: transacao externa ausente", ErrRegistroInvalido)
	}
	if !r.ChaveIdempotencia.Valida() {
		return fmt.Errorf("%w: chave de idempotencia ausente", ErrRegistroInvalido)
	}
	if !r.HashConteudo.Valida() {
		return fmt.Errorf("%w: hash do conteudo ausente", ErrRegistroInvalido)
	}
	if !r.Carteira.Valida() {
		return fmt.Errorf("%w: carteira ausente", ErrRegistroInvalido)
	}
	if !r.Jogador.Valida() {
		return fmt.Errorf("%w: jogador ausente", ErrRegistroInvalido)
	}
	if !r.Rodada.Valida() {
		return fmt.Errorf("%w: rodada ausente", ErrRegistroInvalido)
	}
	if !r.Jogo.Valida() {
		return fmt.Errorf("%w: jogo ausente", ErrRegistroInvalido)
	}
	if !r.Tipo.Valido() {
		return fmt.Errorf("%w: tipo %q", ErrRegistroInvalido, r.Tipo)
	}
	if err := r.Valor.Validar(); err != nil {
		return fmt.Errorf("valor: %w", err)
	}
	// A politica de zero por tipo entra aqui, e nao na borda HTTP nem no
	// consumidor SQS. Aplicada nos dois caminhos de entrada, ela seria
	// duplicada; aplicada so na borda, o dominio aceitaria uma transacao que
	// nenhuma regra financeira consegue processar.
	if err := validarPoliticaDeValor(r.Tipo, r.Valor); err != nil {
		return err
	}
	if r.CriadaEm.IsZero() {
		return fmt.Errorf("%w: instante de criacao ausente", ErrRegistroInvalido)
	}
	return nil
}

// Reidratar reconstroi a transacao a partir do banco.
//
// Nao executa regra e nao emite evento: a transicao ja aconteceu e ja foi
// registrada. Recalcular aqui produziria um estado que nunca existiu.
func Reidratar(d Dados) (Transacao, error) {
	if !d.ID.Valida() {
		return Transacao{}, fmt.Errorf("%w: id ausente", ErrRegistroInvalido)
	}
	if !d.Estado.Valido() {
		return Transacao{}, fmt.Errorf("%w: estado %q", ErrEstadoInvalido, d.Estado)
	}
	// Uma abertura nao tem identidade externa. Aceitar uma no reidratar
	// admitiria registro vindo de backup adulterado em que a abertura tem
	// provedor e chave, que e a forma mais direta de duplicar credito inicial.
	if d.Tipo == TipoAbertura {
		if err := conferirOrigemInterna(d); err != nil {
			return Transacao{}, err
		}
	} else if err := validarRegistro(Registro{
		Provedor:          d.Provedor,
		TransacaoExterna:  d.TransacaoExterna,
		ChaveIdempotencia: d.ChaveIdempotencia,
		HashConteudo:      d.HashConteudo,
		Carteira:          d.Carteira,
		Jogador:           d.Jogador,
		Rodada:            d.Rodada,
		Jogo:              d.Jogo,
		Tipo:              d.Tipo,
		Valor:             d.Valor,
		Referencia:        d.Referencia,
		CriadaEm:          d.CriadaEm,
	}); err != nil {
		return Transacao{}, err
	}
	if err := conferirEstadoEDados(d); err != nil {
		return Transacao{}, err
	}

	return Transacao{
		id:                d.ID,
		provedor:          d.Provedor,
		externa:           d.TransacaoExterna,
		chave:             d.ChaveIdempotencia,
		hash:              d.HashConteudo,
		carteira:          d.Carteira,
		jogador:           d.Jogador,
		rodada:            d.Rodada,
		jogo:              d.Jogo,
		tipo:              d.Tipo,
		valor:             d.Valor,
		referencia:        d.Referencia,
		estado:            d.Estado,
		codigoFalha:       d.CodigoFalha,
		referenciaInterna: d.ReferenciaInterna,
		resultado:         d.Resultado,
		criadaEm:          d.CriadaEm,
		atualizadaEm:      d.AtualizadaEm,
	}, nil
}

// conferirOrigemInterna garante que uma transacao de origem interna nao carrega
// identidade externa.
//
// E o reflexo, na leitura, da regra que recusa OPENING na entrada: os dois lados
// da mesma invariante. Sem ela, o schema aceitaria um OPENING com provedor, e a
// constraint que impede credito inicial duplicado teria que confiar no codigo de
// aplicacao em vez do dado.
func conferirOrigemInterna(d Dados) error {
	if !d.Carteira.Valida() {
		return fmt.Errorf("%w: carteira ausente", ErrRegistroInvalido)
	}
	if !d.Jogador.Valida() {
		return fmt.Errorf("%w: jogador ausente", ErrRegistroInvalido)
	}
	if err := d.Valor.Validar(); err != nil {
		return fmt.Errorf("valor: %w", err)
	}
	if d.CriadaEm.IsZero() {
		return fmt.Errorf("%w: instante de criacao ausente", ErrRegistroInvalido)
	}

	var indevida []string
	if d.Provedor.Valida() {
		indevida = append(indevida, fmt.Sprintf("provedor %q", d.Provedor))
	}
	if d.TransacaoExterna.Valida() {
		indevida = append(indevida, fmt.Sprintf("transacao externa %q", d.TransacaoExterna))
	}
	if d.ChaveIdempotencia.Valida() {
		indevida = append(indevida, fmt.Sprintf("chave %q", d.ChaveIdempotencia))
	}
	if d.HashConteudo.Valida() {
		indevida = append(indevida, fmt.Sprintf("hash %q", d.HashConteudo))
	}
	if d.Rodada.Valida() {
		indevida = append(indevida, fmt.Sprintf("rodada %q", d.Rodada))
	}
	if d.Jogo.Valida() {
		indevida = append(indevida, fmt.Sprintf("jogo %q", d.Jogo))
	}
	if !d.Referencia.Vazia() {
		indevida = append(indevida, "referencia")
	}
	if len(indevida) > 0 {
		return fmt.Errorf("%w: OPENING com identidade externa: %s", ErrRegistroInvalido, strings.Join(indevida, ", "))
	}

	return nil
}

// conferirEstadoEDados garante que os dados acompanham o estado.
//
// A regra e por estado, e nao por "terminal": os tres estados terminais se
// dividem em sucesso e falha, e so os de falha carregam codigo. PROCESSED carrega
// resultado e nao carrega codigo; REJECTED e FAILED carregam codigo e nao
// carregam resultado. Tratar "terminal" como uma categoria unica faria um
// PROCESSED legitimo ser recusado por nao ter codigo de falha.
//
// O lado inverso tambem e verificado: resultado em estado nao terminal e dado
// inconsistente, e faria o replay responder com um saldo que nao corresponde a
// nenhum movimento.
func conferirEstadoEDados(d Dados) error {
	if d.AtualizadaEm.Before(d.CriadaEm) {
		return fmt.Errorf("%w: atualizado em %v antes de criado em %v", ErrEstadoInvalido, d.AtualizadaEm, d.CriadaEm)
	}

	if d.CodigoFalha.Presente() && !d.CodigoFalha.Valido() {
		return fmt.Errorf("%w: codigo %q fora do conjunto", ErrEstadoInvalido, d.CodigoFalha)
	}

	switch d.Estado {
	case EstadoProcessado:
		if d.CodigoFalha.Presente() {
			return fmt.Errorf("%w: processada com codigo de falha %q", ErrEstadoInvalido, d.CodigoFalha)
		}
		if !d.Resultado.Valida() {
			return fmt.Errorf("%w: processada sem resultado financeiro", ErrEstadoInvalido)
		}
	case EstadoRejeitado, EstadoFalhou:
		if !d.CodigoFalha.Presente() {
			return fmt.Errorf("%w: estado %q sem codigo de falha", ErrEstadoInvalido, d.Estado)
		}
		if d.Resultado.Valida() {
			return fmt.Errorf("%w: estado %q com resultado %s", ErrEstadoInvalido, d.Estado, d.Resultado)
		}
	case EstadoPendente, EstadoPendenteReferencia:
		if d.CodigoFalha.Presente() {
			return fmt.Errorf("%w: estado %q com codigo de falha %q", ErrEstadoInvalido, d.Estado, d.CodigoFalha)
		}
		if d.Resultado.Valida() {
			return fmt.Errorf("%w: estado %q com resultado %s", ErrEstadoInvalido, d.Estado, d.Resultado)
		}
		if d.Estado == EstadoPendenteReferencia && d.Referencia.Vazia() {
			return fmt.Errorf("%w: pendente por referencia sem referencia", ErrEstadoInvalido)
		}
	}
	return nil
}

// Processar leva a transacao a PROCESSED, gravando o resultado devolvido ao
// provedor.
//
// O resultado e o saldo observado no processamento original, e e ele que o
// replay devolve. Por isso ele e persistido: recalcular o saldo no replay
// devolveria o saldo de hoje, e nao o que o provedor viu.
func (t Transacao) Processar(resultado money.Money, agora time.Time) (Transacao, error) {
	if err := t.podeTransicionar(EstadoProcessado); err != nil {
		return t, err
	}
	if !resultado.Valida() {
		return t, fmt.Errorf("%w: resultado nao inicializado", ErrResultadoInvalido)
	}
	if resultado.Currency() != t.valor.Currency() {
		return t, fmt.Errorf("%w: resultado %s em moeda diferente do valor %s", ErrResultadoInvalido, resultado, t.valor)
	}

	processada := t
	processada.estado = EstadoProcessado
	processada.resultado = resultado
	processada.atualizadaEm = agora
	return processada, nil
}

// Rejeitar leva a transacao a REJECTED com o codigo da regra violada.
func (t Transacao) Rejeitar(codigo CodigoFalha, agora time.Time) (Transacao, error) {
	if err := t.podeTransicionar(EstadoRejeitado); err != nil {
		return t, err
	}
	if !codigo.Valido() || !codigo.Presente() {
		return t, fmt.Errorf("%w: %q", ErrCodigoFalhaInvalido, codigo)
	}

	rejeitada := t
	rejeitada.estado = EstadoRejeitado
	rejeitada.codigoFalha = codigo
	rejeitada.atualizadaEm = agora
	return rejeitada, nil
}

// Falhar leva a transacao a FAILED com o codigo da falha de infraestrutura.
func (t Transacao) Falhar(codigo CodigoFalha, agora time.Time) (Transacao, error) {
	if err := t.podeTransicionar(EstadoFalhou); err != nil {
		return t, err
	}
	if !codigo.Valido() || !codigo.Presente() {
		return t, fmt.Errorf("%w: %q", ErrCodigoFalhaInvalido, codigo)
	}

	falhou := t
	falhou.estado = EstadoFalhou
	falhou.codigoFalha = codigo
	falhou.atualizadaEm = agora
	return falhou, nil
}

// EsperarReferencia leva a transacao a PENDING_REFERENCE.
//
// Se a referencia externa nao foi informada, nao ha o que esperar: a transicao
// seria um no-op que esconderia entrada invalida.
func (t Transacao) EsperarReferencia(agora time.Time) (Transacao, error) {
	if err := t.podeTransicionar(EstadoPendenteReferencia); err != nil {
		return t, err
	}
	if t.referencia.Vazia() {
		return t, fmt.Errorf("%w: espera por referencia sem referencia informada", ErrRegistroInvalido)
	}

	esperando := t
	esperando.estado = EstadoPendenteReferencia
	esperando.atualizadaEm = agora
	return esperando, nil
}

// ResolverReferencia grava a transacao referenciada que foi encontrada.
//
// Ela nao muda o estado: quem decide o que acontece em seguida e o worker de
// referencias, e essa separacao permite que a referencia seja resolvida e o
// processamento retome depois de um restart.
func (t Transacao) ResolverReferencia(interna wallet.Identificador, agora time.Time) (Transacao, error) {
	if t.estado.Terminal() {
		return t, fmt.Errorf("%w: %s nao aceita transicao", ErrEstadoTerminal, t.estado)
	}
	if !interna.Valida() {
		return t, fmt.Errorf("%w: referencia interna ausente", ErrRegistroInvalido)
	}

	resolvida := t
	resolvida.referenciaInterna = interna
	resolvida.atualizadaEm = agora
	return resolvida, nil
}

// podeTransicionar centraliza a regra da maquina de estados.
func (t Transacao) podeTransicionar(destino Estado) error {
	if t.estado.Terminal() {
		return fmt.Errorf("%w: %s nao vai para %s", ErrEstadoTerminal, t.estado, destino)
	}
	if !destino.Valido() {
		return fmt.Errorf("%w: destino %q", ErrEstadoInvalido, destino)
	}
	return nil
}

// ID devolve o identificador interno.
func (t Transacao) ID() wallet.Identificador { return t.id }

// Provedor devolve o provedor de origem.
func (t Transacao) Provedor() Provedor { return t.provedor }

// TransacaoExterna devolve o identificador da operacao no provedor.
func (t Transacao) TransacaoExterna() Externo { return t.externa }

// ChaveIdempotencia devolve a chave recebida do provedor.
func (t Transacao) ChaveIdempotencia() Chave { return t.chave }

// HashConteudo devolve o resumo deterministico dos campos de negocio.
func (t Transacao) HashConteudo() Hash { return t.hash }

// Carteira devolve a carteira da operacao.
func (t Transacao) Carteira() wallet.Identificador { return t.carteira }

// Jogador devolve o jogador da operacao.
func (t Transacao) Jogador() wallet.Identificador { return t.jogador }

// Rodada devolve a rodada de jogos.
func (t Transacao) Rodada() Rodada { return t.rodada }

// Jogo devolve o jogo.
func (t Transacao) Jogo() Jogo { return t.jogo }

// Tipo devolve o tipo da operacao.
func (t Transacao) Tipo() Tipo { return t.tipo }

// Valor devolve o valor da operacao.
func (t Transacao) Valor() money.Money { return t.valor }

// Referencia devolve a referencia externa informada.
func (t Transacao) Referencia() Referencia { return t.referencia }

// ReferenciaInterna devolve a transacao referenciada resolvida.
func (t Transacao) ReferenciaInterna() wallet.Identificador { return t.referenciaInterna }

// Estado devolve o estado atual.
func (t Transacao) Estado() Estado { return t.estado }

// CodigoFalha devolve o codigo de falha, ou string vazia se nao houver.
func (t Transacao) CodigoFalha() CodigoFalha { return t.codigoFalha }

// Resultado devolve o resultado financeiro devolvido ao provedor.
//
// O valor zero nao e um resultado: e o que distingue "processada com saldo
// zero" de "ainda nao processada".
func (t Transacao) Resultado() money.Money { return t.resultado }

// CriadaEm devolve o instante de criacao.
func (t Transacao) CriadaEm() time.Time { return t.criadaEm }

// AtualizadaEm devolve o instante da ultima transicao.
func (t Transacao) AtualizadaEm() time.Time { return t.atualizadaEm }

// Valida informa se a transacao pode ser usada.
func (t Transacao) Valida() bool {
	return t.id.Valida() && t.estado.Valido() && t.tipo.Valido() && t.valor.Valida()
}
