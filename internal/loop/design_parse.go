package loop

import (
	"regexp"
	"strconv"
	"strings"

	"cardex/internal/model"
)

const (
	evAbsorbed   = "loop.absorbed"
	evResearch   = "loop.research"
	evFreezable  = "loop.freezable"
	evReviewPass = "loop.review_pass"
)

type freezeKind int

const (
	freezeAsk freezeKind = iota
	freezeNow
	freezeReview
)

type parsedLine struct {
	kind   string // add, drop, recheck
	id     string
	reason string
	text   string
	qtype  model.OpenQuestionType
}

type childSpec struct {
	layer model.Layer
	title string
}

type factBlock struct {
	present    bool
	answer     string
	evidence   string
	confidence string
}

var (
	recheckRE = regexp.MustCompile(`(?i)^\[fact(?:\s+web)?\]\s*Q([A-Za-z0-9_-]+)\s*复查[:：]\s*(.*)$`)
	dropRE    = regexp.MustCompile(`(?i)^(?:删除|关闭|drop)\s+Q([A-Za-z0-9_-]+)$`)
	tagRE     = regexp.MustCompile(`(?i)^\[(fact web|fact|experiment|decision|otto)\]\s*(.*)$`)
	childRE   = regexp.MustCompile(`(?i)^(?:\[(module|detail)\]|(module|detail)\s*[:：])\s*(.+)$`)
	qHeadRE   = regexp.MustCompile(`(?i)^###\s*Q([A-Za-z0-9_-]+)\s*$`)
	pathRE    = regexp.MustCompile(`(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+|[A-Za-z0-9_.-]+\.[A-Za-z0-9]+`)
	qBodyRE   = regexp.MustCompile(`(?m)^Q([A-Za-z0-9_-]+)\s*:`)
)

func decideFreeze(layer model.Layer, mode model.FreezeMode) freezeKind {
	switch layer {
	case model.LayerDirection:
		return freezeAsk
	case model.LayerModule:
		if mode == model.FreezeFast {
			return freezeNow
		}
		return freezeAsk
	case model.LayerDetail:
		if mode == model.FreezeFast {
			return freezeNow
		}
		return freezeReview
	default:
		return freezeAsk
	}
}

func blocksFreezable(q model.OpenQuestion) bool {
	if q.Type == model.OQOtto {
		return false
	}
	return q.Status == model.OQOpen || q.Status == model.OQResearching
}

func nodeBlocked(n model.DesignNode) bool {
	for _, q := range n.OpenQuestions {
		if blocksFreezable(q) {
			return true
		}
	}
	return false
}

func bumpTier(t model.Tier) model.Tier {
	switch t {
	case model.TierNone, model.TierT1:
		return model.TierT2
	case model.TierT2:
		return model.TierT3
	default:
		return model.TierT3
	}
}

func researchTier(q model.OpenQuestion) model.Tier {
	t := model.TierT1
	n := q.Asks
	if n < 0 {
		n = 0
	}
	for i := 0; i < n; i++ {
		t = bumpTier(t)
	}
	return t
}

func isWebText(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "[fact web]")
}

func dedupKey(text string) string {
	s := strings.TrimSpace(text)
	low := strings.ToLower(s)
	for _, tag := range []string{"[fact web]", "[fact]", "[experiment]", "[decision]", "[otto]"} {
		if strings.HasPrefix(low, tag) {
			s = strings.TrimSpace(s[len(tag):])
			break
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

func splitSections(md string) map[string]string {
	out := map[string]string{}
	var cur string
	var buf strings.Builder
	flush := func() {
		if cur == "" {
			return
		}
		out[cur] = buf.String()
		buf.Reset()
	}
	for _, line := range strings.Split(md, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "##") && !strings.HasPrefix(trim, "###") {
			flush()
			h := strings.TrimSpace(strings.TrimLeft(trim, "#"))
			cur = h
			continue
		}
		if cur != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	flush()
	return out
}

func cleanBullet(line string) string {
	line = strings.TrimSpace(line)
	for _, p := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, p) {
			line = strings.TrimSpace(line[len(p):])
			break
		}
	}
	if i := strings.IndexByte(line, '.'); i > 0 && i <= 2 {
		if _, err := strconv.Atoi(line[:i]); err == nil {
			if rest := strings.TrimSpace(line[i+1:]); rest != "" {
				line = rest
			}
		}
	}
	return strings.TrimSpace(line)
}

func parseQuestionLines(body string) []parsedLine {
	var out []parsedLine
	for _, raw := range strings.Split(body, "\n") {
		line := cleanBullet(raw)
		if line == "" {
			continue
		}
		if m := recheckRE.FindStringSubmatch(line); m != nil {
			out = append(out, parsedLine{kind: "recheck", id: "Q" + m[1], reason: strings.TrimSpace(m[2])})
			continue
		}
		if m := dropRE.FindStringSubmatch(line); m != nil {
			out = append(out, parsedLine{kind: "drop", id: "Q" + m[1]})
			continue
		}
		if m := tagRE.FindStringSubmatch(line); m != nil {
			out = append(out, parsedLine{kind: "add", text: line, qtype: tagType(m[1])})
			continue
		}
		out = append(out, parsedLine{kind: "add", text: line, qtype: model.OQDecision})
	}
	return out
}

func tagType(tag string) model.OpenQuestionType {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "fact", "fact web":
		return model.OQFact
	case "experiment":
		return model.OQExperiment
	case "otto":
		return model.OQOtto
	default:
		return model.OQDecision
	}
}

