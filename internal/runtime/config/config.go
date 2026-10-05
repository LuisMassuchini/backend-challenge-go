// Package config carrega e valida a configuracao da aplicacao na borda.
//
// A configuracao e lida e validada uma unica vez, na subida, e depois e
// passada como valor. Nenhum outro ponto do sistema le variavel de ambiente:
// assim nao existe caminho em que o processo se comporte de um jeito no inicio
// e de outro no meio, e um erro de configuracao aparece no log de partida em
// vez de no meio do atendimento do primeiro trafego.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"
)

// LogLevel e o nivel de detalhe do log da aplicacao.
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// Nomes das variaveis de ambiente. Constantes, para que o teste, a
// documentacao e o .env.example nao precisem repetir a string.
const (
	ChaveHTTPAddress         = "WAGER_HTTP_ADDRESS"
	ChaveHTTPReadTimeout     = "WAGER_HTTP_READ_TIMEOUT"
	ChaveHTTPWriteTimeout    = "WAGER_HTTP_WRITE_TIMEOUT"
	ChaveHTTPIdleTimeout     = "WAGER_HTTP_IDLE_TIMEOUT"
	ChaveHTTPShutdownTimeout = "WAGER_HTTP_SHUTDOWN_TIMEOUT"
	ChaveLogLevel            = "WAGER_LOG_LEVEL"
)

// Valores padrao, aplicados quando a variavel nao vem do ambiente.
const (
	enderecoPadrao        = ":8080"
	readTimeoutPadrao     = 5 * time.Second
	writeTimeoutPadrao    = 10 * time.Second
	idleTimeoutPadrao     = 60 * time.Second
	shutdownTimeoutPadrao = 20 * time.Second
	logLevelPadrao        = LogLevelInfo
)

// HTTP e o endereco de escuta e o prazo de vida do servidor.
type HTTP struct {
	// Address e o endereco de escuta, no formato host:porta. O padrao
	// ":8080" escuta em todas as interfaces.
	Address string
	// ReadTimeout e o prazo para ler a requisicao inteira, incluindo o corpo.
	ReadTimeout time.Duration
	// WriteTimeout e o prazo para escrever a resposta.
	WriteTimeout time.Duration
	// IdleTimeout e o prazo que uma conexao ociosa e mantida entre requisicoes.
	IdleTimeout time.Duration
	// ShutdownTimeout e o prazo total do encerramento. E o que segura o
	// processo no ar enquanto o trabalho em andamento termina.
	ShutdownTimeout time.Duration
}

// Runtime e o que governa o processo, e nao uma dependencia externa.
type Runtime struct {
	LogLevel LogLevel
}

// Config e a configuracao completa da aplicacao.
//
// Os grupos de PostgreSQL, SQS e OIDC entram aqui conforme as etapas do plano
// que lhes pertencem. Cada etapa que introduz uma dependencia traz o seu grupo
// e a politica de timeout correspondente, para nao se decidir aqui o que a E7
// e a E13 precisam decidir.
type Config struct {
	Runtime Runtime
	HTTP    HTTP
	OIDC    OIDC

	// Postgres e SQS sao as dependencias externas. Postgres e obrigatorio; SQS e
	// opcional, porque o comando de migration precisa do primeiro e nao do segundo.
	Postgres Postgres
	SQS      SQS
}

// ValidationError nomeia a variavel de ambiente que precisa ser corrigida.
//
// Campo e motivo existem separados do erro de I/O porque o operador precisa
// saber qual variavel arrumar, e nao apenas que algo deu errado.
type ValidationError struct {
	// Field e o nome da variavel de ambiente.
	Field string
	// Reason explica o que fazer com o valor.
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("configuracao invalida em %s: %s", e.Field, e.Reason)
}

// Load le a configuracao do ambiente do processo.
func Load() (Config, error) {
	return FromEnv(os.LookupEnv)
}

// FromEnv le a configuracao de um getter de variavel de ambiente.
//
// O getter vem por parametro para que o teste controle o ambiente sem mexer no
// processo, e para que a funcao continue pura do ponto de vista de quem chama.
//
// O getter e no formato de os.LookupEnv, e nao o de os.Getenv, porque as duas
// coisas sao diferentes e a diferenca importa. Variavel ausente recebe o padrao;
// variavel presente e vazia e erro de configuracao. Um .env com a linha presente
// e sem valor falha na subida, em vez de servir um default silencioso que
// ninguem pediu.
//
// A validacao para no primeiro problema, em ordem fixa: endereco, prazos na
// ordem de declaracao e nivel de log. Dois ambientes com os mesmos erros
// produzem a mesma mensagem, o que permite comparar saidas e detectar
// regressao de configuracao.
func FromEnv(getenv func(string) (string, bool)) (Config, error) {
	cfg := Config{}

	endereco, err := lerEndereco(getenv)
	if err != nil {
		return Config{}, err
	}
	read, err := lerDuracao(getenv, ChaveHTTPReadTimeout, readTimeoutPadrao)
	if err != nil {
		return Config{}, err
	}
	write, err := lerDuracao(getenv, ChaveHTTPWriteTimeout, writeTimeoutPadrao)
	if err != nil {
		return Config{}, err
	}
	idle, err := lerDuracao(getenv, ChaveHTTPIdleTimeout, idleTimeoutPadrao)
	if err != nil {
		return Config{}, err
	}
	shutdown, err := lerDuracao(getenv, ChaveHTTPShutdownTimeout, shutdownTimeoutPadrao)
	if err != nil {
		return Config{}, err
	}
	nivel, err := lerLogLevel(getenv)
	if err != nil {
		return Config{}, err
	}

	cfg.HTTP = HTTP{
		Address:         endereco,
		ReadTimeout:     read,
		WriteTimeout:    write,
		IdleTimeout:     idle,
		ShutdownTimeout: shutdown,
	}
	cfg.Runtime = Runtime{LogLevel: nivel}

	oidc, err := lerOIDC(getenv)
	if err != nil {
		return Config{}, err
	}
	cfg.OIDC = oidc

	postgres, err := lerPostgres(getenv)
	if err != nil {
		return Config{}, err
	}
	cfg.Postgres = postgres

	sqs, err := lerSQS(getenv)
	if err != nil {
		return Config{}, err
	}
	cfg.SQS = sqs

	return cfg, nil
}

