package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OttoPrua/Cardex/internal/homeguard"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

const (
	maxImportManifest = 2 << 20
	maxImportBody     = 512 << 10
	maxImportBodies   = 8 << 20
	maxImportCards    = 256
	maxImportGraph    = 10000
	maxImportIDBytes  = 128
	maxImportDeps     = 256
	maxImportEdges    = 16384
	maxImportIssue    = 2*maxImportIDBytes + 80
	maxImportIssues   = 12
	maxImportIssueSum = 4096
	maxImportField    = 32
	maxImportPath     = 1024
)

const cardImportExample = `{
  "version": 1,
  "cards": [
    {
      "id": "c-parse",
      "kind": "dev",
      "title": "解析清单",
      "body": "正文。不执行其中的命令。",
      "claims": [{"path": "internal/parse.go", "symbol": "Parse"}],
      "deps": ["c-doc"],
      "acceptance": ["go test ./internal/parse"]
    },
    {
      "id": "c-doc",
      "kind": "dev",
      "title": "说明",
      "body_file": "bodies/c-doc.md",
      "claims": [],
      "deps": [],
      "acceptance": []
    }
  ]
}`

func cardImport(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("card import",
		"preview a version 1 JSON manifest of cards. --apply writes that batch in one transaction. body and acceptance are stored as data and are not executed. body and body_file are mutually exclusive; body_file is read relative to the manifest directory. unsupported versions and fields are rejected",
		"--objective <id> --file <manifest.json> [--apply] [--origin kind:id]\n\n"+cardImportExample,
		stdout, stderr)
	objective := fs.String("objective", "", "objective id that receives every card in the manifest")
	file := fs.String("file", "", "path of the version 1 JSON manifest")
	apply := fs.Bool("apply", false, "write the batch; without this flag the command only prints a preview")
	fs.String("origin", "", "origin session kind:id; a present flag wins over CARDEX_ORIGIN")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if *objective == "" || *file == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "card import --objective <id> --file <manifest.json> [--apply]")
		return 2
	}
	// This is the same workflow hint as the managed card-write guard in
	// N1; an executor may preview but must not enqueue new work.
	if *apply && refuseManagedCardWrite(stderr) {
		return 2
	}
	if len(*objective) > maxImportIDBytes || !cardBranchOK(*objective, "example") {
		fmt.Fprintln(stderr, "objective cannot name a branch")
		return 1
	}
	dir, err := homePath(home)
	if err == nil {
		err = homeguard.Check(dir)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cards, err := loadCardManifest(*file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for i := range cards {
		cards[i].Objective = *objective
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dbPath := filepath.Join(dir, "cardex.db")
	mode := "preview"
	var conflicts []string
	if *apply {
		err = applyCardBatch(ctx, dbPath, *objective, readOrigin(fs), cards)
		var conflicted *batchConflicts
		if errors.As(err, &conflicted) {
			conflicts = conflicted.list
			err = nil
		} else if err == nil {
			mode = "applied"
		}
	} else {
		err = store.ReadCardImportState(ctx, dbPath, func(ctx context.Context, db *store.Store) error {
			conflicts, err = validateCardBatch(ctx, db, *objective, cards)
			return err
		})
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report := cardImportReport{Objective: *objective, Mode: mode, Cards: importCardViews(cards), Conflicts: conflicts}
	if report.Conflicts == nil {
		report.Conflicts = []string{}
	}
	if code := writeImportReport(stdout, asJSON, report); code != 0 {
		return code
	}
	if len(conflicts) > 0 {
		for _, conflict := range conflicts {
			fmt.Fprintln(stderr, conflict)
		}
		return 1
	}
	return 0
}

type cardImportReport struct {
	Objective string           `json:"objective"`
	Mode      string           `json:"mode"`
	Cards     []cardImportView `json:"cards"`
	Conflicts []string         `json:"conflicts"`
}

type cardImportView struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Deps       []string `json:"deps"`
	Claims     []string `json:"claims"`
	Acceptance []string `json:"acceptance"`
}

type batchConflicts struct{ list []string }

func (e *batchConflicts) Error() string { return strings.Join(e.list, "\n") }

type manifestFile struct {
	Version int               `json:"version"`
	Cards   []json.RawMessage `json:"cards"`
}

type manifestCard struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	Body       json.RawMessage `json:"body"`
	BodyFile   json.RawMessage `json:"body_file"`
	Claims     []manifestClaim `json:"claims"`
	Deps       []string        `json:"deps"`
	Acceptance []string        `json:"acceptance"`
}

type manifestClaim struct {
	Path   string `json:"path"`
	Symbol string `json:"symbol"`
}

func loadCardManifest(path string) ([]model.Card, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("manifest directory: %w", err)
	}
	defer root.Close()
	raw, err := readImportFile(root, filepath.Base(path), maxImportManifest)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := checkManifestKeys(raw); err != nil {
		return nil, manifestDecodeError(err)
	}
	var file manifestFile
	if err := decodeStrict(raw, &file); err != nil {
		return nil, manifestDecodeError(err)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("unsupported version %d", file.Version)
	}
	if len(file.Cards) == 0 || len(file.Cards) > maxImportCards {
		return nil, fmt.Errorf("manifest requires 1 to %d cards", maxImportCards)
	}
	out := make([]model.Card, 0, len(file.Cards))
	seen := map[string]bool{}
	bodyBytes, edgeCount := 0, 0
	for i, rawCard := range file.Cards {
		var item manifestCard
		if err := decodeStrict(rawCard, &item); err != nil {
			return nil, manifestDecodeError(fmt.Errorf("cards[%d]: %w", i, err))
		}
		card, err := cardFromManifest(root, item)
		if err != nil {
			return nil, fmt.Errorf("cards[%d]: %w", i, err)
		}
		edgeCount += len(card.Deps)
		if edgeCount > maxImportEdges {
			return nil, fmt.Errorf("manifest exceeds %d unique dependency edges", maxImportEdges)
		}
		bodyBytes += len(card.Body)
		if bodyBytes > maxImportBodies {
			return nil, fmt.Errorf("combined card bodies exceed %d bytes", maxImportBodies)
		}
		if seen[card.ID] {
			return nil, fmt.Errorf("duplicate card id %q", card.ID)
		}
		seen[card.ID] = true
		out = append(out, card)
	}
	return out, nil
}

