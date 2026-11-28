//go:build !linux && !darwin

package strongcustody

import (
	"errors"
	"testing"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestStrongCustodyUnsupported(t *testing.T) {
	if Supported() {
		t.Fatal("unexpected hard filesystem custody claim")
	}
	if got, err := openStrongWorkspace(t.TempDir()); got != nil || !errors.Is(err, model.ErrStrongArtifact) {
		t.Fatalf("%v", err)
	}
}