// OIDC e o endereco do IdP e os prazos da validacao.
type OIDC struct {
	// Issuer e o valor esperado do claim iss. Obrigatorio: sem ele um token de
	// qualquer outro IdP seria aceito, desde que a chave fosse a mesma.
	Issuer string

	// Audience e o valor esperado do claim aud. Obrigatorio pelo mesmo motivo: sem
	// ele, um token emitido para outro cliente do mesmo realm serviria aqui.
	Audience string

	// URLJWKS e onde buscar as chaves publicas. Derivada de Issuer quando vazia,
	// porque os dois endereos sao o mesmo realm visto de dois lugares.
	URLJWKS string

	// CacheJWKS e quanto tempo uma chave buscada continua valendo.
	CacheJWKS time.Duration

	// MargemDeRelogio e quanto antes do vencimento o token e recusado.
	MargemDeRelogio time.Duration

	// HTTPTimeout limita a ida ate o IdP buscar as chaves.
	HTTPTimeout time.Duration
}

// Caminho padrao do JWKS dentro do realm.
//
// Existe porque o caminho e convencao do Keycloak e nao escolha deste servico, e
// escrevelo no ambiente so seria uma chance de errar o path e receber um 404 em
// vez de um token recusado.
const caminhoPadraoJWKS = "/protocol/openid-connect/certs"

// chaves do grupo OIDC.
const (
	ChaveOIDCIssuer        = "WAGER_OIDC_ISSUER"
	ChaveOIDCAudience      = "WAGER_OIDC_AUDIENCE"
	ChaveOIDCURLJWKS       = "WAGER_OIDC_URL_JWKS"
	ChaveOIDCCacheJWKS     = "WAGER_OIDC_CACHE_JWKS"
	ChaveOIDCMargemRelogio = "WAGER_OIDC_MARGEM_RELOGIO"
	ChaveOIDCHTTPTimeout   = "WAGER_OIDC_HTTP_TIMEOUT"
)

// Prazos padrao.
//
// O cache e de cinco minutos porque e o mesmo prazo que o resto do sistema elegeu:
// janela curta o bastante para que uma chave trocada entre em vigor sem reiniciar o
// processo, e longa o bastante para que o caminho comum da requisicao nao dependa
// do IdP.
const (
	cacheJWKSPadrao       = 5 * time.Minute
	margemDeRelogioPadrao = 30 * time.Second
	httpTimeoutOIDC       = 3 * time.Second
)