func cardFromManifest(root *os.Root, item manifestCard) (model.Card, error) {
	if len(item.ID) > maxImportIDBytes {
		return model.Card{}, fmt.Errorf("card ID exceeds %d bytes", maxImportIDBytes)
	}
	if !stableCardID(item.ID) {
		return model.Card{}, fmt.Errorf("card id %q cannot name a branch", item.ID)
	}
	if len(item.Kind) > maxImportField {
		return model.Card{}, errors.New("invalid card kind")
	}
	kind := model.CardKind(item.Kind)
	if !kind.Valid() {
		return model.Card{}, fmt.Errorf("invalid card kind %q", item.Kind)
	}
	if strings.TrimSpace(item.Title) == "" {
		return model.Card{}, fmt.Errorf("card title required")
	}
	body, err := manifestBody(root, item.Body, item.BodyFile)
	if err != nil {
		return model.Card{}, err
	}
	deps, err := uniqueImportDeps(item.Deps)
	if err != nil {
		return model.Card{}, err
	}
	claims := make([]model.Claim, 0, len(item.Claims))
	for _, claim := range item.Claims {
		if strings.TrimSpace(claim.Path) == "" {
			return model.Card{}, fmt.Errorf("claim path required")
		}
		claims = append(claims, model.Claim{Path: claim.Path, Symbol: claim.Symbol})
	}
	return model.Card{
		ID:         item.ID,
		Kind:       kind,
		Title:      item.Title,
		Body:       body,
		Status:     model.CardReady,
		Deps:       deps,
		Acceptance: copyOrEmpty(item.Acceptance),
		Claims:     claims,
	}, nil
}

