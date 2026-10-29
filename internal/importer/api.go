// Package importer reads a v1 home and maps it into v2 rows. Design §14.
package importer

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned until R4-B fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Report counts v1 statuses and the v2 rows they became.
type Report struct {
	DryRun   bool           // 为真时没有写入。
	Counts   map[string]int // 按 v1 状态和 v2 落位计数。
	Unparsed []string       // 无法解析的条目及原因。
}

// Import reads v1Home. It does not write that directory. dryRun only builds a report.
func Import(ctx context.Context, v1Home string, dryRun bool) (Report, error) {
	return Report{}, ErrNotImplemented
}
