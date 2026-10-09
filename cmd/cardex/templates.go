package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

//go:embed templates/*.md
var embeddedTemplates embed.FS

const (
	templateBaselineName  = ".shipped.json"
	templateBackupDirName = ".refresh-backup"
)

type templateBaselineDoc struct {
	Files map[string]string `json:"files"`
}

type templateFileStatus struct {
	Name       string
	Source     string
	Difference string
}

type templateRefreshResult struct {
	Name       string
	BackupPath string
	Wrote      bool
}

// writeDefaultTemplates 把内置模板复制到数据目录（已存在的不覆盖，用户可自行修改）。
// 仅给本次新写入的文件记录当前 shipped digest；已有副本不补基准，保持 UNKNOWN。
func writeDefaultTemplates(root string) error {
	_, dir, err := confinedTemplatesDir(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, _, err := confinedTemplatesDir(root); err != nil {
		return err
	}
	entries, err := embeddedTemplates.ReadDir("templates")
	if err != nil {
		return err
	}
	meta, err := loadTemplateBaseline(root)
	if err != nil {
		return err
	}
	changed := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dst := filepath.Join(dir, e.Name())
		if _, err := os.Lstat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := embeddedTemplates.ReadFile("templates/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
		if meta.Files == nil {
			meta.Files = map[string]string{}
		}
		meta.Files[e.Name()] = sha256Hex(string(data))
		changed = true
	}
	if !changed {
		return nil
	}
	return saveTemplateBaseline(root, meta)
}

// loadTemplate 优先读数据目录里（可能被用户改过的）模板，退回内置版本。
func loadTemplate(root, name string) (string, error) {
	path := filepath.Join(templatesDir(root), name+".md")
	if data, err := os.ReadFile(path); err == nil {
		return string(data), nil
	}
	data, err := embeddedTemplates.ReadFile("templates/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("找不到模板 %s: %w", name, err)
	}
	return string(data), nil
}

var templateVarRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// renderTemplate 单遍替换 {{KEY}} 占位符。单遍(而非按 map 循环 ReplaceAll)有两个必须:
// ①注入值里若含字面 {{OTHER}} 不会被二次替换(否则甲结论含 {{B}} 会被乙结论顶掉=注入);
// ②与 vars 的 map 迭代顺序无关(Go map 迭代随机,循环替换会让 C 的合并 prompt 跨运行非确定)。
// 未在 vars 里的 {{X}} 原样保留(同旧行为)。
func renderTemplate(tpl string, vars map[string]string) string {
	return templateVarRe.ReplaceAllStringFunc(tpl, func(m string) string {
		if v, ok := vars[m[2:len(m)-2]]; ok {
			return v
		}
		return m
	})
}

func templateBaselinePath(root string) string {
	return filepath.Join(templatesDir(root), templateBaselineName)
}

func loadTemplateBaseline(root string) (templateBaselineDoc, error) {
	doc := templateBaselineDoc{Files: map[string]string{}}
	data, err := os.ReadFile(templateBaselinePath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return doc, err
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return templateBaselineDoc{}, fmt.Errorf("read template baseline: %w", err)
	}
	if doc.Files == nil {
		doc.Files = map[string]string{}
	}
	return doc, nil
}

func saveTemplateBaseline(root string, doc templateBaselineDoc) error {
	if doc.Files == nil {
		doc.Files = map[string]string{}
	}
	absRoot, dir, err := confinedTemplatesDir(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	meta := filepath.Join(dir, templateBaselineName)
	if err := refuseEscapingAncestors(absRoot, meta); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(meta, append(data, '\n'))
}

func recordTemplateBaseline(root, filename string, shipped []byte) error {
	meta, err := loadTemplateBaseline(root)
	if err != nil {
		return err
	}
	if meta.Files == nil {
		meta.Files = map[string]string{}
	}
	meta.Files[filename] = sha256Hex(string(shipped))
	return saveTemplateBaseline(root, meta)
}

func embeddedTemplateBytes(filename string) ([]byte, error) {
	return embeddedTemplates.ReadFile("templates/" + filename)
}

func normalizeTemplateFilename(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("empty template name")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("absolute template name refused: %s", name)
	}
	slash := filepath.ToSlash(name)
	if strings.Contains(slash, "/") || strings.ContainsRune(name, filepath.Separator) {
		return "", fmt.Errorf("path-separated template name refused: %s", name)
	}
	if slash == "." || slash == ".." || strings.Contains(slash, "..") {
		return "", fmt.Errorf("traversing template name refused: %s", name)
	}
	if strings.HasPrefix(slash, ".") {
		return "", fmt.Errorf("hidden template name refused: %s", name)
	}
	base := filepath.Base(slash)
	if base != slash {
		return "", fmt.Errorf("template name refused: %s", name)
	}
	if !strings.HasSuffix(base, ".md") {
		base += ".md"
	}
	stem := strings.TrimSuffix(base, ".md")
	if stem == "" {
		return "", fmt.Errorf("invalid template name: %s", name)
	}
	for _, r := range stem {
		if r == '-' || r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return "", fmt.Errorf("invalid template name: %s", name)
	}
	return base, nil
}

func confinedTemplatePath(root, name string) (string, error) {
	file, err := normalizeTemplateFilename(name)
	if err != nil {
		return "", err
	}
	absRoot, dir, err := confinedTemplatesDir(root)
	if err != nil {
		return "", err
	}
	dest := filepath.Clean(filepath.Join(dir, file))
	if !pathInsideRoot(dir, dest) {
		return "", fmt.Errorf("template name %q escapes templates directory", name)
	}
	if err := refuseEscapingAncestors(absRoot, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func absDataRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if _, err := os.Lstat(abs); err != nil {
		if os.IsNotExist(err) {
			return abs, nil
		}
		return "", err
	}
	resolved, err := evalStable(abs)
	if err != nil {
		return "", fmt.Errorf("canonicalize data root: %w", err)
	}
	return resolved, nil
}

func confinedTemplatesDir(root string) (absRoot, dir string, err error) {
	absRoot, err = absDataRoot(root)
	if err != nil {
		return "", "", err
	}
	dir = filepath.Join(absRoot, "templates")
	if err := refuseEscapingAncestors(absRoot, dir); err != nil {
		return "", "", err
	}
	return absRoot, dir, nil
}

func refuseEscapingSymlinkAt(absRoot, path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("refusing symlink %s: %w", path, err)
	}
	resolved = filepath.Clean(resolved)
	if !pathInsideRoot(absRoot, resolved) {
		return fmt.Errorf("refusing symlink %s outside root", path)
	}
	return nil
}

func refuseEscapingAncestors(absRoot, path string) error {
	absRoot = filepath.Clean(absRoot)
	cur := filepath.Clean(path)
	for {
		if cur == absRoot {
			return nil
		}
		if err := refuseEscapingSymlinkAt(absRoot, cur); err != nil {
			return err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil
		}
		if !pathInsideRoot(absRoot, parent) && parent != absRoot {
			return fmt.Errorf("path %s escapes root", path)
		}
		cur = parent
	}
}

func refuseTemplateSymlink(root, dest string) error {
	absRoot, err := absDataRoot(root)
	if err != nil {
		return err
	}
	if err := refuseEscapingAncestors(absRoot, dest); err != nil {
		return err
	}
	fi, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return fmt.Errorf("refusing to follow symlink template %s", dest)
}

func templateRefreshBackupDir(root string) (string, error) {
	absRoot, dir, err := confinedTemplatesDir(root)
	if err != nil {
		return "", err
	}
	bdir := filepath.Join(dir, templateBackupDirName)
	if err := refuseEscapingAncestors(absRoot, bdir); err != nil {
		return "", err
	}
	fi, err := os.Lstat(bdir)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink backup dir %s", bdir)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("backup dir is not a directory: %s", bdir)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return bdir, nil
}

func writeTemplateRefreshBackup(root, filename string, data []byte) (string, error) {
	absRoot, err := absDataRoot(root)
	if err != nil {
		return "", err
	}
	bdir, err := templateRefreshBackupDir(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		return "", err
	}
	if err := refuseEscapingAncestors(absRoot, bdir); err != nil {
		return "", err
	}
	fi, err := os.Lstat(bdir)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return "", fmt.Errorf("backup dir is not a directory: %s", bdir)
	}
	for i := 0; i < 64; i++ {
		dest := filepath.Clean(filepath.Join(bdir, fmt.Sprintf("%s.%d", filename, time.Now().UnixNano())))
		if !pathInsideRoot(filepath.Clean(bdir), dest) {
			return "", fmt.Errorf("backup path escapes")
		}
		if err := refuseEscapingAncestors(absRoot, dest); err != nil {
			return "", err
		}
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			_ = os.Remove(dest)
			return "", werr
		}
		if cerr != nil {
			_ = os.Remove(dest)
			return "", cerr
		}
		return dest, nil
	}
	return "", fmt.Errorf("could not allocate unique backup name for %s", filename)
}