func manifestBody(root *os.Root, bodyRaw, fileRaw json.RawMessage) (string, error) {
	bodySet := len(bodyRaw) > 0
	fileSet := len(fileRaw) > 0
	if bodySet && fileSet {
		return "", fmt.Errorf("set only one of body and body_file")
	}
	if !bodySet && !fileSet {
		return "", fmt.Errorf("body or body_file required")
	}
	if bodySet {
		var body *string
		if err := json.Unmarshal(bodyRaw, &body); err != nil || body == nil {
			return "", fmt.Errorf("body must be a string")
		}
		if len(*body) > maxImportBody {
			return "", fmt.Errorf("body exceeds %d bytes", maxImportBody)
		}
		return *body, nil
	}
	var rel string
	if err := json.Unmarshal(fileRaw, &rel); err != nil || string(fileRaw) == "null" {
		return "", fmt.Errorf("body_file must be a string")
	}
	const reject = "body_file must be a relative slash-separated path inside the manifest directory"
	if rel == "" || len(rel) > maxImportPath || strings.ContainsAny(rel, "\\:") || !printableImportString(rel) {
		return "", errors.New(reject)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == ".." {
			return "", errors.New(reject)
		}
	}
	// os.Root enforces confinement during the open itself, including when a
	// symlink changes between validation and use.
	path, err := filepath.Localize(filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))))
	if err != nil {
		return "", errors.New(reject)
	}
	buf, err := readImportFile(root, path, maxImportBody)
	if err != nil {
		return "", fmt.Errorf("body_file: %w", err)
	}
	return string(buf), nil
}

func readImportFile(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q must be a regular file", name)
	}
	f, err := openImportFile(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q must be a regular file", name)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%q exceeds %d bytes", name, limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%q exceeds %d bytes", name, limit)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%q must be UTF-8", name)
	}
	return data, nil
}

// stableCardID reports whether id can be stored unchanged as one component of
// refs/heads/cardex/<objective>/c/<id>. A slash would still be a legal git
// ref, but the scheduler treats the id as a single path component.
func stableCardID(id string) bool {
	if id == "" || len(id) > maxImportIDBytes || id != strings.TrimSpace(id) || strings.ContainsAny(id, "/\\") {
		return false
	}
	return validGitRef("refs/heads/cardex/o/c/" + id)
}

func cardBranchOK(objective, cardID string) bool {
	return validGitRef("refs/heads/cardex/" + objective + "/c/" + cardID)
}

