package board

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
)

// statusText is the Chinese label shown for a stored status. Unknown values
// are shown as stored.
var statusText = map[string]string{
	"active": "进行中", "paused": "暂停", "done_candidate": "待确认完成", "done": "已完成", "canceled": "已取消",
	"drafting": "起草中", "freezable": "可冻结", "frozen": "已冻结", "reopened": "已重开",
	"ready": "待派发", "waiting": "等待中", "running": "运行中", "harvesting": "收割中", "reviewing": "复核中",
	"merge_queued": "排队合并", "merged": "已合入", "needs_input": "待拍板", "failed": "失败", "held": "搁置",
	"ok": "正常", "degraded": "退化", "down": "不可用", "passed": "通过", "tests_failed": "测试未过",
	"no_output": "没有产出", "result_unparsable": "结果无法解析", "quota_hit": "撞到额度", "auth_failed": "认证失败",
	"transport": "连接失败", "timeout": "超时", "permission_denied": "权限被拒", "refusal": "模型拒答",
	"budget_limited": "预算用尽", "violation": "触碰底线", "launch_failed": "启动失败", "unavailable": "渠道不可用",
}

// statusTone groups a status into one of the board's colour tones.
var statusTone = map[string]string{
	"active": "live", "running": "live", "harvesting": "live", "reviewing": "live",
	"done": "ok", "merged": "ok", "ok": "ok", "passed": "ok", "frozen": "ok",
	"waiting": "warn", "needs_input": "warn", "merge_queued": "warn", "done_candidate": "warn", "freezable": "warn", "degraded": "warn", "held": "warn",
	"failed": "err", "down": "err", "tests_failed": "err",
	"no_output": "err", "result_unparsable": "err", "quota_hit": "warn", "auth_failed": "err", "transport": "err", "timeout": "err",
	"permission_denied": "err", "refusal": "err", "budget_limited": "warn", "violation": "err", "launch_failed": "err", "unavailable": "err",
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
	"inc":    func(n int) int { return n + 1 },
	"group":  groupOf,
	"sub":    func(a, b int) int { return a - b },
	"glabel": func(g string) string { return groupText[g] },
	"gtone":  func(g string) string { return groupTone[g] },
	"dec":    func(n int) int { return n - 1 },
	"pages": func(total, size int) int {
		if size <= 0 || total <= 0 {
			return 1
		}
		return (total + size - 1) / size
	},
}

// statusGroup folds the stored card statuses into the few groups a person
// cares about. Harvesting is collecting an attempt's result and running its
// acceptance, so it counts as running.
var statusGroup = map[string]string{
	"ready": "queued", "waiting": "queued",
	"running": "running", "harvesting": "running", "reviewing": "running",
	"merge_queued": "merging",
	"needs_input":  "input",
	"merged":       "done", "done": "done",
	"paused": "paused", "canceled": "paused",
	"failed": "failed",
}

var groupOrder = []string{"running", "queued", "input", "merging", "failed", "done", "paused"}

var groupText = map[string]string{
	"queued": "排队", "running": "执行中", "merging": "合并中", "input": "待你处理",
	"done": "完成", "paused": "暂停或取消", "failed": "失败",
}

var groupTone = map[string]string{
	"queued": "mute", "running": "live", "merging": "live", "input": "warn",
	"done": "ok", "paused": "mute", "failed": "err",
}

func groupOf(status string) string {
	if g, ok := statusGroup[status]; ok {
		return g
	}
	return "queued"
}

// humanQuestion explains a scheduler-generated question in plain Chinese.
// Unknown texts are returned unchanged.
func humanQuestion(text string) string {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "rework_count would exceed "):
		rest := strings.TrimPrefix(t, "rework_count would exceed ")
		n, why, _ := strings.Cut(rest, " ")
		why = strings.Trim(why, "()")
		switch why {
		case "tests failed":
			why = "测试还是没过"
		case "review":
			why = "复核还是没通过"
		}
		return fmt.Sprintf("已经返工 %s 次，%s。调度器不再自动返工，停下来等你决定。", n, why)
	case strings.HasPrefix(t, "no output after switching channel"):
		return "换了一个渠道重跑，执行器还是没有任何输出。调度器不再自动重试，停下来等你决定。"
	case strings.HasPrefix(t, "launch_failed\n"):
		step, errText := "", ""
		for _, line := range strings.Split(t, "\n")[1:] {
			if v, ok := strings.CutPrefix(line, "step: "); ok {
				step = v
			}
			if v, ok := strings.CutPrefix(line, "error: "); ok {
				errText = v
			}
		}
		return fmt.Sprintf("启动执行器失败（步骤 %s）：%s", step, errText)
	case strings.HasPrefix(t, "no other channel is available"):
		reason := strings.Trim(strings.TrimPrefix(t, "no other channel is available"), " ()")
		if reason == "" {
			return "没有别的可用渠道能接这张卡。"
		}
		return "没有别的可用渠道能接这张卡（" + reason + "）。"
	}
	return t
}
