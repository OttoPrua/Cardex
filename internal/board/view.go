package board

import (
	"html/template"
	"strconv"
)

// statusText is the Chinese label shown for a stored status. Unknown values
// are shown as stored.
var statusText = map[string]string{
	"active": "进行中", "paused": "暂停", "done_candidate": "待确认完成", "done": "已完成", "canceled": "已取消",
	"drafting": "起草中", "freezable": "可冻结", "frozen": "已冻结", "reopened": "已重开",
	"ready": "待派发", "waiting": "等待中", "running": "运行中", "harvesting": "收割中", "reviewing": "复核中",
	"merge_queued": "排队合并", "merged": "已合入", "needs_input": "待拍板", "failed": "失败", "held": "搁置",
	"ok": "正常", "degraded": "退化", "down": "不可用", "passed": "通过", "tests_failed": "测试未过",
}

// statusTone groups a status into one of the board's colour tones.
var statusTone = map[string]string{
	"active": "live", "running": "live", "harvesting": "live", "reviewing": "live",
	"done": "ok", "merged": "ok", "ok": "ok", "passed": "ok", "frozen": "ok",
	"waiting": "warn", "needs_input": "warn", "merge_queued": "warn", "done_candidate": "warn", "freezable": "warn", "degraded": "warn", "held": "warn",
	"failed": "err", "down": "err", "tests_failed": "err",
}

var boardFuncs = template.FuncMap{
	"label": func(s string) string {
		if v, ok := statusText[s]; ok {
			return v
		}
		return s
	},
	"tone": func(s string) string {
		if v, ok := statusTone[s]; ok {
			return v
		}
		return "mute"
	},
	// pct turns a 0..1 fraction into a CSS width; nil (unknown) is 0.
	"pct": func(v *float64) string {
		if v == nil {
			return "0%"
		}
		f := *v * 100
		if f < 0 {
			f = 0
		}
		if f > 100 {
			f = 100
		}
		return strconv.FormatFloat(f, 'f', 0, 64) + "%"
	},
	// load is occupied/concurrency as a CSS width.
	"load": func(occupied, concurrency int) string {
		if concurrency <= 0 {
			return "0%"
		}
		f := float64(occupied) / float64(concurrency) * 100
		if f > 100 {
			f = 100
		}
		return strconv.FormatFloat(f, 'f', 0, 64) + "%"
	},
	"short": func(s string) string {
		if len(s) > 10 {
			return s[:10]
		}
		return s
	},
	"inc": func(n int) int { return n + 1 },
	"dec": func(n int) int { return n - 1 },
	"pages": func(total, size int) int {
		if size <= 0 || total <= 0 {
			return 1
		}
		return (total + size - 1) / size
	},
}