func parseChildLines(body string) []childSpec {
	var out []childSpec
	for _, raw := range strings.Split(body, "\n") {
		line := cleanBullet(raw)
		if line == "" {
			continue
		}
		m := childRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if name == "" {
			name = m[2]
		}
		title := strings.TrimSpace(m[3])
		if title == "" {
			continue
		}
		layer := model.LayerModule
		if strings.EqualFold(name, "detail") {
			layer = model.LayerDetail
		}
		out = append(out, childSpec{layer: layer, title: title})
	}
	return out
}

func parseFreezable(body string) bool {
	s := strings.TrimSpace(body)
	if s == "" {
		return false
	}
	first := strings.ToLower(strings.Fields(s)[0])
	switch first {
	case "是", "yes", "true", "可冻结", "提议", "同意", "1":
		return true
	default:
		return false
	}
}

func parseFactSection(body string) map[string]factBlock {
	out := map[string]factBlock{}
	var cur string
	var buf []string
	flush := func() {
		if cur == "" {
			return
		}
		out[cur] = parseFactBuf(buf)
		buf = nil
	}
	for _, line := range strings.Split(body, "\n") {
		if m := qHeadRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			flush()
			cur = "Q" + m[1]
			continue
		}
		if cur != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return out
}

func parseFactBuf(lines []string) factBlock {
	b := factBlock{present: true}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if v, ok := consumeLabel(trim, "结论"); ok {
			b.answer = joinLine(b.answer, v)
			continue
		}
		if v, ok := consumeLabel(trim, "证据"); ok {
			b.evidence = joinLine(b.evidence, v)
			continue
		}
		if v, ok := consumeLabel(trim, "把握"); ok {
			b.confidence = v
			continue
		}
		b.answer = joinLine(b.answer, trim)
	}
	return b
}

func consumeLabel(line, label string) (string, bool) {
	for _, sep := range []string{"：", ":"} {
		p := label + sep
		if strings.HasPrefix(line, p) {
			return strings.TrimSpace(strings.TrimPrefix(line, p)), true
		}
	}
	return "", false
}

func joinLine(base, add string) string {
	add = strings.TrimSpace(add)
	if add == "" {
		return base
	}
	if base == "" {
		return add
	}
	return base + "\n" + add
}