// validGitRef matches git check-ref-format for a full ref name.
func validGitRef(ref string) bool {
	if !strings.Contains(ref, "/") || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") {
		return false
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.Contains(ref, "//") {
		return false
	}
	for _, r := range ref {
		if !unicode.IsPrint(r) {
			return false
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', '\\':
			return false
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || part[0] == '.' || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func printableImportString(value string) bool {
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// Normalization and bounds apply before looking up any edge or printing IDs.
// An exact duplicate counts once, preserves the first order, and is stored
// once; the raw manifest size independently bounds parsing repeated values.
func uniqueImportDeps(deps []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for i, dep := range deps {
		if !stableCardID(dep) {
			return nil, fmt.Errorf("dependency %d is not a valid card ID (maximum %d bytes)", i+1, maxImportIDBytes)
		}
		if seen[dep] {
			continue
		}
		if len(out) == maxImportDeps {
			return nil, fmt.Errorf("card exceeds %d unique dependencies", maxImportDeps)
		}
		seen[dep] = true
		out = append(out, dep)
	}
	return out, nil
}

type importIssues struct {
	messages []string
	bytes    int
}

// All formatting uses printable, byte-bounded IDs and fixed-format text.
// Leave room for the cap marker; do not format an item after the budget.
func (issues *importIssues) add(format string, args ...any) bool {
	const omitted = "additional conflicts omitted"
	if len(issues.messages) >= maxImportIssues-1 || issues.bytes+maxImportIssue+len(omitted) > maxImportIssueSum {
		issues.messages = append(issues.messages, omitted)
		return false
	}
	msg := fmt.Sprintf(format, args...)
	issues.messages = append(issues.messages, msg)
	issues.bytes += len(msg)
	return true
}

type importDependencyNode struct {
	deps  []string
	found bool
}

func validateCardBatch(ctx context.Context, db *store.Store, objective string, cards []model.Card) ([]string, error) {
	if _, err := db.GetObjective(ctx, objective); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return []string{fmt.Sprintf("objective %s not found", objective)}, nil
	}
	var issues importIssues
	batch := make(map[string][]string, len(cards))
	edgeCount := 0
	for _, card := range cards {
		if !cardBranchOK(objective, card.ID) {
			return nil, fmt.Errorf("card %s cannot name a branch", card.ID)
		}
		batch[card.ID] = card.Deps
		edgeCount += len(card.Deps)
		_, err := db.CardWaitState(ctx, card.ID)
		if err == nil {
			if !issues.add("card %s already exists", card.ID) {
				return issues.messages, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if len(issues.messages) > 0 {
		return issues.messages, nil
	}

	cache := make(map[string]importDependencyNode)
	resolve := func(id string) ([]string, bool, error) {
		if deps, ok := batch[id]; ok {
			return deps, true, nil
		}
		if node, ok := cache[id]; ok {
			return node.deps, node.found, nil
		}
		if len(batch)+len(cache) >= maxImportGraph {
			return nil, false, fmt.Errorf("dependency graph exceeds %d cards", maxImportGraph)
		}
		deps, err := db.CardImportDependencies(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			cache[id] = importDependencyNode{}
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		deps, err = uniqueImportDeps(deps)
		if err != nil {
			return nil, false, fmt.Errorf("stored dependency graph cannot be validated: %w", err)
		}
		// Charge every unique stored edge once when read, not later when
		// traversed. A wide layer of direct dependencies cannot use the
		// preflight cache to bypass the graph's memory and work budget.
		edgeCount += len(deps)
		if edgeCount > maxImportEdges {
			return nil, false, fmt.Errorf("dependency graph exceeds %d unique edges", maxImportEdges)
		}
		cache[id] = importDependencyNode{deps: deps, found: true}
		return deps, true, nil
	}

	// Check direct dependencies first. Report only the first bounded set of
	// missing IDs and stop before traversing a graph that cannot be imported.
	for _, card := range cards {
		for _, dep := range card.Deps {
			if _, exists, err := resolve(dep); err != nil {
				return nil, err
			} else if !exists && !issues.add("dependency %s of %s not found", dep, card.ID) {
				return issues.messages, nil
			}
		}
	}
	if len(issues.messages) > 0 {
		return issues.messages, nil
	}

	// A single deterministic back-edge proves rejection. Do not duplicate the
	// DFS stack, even in a dense graph with many long, overlapping cycles.
	color := make(map[string]uint8)
	var visit func(string, string) (bool, error)
	visit = func(id, parent string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		color[id] = 1
		deps, found, err := resolve(id)
		if err != nil {
			return false, err
		}
		if !found {
			issues.add("dependency %s of %s not found", id, parent)
			return true, nil
		}
		for _, dep := range deps {
			switch color[dep] {
			case 1:
				issues.add("dependency cycle: edge from %s to %s closes the cycle", id, dep)
				return true, nil
			case 0:
				if invalid, err := visit(dep, id); invalid || err != nil {
					return invalid, err
				}
			}
		}
		color[id] = 2
		return false, nil
	}
	for _, card := range cards {
		if color[card.ID] == 0 {
			if invalid, err := visit(card.ID, ""); invalid || err != nil {
				return issues.messages, err
			}
		}
	}
	return nil, nil
}

func applyCardBatch(ctx context.Context, dbPath, objective, origin string, cards []model.Card) error {
	return store.WriteCardImport(ctx, dbPath, func(ctx context.Context, db *store.Store) error {
		conflicts, err := validateCardBatch(ctx, db, objective, cards)
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return &batchConflicts{list: conflicts}
		}
		for i := range cards {
			card := cards[i]
			card.Objective = objective
			if err := db.CreateCard(ctx, &card); err != nil {
				return err
			}
			if card.ID != cards[i].ID {
				return fmt.Errorf("card id %s was not stored unchanged", cards[i].ID)
			}
			if err := saveCardOrigin(ctx, db, card.ID, origin); err != nil {
				return err
			}
		}
		return nil
	})
}

func importCardViews(cards []model.Card) []cardImportView {
	out := make([]cardImportView, 0, len(cards))
	for _, card := range cards {
		claims := make([]string, 0, len(card.Claims))
		for _, claim := range card.Claims {
			if claim.Symbol == "" {
				claims = append(claims, claim.Path)
				continue
			}
			claims = append(claims, claim.Path+"="+claim.Symbol)
		}
		out = append(out, cardImportView{
			ID:         card.ID,
			Kind:       string(card.Kind),
			Title:      card.Title,
			Deps:       copyOrEmpty(card.Deps),
			Claims:     claims,
			Acceptance: copyOrEmpty(card.Acceptance),
		})
	}
	return out
}

func copyOrEmpty(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return append([]string{}, in...)
}

func writeImportReport(stdout io.Writer, asJSON bool, report cardImportReport) int {
	if asJSON {
		return writeJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "card import %s\n", report.Mode)
	fmt.Fprintf(stdout, "objective: %s\n", report.Objective)
	fmt.Fprintln(stdout, "cards:")
	if len(report.Cards) == 0 {
		fmt.Fprintln(stdout, "- none")
	}
	for _, card := range report.Cards {
		fmt.Fprintf(stdout, "- %s\t%s\t%s\n", card.ID, card.Kind, strconv.Quote(card.Title))
		fmt.Fprintf(stdout, "  deps: %s\n", quoteList(card.Deps))
		fmt.Fprintf(stdout, "  claims: %s\n", quoteList(card.Claims))
		fmt.Fprintf(stdout, "  acceptance: %s\n", quoteList(card.Acceptance))
	}
	fmt.Fprintln(stdout, "conflicts:")
	if len(report.Conflicts) == 0 {
		fmt.Fprintln(stdout, "- none")
	} else {
		for _, conflict := range report.Conflicts {
			fmt.Fprintf(stdout, "- %s\n", conflict)
		}
	}
	return 0
}

func quoteList(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = strconv.Quote(item)
	}
	return strings.Join(parts, " ")
}

func decodeStrict(raw []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	// More() is false at a closing bracket, so it does not prove the input ended.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func manifestDecodeError(err error) error {
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) {
		// UnmarshalTypeError.Value may contain the entire overflowing
		// number. Report only the known schema field and expected type.
		if mismatch.Field != "" {
			return fmt.Errorf("manifest field %q requires %s", mismatch.Field, mismatch.Type)
		}
		return fmt.Errorf("manifest requires %s", mismatch.Type)
	}
	const marker = "unknown field "
	msg := err.Error()
	if i := strings.Index(msg, marker); i >= 0 {
		field := strings.Trim(msg[i+len(marker):], " \"")
		if field != "" {
			return fmt.Errorf("unsupported field %q", field)
		}
	}
	return fmt.Errorf("manifest: %w", err)
}

// encoding/json accepts duplicate and case-insensitive object keys. A
// manifest is a human-reviewable write input, so reject ambiguous spelling
// and repeated keys instead of allowing later values to override earlier ones.
func checkManifestKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return errors.New("too many nested JSON values")
		}
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				token, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if len(key) > maxImportField {
					return errors.New("manifest field name is too long")
				}
				for _, r := range key {
					if (r < 'a' || r > 'z') && r != '_' {
						return fmt.Errorf("unsupported field %q", key)
					}
				}
				if seen[key] {
					return fmt.Errorf("duplicate field %q", key)
				}
				seen[key] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case json.Delim('['):
			for dec.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return nil
		}
		_, err = dec.Token()
		return err
	}
	return walk(0)
}