func classifyTemplateFile(filename string, user []byte, hasUser bool, shipped []byte, hasShipped bool, recorded string, hasRecorded bool) templateFileStatus {
	st := templateFileStatus{Name: strings.TrimSuffix(filename, ".md")}
	curEq := hasUser && hasShipped && bytes.Equal(user, shipped)
	switch {
	case !hasUser && hasShipped:
		st.Source = "embedded"
		st.Difference = "missing"
	case !hasRecorded:
		if curEq {
			st.Source = "current-equality"
			st.Difference = "equal-to-current-shipped"
		} else {
			st.Source = "UNKNOWN"
			st.Difference = "custom"
		}
	default:
		matchesRecorded := hasUser && sha256Hex(string(user)) == recorded
		st.Source = "recorded"
		switch {
		case matchesRecorded && curEq:
			st.Source = "shipped"
			st.Difference = "none"
		case matchesRecorded:
			st.Difference = "outdated"
		case curEq:
			st.Difference = "equal-to-current-shipped"
		default:
			st.Difference = "custom"
		}
	}
	return st
}

func listTemplateStatuses(root string) ([]templateFileStatus, error) {
	_, dir, err := confinedTemplatesDir(root)
	if err != nil {
		return nil, err
	}
	meta, err := loadTemplateBaseline(root)
	if err != nil {
		return nil, err
	}
	names := map[string]struct{}{}
	entries, err := embeddedTemplates.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names[e.Name()] = struct{}{}
	}
	if onDisk, err := os.ReadDir(dir); err == nil {
		for _, e := range onDisk {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			names[e.Name()] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)
	out := make([]templateFileStatus, 0, len(ordered))
	for _, filename := range ordered {
		var user []byte
		hasUser := false
		path := filepath.Join(dir, filename)
		if data, err := os.ReadFile(path); err == nil {
			user, hasUser = data, true
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		shipped, err := embeddedTemplateBytes(filename)
		hasShipped := err == nil
		if err != nil {
			shipped = nil
		}
		rec, hasRec := meta.Files[filename]
		out = append(out, classifyTemplateFile(filename, user, hasUser, shipped, hasShipped, rec, hasRec))
	}
	return out, nil
}

func refreshSelectedTemplates(root string, names []string) ([]templateRefreshResult, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("refresh requires explicit template names")
	}
	if _, _, err := confinedTemplatesDir(root); err != nil {
		return nil, err
	}
	type job struct {
		name     string
		filename string
		dest     string
		current  []byte
		exists   bool
		shipped  []byte
	}
	jobs := make([]job, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		dest, err := confinedTemplatePath(root, name)
		if err != nil {
			return nil, err
		}
		if err := refuseTemplateSymlink(root, dest); err != nil {
			return nil, err
		}
		filename := filepath.Base(dest)
		if _, dup := seen[filename]; dup {
			continue
		}
		seen[filename] = struct{}{}
		shipped, err := embeddedTemplateBytes(filename)
		if err != nil {
			return nil, fmt.Errorf("not a shipped template: %s", name)
		}
		j := job{name: strings.TrimSuffix(filename, ".md"), filename: filename, dest: dest, shipped: shipped}
		cur, err := os.ReadFile(dest)
		if err == nil {
			j.current = cur
			j.exists = true
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	results := make([]templateRefreshResult, 0, len(jobs))
	for _, j := range jobs {
		res := templateRefreshResult{Name: j.name}
		if j.exists && bytes.Equal(j.current, j.shipped) {
			if err := recordTemplateBaseline(root, j.filename, j.shipped); err != nil {
				return results, err
			}
			results = append(results, res)
			continue
		}
		if j.exists {
			backup, err := writeTemplateRefreshBackup(root, j.filename, j.current)
			if err != nil {
				return results, err
			}
			res.BackupPath = backup
		}
		if err := os.MkdirAll(filepath.Dir(j.dest), 0o755); err != nil {
			return results, err
		}
		if err := os.WriteFile(j.dest, j.shipped, 0o644); err != nil {
			return results, err
		}
		res.Wrote = true
		if err := recordTemplateBaseline(root, j.filename, j.shipped); err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}
