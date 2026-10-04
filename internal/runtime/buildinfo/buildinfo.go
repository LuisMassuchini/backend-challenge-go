// Package buildinfo identifica o binario em execucao: de qual modulo ele veio,
// em que versao e com qual toolchain foi compilado.
//
// Existe para que o log de partida, o health check e a resposta de versao ja
// nascam com essa informacao, sem depender de alguem se lembrar de buscar depois
// de um incidente. Um servico que nao diz qual versao esta no ar transforma
// qualquer incidente em investigacao manual.
package buildinfo

import "runtime"

// Module e o caminho do modulo deste projeto. Precisa ficar em sincronia com o
// go.mod, e ha teste que falha quando os dois divergem.
const Module = "github.com/LuisMassuchini/backend-challenge-go"

// DevelopmentVersion e a versao usada quando o binario foi compilado sem
// -ldflags. Preferivel a string vazia: um binario de desenvolvimento nao pode
// se passar por release.
const DevelopmentVersion = "dev"

// version e sobrescrita em build por -ldflags. Ver o alvo de build da E19.
var version = DevelopmentVersion

// Info identifica o binario em execucao.
type Info struct {
	// Module e o caminho do modulo de origem.
	Module string
	// Version e a versao de release, ou DevelopmentVersion.
	Version string
	// GoVersion e a versao do toolchain que compilou o binario.
	GoVersion string
}

// Current reune a identificacao do binario em execucao.
func Current() Info {
	return Info{
		Module:    Module,
		Version:   version,
		GoVersion: runtime.Version(),
	}
}