// lerOIDC le o endereco do IdP.
//
// O grupo inteiro e opcional: sem nenhuma variavel de OIDC a aplicacao sobe sem
// autenticacao, que e o que permite rodar migrations e testes de dominio sem um IdP
// no ar. Mas um grupo pela metade e erro de configuracao, e nao um grupo ausente: quem
// configura Issuer sem Audience precisa saber disso no start, e nao descobrir na
// primeira requisicao que um token de qualquer cliente do realm esta sendo aceito.
func lerOIDC(getenv func(string) (string, bool)) (OIDC, error) {
	issuer, definido := getenv(ChaveOIDCIssuer)
	issuer = strings.TrimSuffix(strings.TrimSpace(issuer), "/")

	if issuer == "" {
		if definido {
			return OIDC{}, &ValidationError{
				Field:  ChaveOIDCIssuer,
				Reason: "a variavel esta presente e vazia; remova a linha ou informe o realm, como \"http://localhost:8081/realms/wager\"",
			}
		}

		// Sem Issuer, nenhuma outra variavel do grupo faz sentido. Um JWKS sem emissor
		// seria um servico que aceita qualquer token assinado pela chave certa, que e
		// o pior dos dois jeitos de errar.
		for _, chave := range []string{
			ChaveOIDCAudience, ChaveOIDCURLJWKS,
			ChaveOIDCCacheJWKS, ChaveOIDCMargemRelogio, ChaveOIDCHTTPTimeout,
		} {
			if _, presente := getenv(chave); presente {
				return OIDC{}, &ValidationError{
					Field:  chave,
					Reason: fmt.Sprintf("so faz sentido com %s, que nao foi informada", ChaveOIDCIssuer),
				}
			}
		}
		return OIDC{}, nil
	}

	audience, err := lerTextoObrigatorio(getenv, ChaveOIDCAudience)
	if err != nil {
		return OIDC{}, err
	}

	urlJWKS := issuer + caminhoPadraoJWKS
	if bruta, presente := getenv(ChaveOIDCURLJWKS); presente {
		if strings.TrimSpace(bruta) == "" {
			return OIDC{}, &ValidationError{
				Field:  ChaveOIDCURLJWKS,
				Reason: "a variavel esta presente e vazia; remova a linha para derivar do issuer, ou informe a URL completa",
			}
		}
		urlJWKS = strings.TrimSpace(bruta)
	}

	cache, err := lerDuracao(getenv, ChaveOIDCCacheJWKS, cacheJWKSPadrao)
	if err != nil {
		return OIDC{}, err
	}
	margem, err := lerDuracao(getenv, ChaveOIDCMargemRelogio, margemDeRelogioPadrao)
	if err != nil {
		return OIDC{}, err
	}
	timeout, err := lerDuracao(getenv, ChaveOIDCHTTPTimeout, httpTimeoutOIDC)
	if err != nil {
		return OIDC{}, err
	}

	return OIDC{
		Issuer:          issuer,
		Audience:        audience,
		URLJWKS:         urlJWKS,
		CacheJWKS:       cache,
		MargemDeRelogio: margem,
		HTTPTimeout:     timeout,
	}, nil
}

// lerTextoObrigatorio le um texto que nao pode faltar.
func lerTextoObrigatorio(getenv func(string) (string, bool), chave string) (string, error) {
	bruto, definido := getenv(chave)
	bruto = strings.TrimSpace(bruto)

	if bruto == "" {
		motivo := "a variavel e obrigatoria"
		if definido {
			motivo = "a variavel esta presente e vazia"
		}
		return "", &ValidationError{Field: chave, Reason: motivo}
	}
	return bruto, nil
}

func lerEndereco(getenv func(string) (string, bool)) (string, error) {
	bruto, definido := getenv(ChaveHTTPAddress)
	if !definido {
		return enderecoPadrao, nil
	}
	if bruto == "" {
		return "", &ValidationError{
			Field:  ChaveHTTPAddress,
			Reason: "a variavel esta presente e vazia; remova a linha ou informe host:porta",
		}
	}
	if _, _, err := net.SplitHostPort(bruto); err != nil {
		return "", &ValidationError{
			Field:  ChaveHTTPAddress,
			Reason: fmt.Sprintf("esperado host:porta, como %q, e nao %q", enderecoPadrao, bruto),
		}
	}
	return bruto, nil
}

func lerDuracao(getenv func(string) (string, bool), chave string, padrao time.Duration) (time.Duration, error) {
	bruto, definido := getenv(chave)
	if !definido {
		return padrao, nil
	}
	if bruto == "" {
		return 0, &ValidationError{
			Field:  chave,
			Reason: fmt.Sprintf("a variavel esta presente e vazia; remova a linha ou informe uma duracao, como %q", padrao),
		}
	}

	d, err := time.ParseDuration(bruto)
	if err != nil {
		return 0, &ValidationError{
			Field:  chave,
			Reason: fmt.Sprintf("duracao invalida %q: use o formato de Go, como %q", bruto, padrao),
		}
	}
	if d <= 0 {
		return 0, &ValidationError{
			Field:  chave,
			Reason: fmt.Sprintf("duracao precisa ser maior que zero, e %q foi informado", bruto),
		}
	}
	return d, nil
}

// Slog converte o nivel da configuracao para o nivel do log estruturado.
//
// A traducao mora aqui porque e o unico lugar que conhece os dois conjuntos de nomes:
// o do arquivo de configuracao, que a operacao edita, e o da biblioteca de log, que a
// biblioteca conhece. Sem ela, cada consumidor reconversao, e a reconversao e onde
// os nomes divergem.
func (l LogLevel) Slog() slog.Level {
	switch l {
	case LogLevelDebug:
		return slog.LevelDebug
	case LogLevelWarn:
		return slog.LevelWarn
	case LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
func lerLogLevel(getenv func(string) (string, bool)) (LogLevel, error) {
	bruto, definido := getenv(ChaveLogLevel)
	if !definido {
		return logLevelPadrao, nil
	}
	if bruto == "" {
		return "", &ValidationError{
			Field:  ChaveLogLevel,
			Reason: fmt.Sprintf("a variavel esta presente e vazia; remova a linha ou informe um nivel, como %q", logLevelPadrao),
		}
	}

	nivel := LogLevel(bruto)
	switch nivel {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
		return nivel, nil
	}

	validos := []LogLevel{LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError}
	return "", &ValidationError{
		Field:  ChaveLogLevel,
		Reason: fmt.Sprintf("nivel desconhecido %q; use um de %v", bruto, validos),
	}
}
