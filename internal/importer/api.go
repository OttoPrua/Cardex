// Package importer reads a v1 home and maps it into v2 rows. Design §14.
package importer

import "context"

// Report counts v1 statuses and the v2 rows they became.
// Count keys are "v1:<status>" and "v2:<placement>" (ready, paused, done, failed, canceled, history).
type Report struct {
	DryRun   bool           // 为真时没有写入 v2 行。
	Counts   map[string]int // 按 v1 状态和 v2 落位分别计数。
	Unparsed []string       // 无法解析或无法落位的条目及原因。
}

// Import reads v1Home and, unless dryRun, writes into the v2 home.
// It does not write any file under v1Home.
func Import(ctx context.Context, v2Home, v1Home string, dryRun bool) (Report, error) {
	return importHome(ctx, v2Home, v1Home, dryRun)
}