func extractPaths(evidence string) []string {
	if strings.TrimSpace(evidence) == "" {
		return []string{}
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range pathRE.FindAllString(evidence, -1) {
		if strings.Contains(m, "://") {
			continue
		}
		p := strings.TrimPrefix(filepathSlash(m), "./")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if out == nil {
		return []string{}
	}
	return out
}

func filepathSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

func applyBlock(q *model.OpenQuestion, b factBlock, commit string) {
	q.Commit = commit
	if q.CitedPaths == nil {
		q.CitedPaths = []string{}
	}
	if !b.present || strings.TrimSpace(b.answer) == "" {
		q.Status = model.OQUnanswered
		return
	}
	q.Answer = strings.TrimSpace(b.answer)
	q.Evidence = strings.TrimSpace(b.evidence)
	q.CitedPaths = extractPaths(q.Evidence)
	if strings.TrimSpace(q.Evidence) == "" {
		q.Confidence = "低"
	} else if c := strings.TrimSpace(b.confidence); c != "" {
		q.Confidence = c
	} else {
		q.Confidence = "中"
	}
	q.Status = model.OQAnswered
}

func nextQID(qs []model.OpenQuestion) string {
	max := 0
	for _, q := range qs {
		if strings.HasPrefix(q.ID, "Q") {
			n, err := strconv.Atoi(q.ID[1:])
			if err == nil && n > max {
				max = n
			}
		}
	}
	return "Q" + strconv.Itoa(max+1)
}

func addOpenQuestion(n *model.DesignNode, text string, typ model.OpenQuestionType) (model.OpenQuestion, bool) {
	key := dedupKey(text)
	if key == "" {
		return model.OpenQuestion{}, false
	}
	for _, q := range n.OpenQuestions {
		if dedupKey(q.Text) == key {
			return q, false
		}
	}
	q := model.OpenQuestion{
		ID:         nextQID(n.OpenQuestions),
		Text:       strings.TrimSpace(text),
		Type:       typ,
		Status:     model.OQOpen,
		CitedPaths: []string{},
	}
	n.OpenQuestions = append(n.OpenQuestions, q)
	return q, true
}

func dropOpenQuestion(n *model.DesignNode, id string) {
	out := make([]model.OpenQuestion, 0, len(n.OpenQuestions))
	for _, q := range n.OpenQuestions {
		if q.ID == id {
			continue
		}
		out = append(out, q)
	}
	n.OpenQuestions = out
}

func applyRecheck(n *model.DesignNode, id string) bool {
	for i, q := range n.OpenQuestions {
		if q.ID != id {
			continue
		}
		q.Asks++
		q.Status = model.OQOpen
		if q.Asks >= 2 {
			q.Type = model.OQDecision
		} else if q.Type != model.OQFact {
			q.Type = model.OQFact
		}
		n.OpenQuestions[i] = q
		return q.Type == model.OQFact
	}
	return false
}

type factGroup struct {
	items []model.OpenQuestion
	web   bool
	tier  model.Tier
}

func groupFacts(qs []model.OpenQuestion) []factGroup {
	type key struct {
		web  bool
		tier model.Tier
	}
	order := []key{}
	buckets := map[key][]model.OpenQuestion{}
	for _, q := range qs {
		if q.Type != model.OQFact || q.Status != model.OQOpen {
			continue
		}
		k := key{web: isWebText(q.Text), tier: researchTier(q)}
		if _, ok := buckets[k]; !ok {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], q)
	}
	var groups []factGroup
	for _, k := range order {
		items := buckets[k]
		for i := 0; i < len(items); i += 5 {
			j := i + 5
			if j > len(items) {
				j = len(items)
			}
			groups = append(groups, factGroup{items: append([]model.OpenQuestion(nil), items[i:j]...), web: k.web, tier: k.tier})
		}
	}
	return groups
}

func terminalCard(st model.CardStatus) bool {
	switch st {
	case model.CardDone, model.CardMerged, model.CardCanceled, model.CardFailed:
		return true
	default:
		return false
	}
}

func notStartedCard(st model.CardStatus) bool {
	switch st {
	case model.CardReady, model.CardWaiting, model.CardNeedsInput:
		return true
	default:
		return false
	}
}

func consented(q model.Question) bool {
	if strings.TrimSpace(q.Answer) == "" || len(q.Options) == 0 {
		return false
	}
	return q.Answer == q.Options[0]
}

func markSummary(q model.OpenQuestion) model.OpenQuestion {
	cp := q
	cp.CitedPaths = append([]string(nil), q.CitedPaths...)
	switch q.Status {
	case model.OQUnanswered:
		cp.Answer = joinLine(cp.Answer, "未能核实")
	case model.OQStale:
		cp.Answer = joinLine(cp.Answer, "可能过期")
	}
	return cp
}

func passedReview(section string) bool {
	s := strings.TrimSpace(section)
	if s == "" {
		return false
	}
	first := strings.Fields(s)[0]
	return strings.EqualFold(first, "pass") || first == "通过"
}

func questionIDsInBody(body string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, m := range qBodyRE.FindAllStringSubmatch(body, -1) {
		id := "Q" + m[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func researchBody(items []model.OpenQuestion) string {
	var b strings.Builder
	for _, q := range items {
		b.WriteString(q.ID)
		b.WriteString(": ")
		b.WriteString(q.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
