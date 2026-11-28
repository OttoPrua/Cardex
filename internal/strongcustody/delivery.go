package strongcustody

import (
	"context"
	"path/filepath"

	"github.com/OttoPrua/Cardex/internal/model"
)

const strongDeliveryName = "input.json"

// ProveLaunch verifies the same still-open capture, exact pending attempt and
// exact privately staged payload immediately before a one-use synthetic start.
func (c *Capture) ProveLaunch(ctx context.Context, attempt model.Attempt, path string) (model.StrongCaptureClaim, error) {
	if c == nil {
		return model.StrongCaptureClaim{}, model.ErrStrongArtifact
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workspace == nil || !sameAttempt(attempt, c.reg) || !strongWorkspaceCurrent(c.workspace, c.reg.Worktree) ||
		c.verifier.CheckStrongProducer(ctx, c.reg, &c.claim) != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != strongDeliveryName {
		return model.StrongCaptureClaim{}, model.ErrStrongArtifact
	}
	body, err := ReadFakeDelivery(path, strongDeliveryName, model.StrongBundleMax)
	if err != nil || model.StrongArtifactDigest(body) != c.claim.BundleSHA {
		return model.StrongCaptureClaim{}, model.ErrStrongArtifact
	}
	return c.claim, nil
}

// ReadFakeDelivery is confined to the two synthetic delivery files. Both the
// controller and the local child read through the same no-follow boundary.
func ReadFakeDelivery(path, name string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != name ||
		(name != strongDeliveryName && name != "script.json") || limit < 1 || limit > model.StrongBundleMax {
		return nil, model.ErrStrongArtifact
	}
	parent, err := openStrongWorkspace(filepath.Dir(path))
	if err != nil {
		return nil, model.ErrStrongArtifact
	}
	defer parent.Close()
	body, err := readStrongAt(parent, name, limit)
	if err != nil || !strongWorkspaceCurrent(parent, filepath.Dir(path)) {
		return nil, model.ErrStrongArtifact
	}
	return body, nil
}
