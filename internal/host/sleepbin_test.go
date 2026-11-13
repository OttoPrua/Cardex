package host_test

import (
	"os/exec"

	"github.com/OttoPrua/Cardex/internal/proc"
)

func sleepBin() string {
	if p, err := exec.LookPath("/bin/sleep"); err == nil {
		return p
	}
	if p, err := proc.Tool("sleep"); err == nil {
		return p
	}
	return "/bin/sleep"
}
