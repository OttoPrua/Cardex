//go:build !linux && !darwin

package strongcustody

import (
	"os"

	"github.com/OttoPrua/Cardex/internal/model"
)

func openStrongWorkspace(string) (*os.File, error)         { return nil, model.ErrStrongArtifact }
func strongWorkspaceCurrent(*os.File, string) bool         { return false }
func strongSlotAbsent(*os.File, string) bool               { return false }
func readStrongAt(*os.File, string, int64) ([]byte, error) { return nil, model.ErrStrongArtifact }

// Supported reports whether local output custody primitives exist. It is not a runner sandbox capability.
func Supported() bool { return false }
