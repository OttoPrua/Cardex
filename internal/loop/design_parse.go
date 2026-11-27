package loop

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

const (
	evAbsorbed   = "loop.absorbed"
	evUnanswered = "loop.unanswered" // one-shot terminal research backfill for an attempt
	evContinued  = "loop.continued"
	evResearch   = "loop.research"
	evFreezable  = "loop.freezable"
	evReviewPass = "loop.review_pass"
	evReviewBind = "loop.review_bind"
	evReopenUsed = "loop.reopen_used"
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
	tagRE     = regexp.MustCompile(`(?i)^\[(` + strings.Join(model.OpenQuestionTagNames(), "|") + `)\]\s*(.*)$`)
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
	if q.Type == model.OQOwner {
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
	for _, name := range model.OpenQuestionTagNames() {
		tag := "[" + name + "]"
		if strings.HasPrefix(low, tag) {
			s = strings.TrimSpace(s[len(tag):])
			break
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

func splitSections(md string) map[string]string {
	out := map[string]string{}
	for name, sections := range designLevelTwoSections(md) {
		out[name] = sections[len(sections)-1]
	}
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
	return model.CanonicalOpenQuestionType(model.OpenQuestionType(tag))
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

type freezeVerdict int

const (
	freezeNo freezeVerdict = iota
	freezeYes
	freezeUnclear
)

// judgeResultFreeze reads one dedicated control section. A conclusion is
// normally prose; only a unique, whole legacy short answer can still propose freezing.
func judgeResultFreeze(result string) freezeVerdict {
	sections := designLevelTwoSections(result)
	if explicit := sections["可冻结"]; len(explicit) != 0 {
		if len(explicit) != 1 {
			return freezeUnclear
		}
		return classifyFreeze(explicit[0])
	}
	conclusions := sections["结论"]
	for _, body := range conclusions {
		if shortFreezeAnswer(body) {
			if len(conclusions) != 1 {
				return freezeUnclear
			}
			return freezeYes
		}
	}
	return freezeNo
}

type markdownControlHeading struct {
	line  int
	level int
	title string
}

func designLevelTwoSections(md string) map[string][]string {
	sections := map[string][]string{}
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var current *markdownControlHeading
	appendSection := func(end int) {
		if current != nil {
			sections[current.title] = append(sections[current.title], strings.Join(lines[current.line+1:end], "\n"))
		}
	}
	for _, heading := range markdownControlHeadings(lines) {
		if heading.level > 2 {
			continue
		}
		appendSection(heading.line)
		current = nil
		if heading.level == 2 {
			h := heading
			current = &h
		}
	}
	appendSection(len(lines))
	return sections
}

func markdownControlHeadings(lines []string) []markdownControlHeading {
	var headings []markdownControlHeading
	var fence byte
	var fenceWidth int
	var inComment bool
	var itemIndent int
	for i, line := range lines {
		if i == 0 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		left := strings.TrimLeft(line, " ")
		indent := len(line) - len(left)
		trim := strings.TrimSpace(left)
		if inComment || (fence == 0 && strings.HasPrefix(trim, "<!--")) {
			end := strings.Index(trim, "-->")
			inComment = end < 0
			if end >= 0 && strings.TrimSpace(trim[end+3:]) != "" {
				// Do not restart recognition after mixed comment/HTML text.
				break
			}
			continue
		}
		if trim == "" {
			continue
		}
		if itemIndent != 0 && fence == 0 {
			if indent >= itemIndent {
				continue
			}
			itemIndent = 0
		}
		if indent > 3 || strings.HasPrefix(left, "\t") {
			continue
		}
		if fence == 0 {
			if strings.HasPrefix(trim, "<") {
				// Raw HTML is outside the control protocol. A partial HTML
				// parser could mistake an inner close for the block's end.
				// Do not recognize any later control heading in this artifact.
				break
			}
			if width := markdownListMarkerWidth(trim); width > 0 {
				itemIndent = indent + width
				continue
			}
		}
		if len(trim) >= 3 && (trim[0] == '`' || trim[0] == '~') {
			width := 1
			for width < len(trim) && trim[width] == trim[0] {
				width++
			}
			if width >= 3 && fence == 0 {
				fence, fenceWidth = trim[0], width
			} else if width >= fenceWidth && fence == trim[0] && strings.TrimSpace(trim[width:]) == "" {
				fence, fenceWidth = 0, 0
			}
			continue
		}
		if fence != 0 {
			continue
		}
		width := 0
		for width < len(trim) && trim[width] == '#' {
			width++
		}
		if width < 1 || width > 6 || width == len(trim) || (trim[width] != ' ' && trim[width] != '\t') {
			continue
		}
		title := strings.TrimSpace(trim[width:])
		headings = append(headings, markdownControlHeading{line: i, level: width, title: title})
	}
	return headings
}

func markdownListMarkerWidth(line string) int {
	if len(line) >= 2 && strings.ContainsRune("-+*", rune(line[0])) && (line[1] == ' ' || line[1] == '\t') {
		return 2
	}
	i := 0
	for i < len(line) && i < 9 && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(line) && (line[i] == '.' || line[i] == ')') && (line[i+1] == ' ' || line[i+1] == '\t') {
		return i + 2
	}
	return 0
}

func startsWithIndentedLiteral(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		left := strings.TrimLeft(line, " ")
		return len(line)-len(left) > 3 || strings.HasPrefix(left, "\t")
	}
	return false
}

// structuralPlanBody preserves the legacy plan heading names/levels while
// excluding code examples. Review parsing keeps its separate compatibility rules.
func structuralPlanBody(raw string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	headings := markdownControlHeadings(lines)
	for i, heading := range headings {
		if normHeading(heading.title) != normHeading("新卡") {
			continue
		}
		end := len(lines)
		if i+1 < len(headings) {
			end = headings[i+1].line
		}
		return strings.Join(lines[heading.line+1:end], "\n"), true
	}
	return "", false
}

// classifyFreeze returns freezeYes only for a short answer. Negation stays no.
// Any other ## 可冻结 body is unclear.
func classifyFreeze(body string) freezeVerdict {
	if startsWithIndentedLiteral(body) {
		return freezeUnclear
	}
	compact := compactFreeze(body)
	if currentNegative(leadingToken(body), compact) {
		return freezeNo
	}
	if shortFreezeAnswer(body) {
		return freezeYes
	}
	return freezeUnclear
}

// shortFreezeAnswer is the whole section after leading and trailing whitespace,
// one leading list marker, and trailing punctuation are removed. A second line
// is not an answer. A whitelist word plus leading separators and then exactly
// the characters 可以冻结 is also an answer. Separators inside that phrase are not.
func shortFreezeAnswer(body string) bool {
	if startsWithIndentedLiteral(body) {
		return false
	}
	s := strings.TrimSpace(body)
	s = stripOneFreezeMarker(s)
	s = strings.TrimSpace(s)
	s = strings.TrimRightFunc(s, freezeTrailingPunct)
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return false
	}
	folded := strings.ToLower(s)
	if freezeYesWord(folded) {
		return true
	}
	for _, word := range freezeYesWords {
		if !strings.HasPrefix(folded, word) {
			continue
		}
		rest := strings.TrimLeftFunc(folded[len(word):], freezeSeparator)
		if rest == "可以冻结" {
			return true
		}
	}
	return false
}

func freezeSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '，', '。', ',', '.', '！', '!', '？', '?', '、', '：', ':', '；', ';', '"', '\'', '“', '”', '‘', '’':
		return true
	default:
		return false
	}
}

var freezeYesWords = []string{"可以冻结", "可冻结", "同意", "提议", "true", "yes", "是", "1"}

func freezeYesWord(folded string) bool {
	for _, word := range freezeYesWords {
		if folded == word {
			return true
		}
	}
	return false
}

func stripOneFreezeMarker(s string) string {
	switch {
	case strings.HasPrefix(s, "1."):
		return s[2:]
	case strings.HasPrefix(s, "1、"):
		return strings.TrimPrefix(s, "1、")
	case strings.HasPrefix(s, "-"), strings.HasPrefix(s, "*"), strings.HasPrefix(s, "+"):
		return s[1:]
	default:
		return s
	}
}

func freezeTrailingPunct(r rune) bool {
	switch r {
	case '，', '。', ',', '.', '！', '!', '？', '?', '、', '：', ':', '；', ';', '"', '\'', '“', '”', '‘', '’':
		return true
	default:
		return false
	}
}

// currentNegative is a determined no. A leading 否 or 不 stays no when a later
// 可以冻结 fragment only names a future condition.
func currentNegative(token, compact string) bool {
	switch token {
	case "否", "不", "no", "false", "拒绝", "0":
		return true
	}
	if strings.HasPrefix(compact, "否") {
		return true
	}
	negAt, negLen := findNegation(compact)
	if negAt < 0 {
		return false
	}
	posAt := indexPositiveOutsideNegation(compact)
	if posAt < 0 {
		return true
	}
	return negAt < posAt && negAt+negLen <= len(compact)
}

func findNegation(compact string) (int, int) {
	bestAt, bestLen := -1, 0
	for _, phrase := range negationPhrases {
		at := strings.Index(compact, phrase)
		if at < 0 {
			continue
		}
		if bestAt < 0 || at < bestAt || (at == bestAt && len(phrase) > bestLen) {
			bestAt, bestLen = at, len(phrase)
		}
	}
	return bestAt, bestLen
}

// indexPositiveOutsideNegation only orders a negation ahead of a later fragment.
// It is not a freeze yes: classifyFreeze never affirms from this scan.
func indexPositiveOutsideNegation(compact string) int {
	best := -1
	for _, phrase := range []string{"可以冻结", "可冻结"} {
		from := 0
		for from < len(compact) {
			rel := strings.Index(compact[from:], phrase)
			if rel < 0 {
				break
			}
			at := from + rel
			if !insideNegation(compact, at) && (best < 0 || at < best) {
				best = at
			}
			from = at + len(phrase)
		}
	}
	return best
}

func insideNegation(compact string, pos int) bool {
	for _, phrase := range negationPhrases {
		from := 0
		for from < len(compact) {
			rel := strings.Index(compact[from:], phrase)
			if rel < 0 {
				break
			}
			at := from + rel
			if pos >= at && pos < at+len(phrase) {
				return true
			}
			from = at + len(phrase)
		}
	}
	return false
}

var negationPhrases = []string{"不可以冻结", "不可冻结", "不能冻结", "不予冻结", "不建议冻结", "暂不冻结"}

func leadingToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	end := strings.IndexFunc(s, freezeSep)
	if end < 0 {
		return strings.ToLower(s)
	}
	if end == 0 {
		return ""
	}
	return strings.ToLower(s[:end])
}

func freezeSep(r rune) bool {
	switch r {
	case ' ', '\n', '\t', '\r', '，', '。', ',', '.', '！', '!', '？', '?', '、', '：', ':', '；', ';', '"', '\'', '“', '”', '‘', '’':
		return true
	default:
		return false
	}
}

func compactFreeze(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\t', '\r', '，', '。', ',', '.', '！', '!', '？', '?', '、', '：', ':', '；', ';', '"', '\'':
			return -1
		default:
			return r
		}
	}, strings.ToLower(s))
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

// freezePassAllowed is the freeze-pass gate. The attempt outcome decides.
// A later passed attempt may freeze even when the card is still failed or
// canceled. An empty outcome counts only for an already done or merged card.
// Every other outcome cannot freeze the node.
func freezePassAllowed(status model.CardStatus, outcome model.Outcome) bool {
	switch outcome {
	case model.OutcomePassed:
		return true
	case "":
		return status == model.CardDone || status == model.CardMerged
	default:
		return false
	}
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
