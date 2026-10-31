package claims

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"cardex/internal/model"
)

// ParseSymbols returns top-level functions, methods as Type.Method, and types
// declared in a Go file. The map value is the source text of that symbol.
func ParseSymbols(filename string, src []byte) (map[string]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if file == nil {
		return nil, err
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recv := recvName(d.Recv.List[0].Type)
				if recv == "" {
					recv = "_"
				}
				name = recv + "." + d.Name.Name
			}
			out[name] = snippet(fset, src, d)
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				out[ts.Name.Name] = snippet(fset, src, ts)
			}
		}
	}
	return out, nil
}

func recvName(e ast.Expr) string {
	switch n := e.(type) {
	case *ast.Ident:
		return n.Name
	case *ast.StarExpr:
		return recvName(n.X)
	case *ast.IndexExpr:
		return recvName(n.X)
	case *ast.IndexListExpr:
		return recvName(n.X)
	case *ast.SelectorExpr:
		return n.Sel.Name
	default:
		return ""
	}
}

func snippet(fset *token.FileSet, src []byte, n ast.Node) string {
	start := fset.Position(n.Pos()).Offset
	end := fset.Position(n.End()).Offset
	if start < 0 || end > len(src) || start > end {
		return ""
	}
	return string(src[start:end])
}

// ChangedSymbols compares two commits. New files are claimed by exact path,
// non-Go files at file level, and modified Go files by the symbols whose
// source changed.
func ChangedSymbols(repo, before, after string) ([]model.Claim, error) {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(before) == "" || strings.TrimSpace(after) == "" {
		return nil, fmt.Errorf("claims: repo, before, and after are required")
	}
	status, err := gitBytes(context.Background(), repo, "diff", "--name-status", "--no-renames", "-z", before, after)
	if err != nil {
		return nil, err
	}
	return claimsFromStatus(context.Background(), repo, before, after, status, false)
}

// WorktreeSymbols compares commit before to the working tree at dir, including
// untracked files. .cardex-result.md is not a claim.
func WorktreeSymbols(ctx context.Context, dir, before string) ([]model.Claim, error) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(before) == "" {
		return nil, fmt.Errorf("claims: worktree and base are required")
	}
	status, err := gitBytes(ctx, dir, "diff", "--name-status", "--no-renames", "-z", before)
	if err != nil {
		return nil, err
	}
	claims, err := claimsFromStatus(ctx, dir, before, "", status, true)
	if err != nil {
		return nil, err
	}
	untracked, err := gitBytes(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, path := range splitNUL(untracked) {
		path = filepath.ToSlash(path)
		if path == "" || path == resultFile {
			continue
		}
		claims = append(claims, model.Claim{Path: path})
	}
	sortClaims(claims)
	return claims, nil
}

const resultFile = ".cardex-result.md"

func claimsFromStatus(ctx context.Context, repo, before, after string, status []byte, worktree bool) ([]model.Claim, error) {
	var out []model.Claim
	parts := splitNUL(status)
	for i := 0; i+1 < len(parts); i += 2 {
		code := parts[i]
		path := filepath.ToSlash(parts[i+1])
		if path == "" || path == resultFile {
			continue
		}
		if code == "" {
			continue
		}
		kind := code[0]
		var beforeSrc, afterSrc []byte
		var err error
		switch kind {
		case 'A':
			out = append(out, model.Claim{Path: path})
			continue
		case 'D':
			beforeSrc, err = showFile(ctx, repo, before, path)
			if err != nil {
				return nil, err
			}
			out = append(out, modifiedClaims(path, beforeSrc, nil, false)...)
			continue
		default:
			beforeSrc, err = showFile(ctx, repo, before, path)
			if err != nil {
				return nil, err
			}
			if worktree {
				afterSrc, err = os.ReadFile(filepath.Join(repo, filepath.FromSlash(path)))
			} else {
				afterSrc, err = showFile(ctx, repo, after, path)
			}
			if err != nil {
				return nil, err
			}
			if bytes.Equal(beforeSrc, afterSrc) {
				// Git recorded this path, but the blob bytes match: a mode
				// or type change. That is one file-level claim. Do not drop
				// it, and do not invent symbols from the unchanged text.
				out = append(out, model.Claim{Path: path})
				continue
			}
			out = append(out, modifiedClaims(path, beforeSrc, afterSrc, true)...)
		}
	}
	sortClaims(out)
	return out, nil
}

func modifiedClaims(path string, before, after []byte, haveAfter bool) []model.Claim {
	if !strings.HasSuffix(path, ".go") {
		return []model.Claim{{Path: path}}
	}
	if !haveAfter {
		names := symbolNames(path, before)
		if len(names) == 0 {
			return []model.Claim{{Path: path}}
		}
		out := make([]model.Claim, 0, len(names))
		for _, name := range names {
			out = append(out, model.Claim{Path: path, Symbol: name})
		}
		return out
	}
	oldSym, oldErr := ParseSymbols(path, before)
	newSym, newErr := ParseSymbols(path, after)
	if oldSym == nil && oldErr != nil || newSym == nil && newErr != nil {
		return []model.Claim{{Path: path}}
	}
	if oldSym == nil {
		oldSym = map[string]string{}
	}
	if newSym == nil {
		newSym = map[string]string{}
	}
	seen := map[string]bool{}
	var names []string
	for name, src := range newSym {
		if oldSym[name] != src {
			names = append(names, name)
		}
		seen[name] = true
	}
	for name := range oldSym {
		if !seen[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return []model.Claim{{Path: path}}
	}
	sort.Strings(names)
	out := make([]model.Claim, 0, len(names))
	for _, name := range names {
		out = append(out, model.Claim{Path: path, Symbol: name})
	}
	return out
}

func symbolNames(path string, src []byte) []string {
	syms, err := ParseSymbols(path, src)
	if err != nil && len(syms) == 0 {
		return nil
	}
	names := make([]string, 0, len(syms))
	for name := range syms {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortClaims(claims []model.Claim) {
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Path != claims[j].Path {
			return claims[i].Path < claims[j].Path
		}
		return claims[i].Symbol < claims[j].Symbol
	})
}

func showFile(ctx context.Context, repo, rev, path string) ([]byte, error) {
	return gitBytes(ctx, repo, "show", rev+":"+path)
}

func gitBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func splitNUL(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	raw := bytes.Split(b, []byte{0})
	var out []string
	for _, p := range raw {
		if len(p) == 0 {
			continue
		}
		out = append(out, string(p))
	}
	return out
}
