package workspace

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git implements Manager with git worktrees. The zero value is ready to use.
type Git struct{}

var _ Manager = Git{}

// Create runs git worktree add -b for a new card, or checks out the existing
// card branch for a continuation. The new card starts at the integration
// branch HEAD; a continuation starts at the card branch HEAD.
func (Git) Create(ctx context.Context, req CreateRequest) (Worktree, error) {
	if strings.TrimSpace(req.CardID) == "" || strings.TrimSpace(req.AttemptID) == "" ||
		strings.TrimSpace(req.ObjectiveID) == "" || strings.TrimSpace(req.RepoPath) == "" ||
		strings.TrimSpace(req.WorktreeRoot) == "" {
		return Worktree{}, fmt.Errorf("workspace: incomplete create request")
	}
	branch := fmt.Sprintf("cardex/%s/c/%s", req.ObjectiveID, req.CardID)
	path := filepath.Join(req.WorktreeRoot, req.CardID+"-"+req.AttemptID)
	if err := os.MkdirAll(req.WorktreeRoot, 0o755); err != nil {
		return Worktree{}, err
	}
	if req.Continue {
		// The previous attempt's worktree still has this branch until merge.
		// Detach it in place so the new worktree can check the branch out.
		if err := detachCardBranch(ctx, req.RepoPath, branch); err != nil {
			return Worktree{}, err
		}
		if _, err := runGit(ctx, req.RepoPath, "worktree", "add", path, branch); err != nil {
			return Worktree{}, err
		}
	} else {
		if strings.TrimSpace(req.IntegrationBranch) == "" {
			return Worktree{}, fmt.Errorf("workspace: integration branch required")
		}
		if _, err := runGit(ctx, req.RepoPath, "worktree", "add", "-b", branch, path, req.IntegrationBranch); err != nil {
			return Worktree{}, err
		}
	}
	base, err := runGit(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return Worktree{}, err
	}
	if err := excludeResultFile(ctx, path); err != nil {
		return Worktree{}, err
	}
	return Worktree{Path: path, Branch: branch, BaseCommit: strings.TrimSpace(base)}, nil
}

// detachCardBranch frees the card branch when a previous attempt's worktree
// still has it checked out. The worktree directory and its commits stay.
func detachCardBranch(ctx context.Context, repo, branch string) error {
	raw, err := runGit(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	want := "refs/heads/" + branch
	var path, checked string
	release := func() error {
		if path == "" || checked != want {
			path, checked = "", ""
			return nil
		}
		if _, err := runGit(ctx, path, "checkout", "--detach"); err != nil {
			return err
		}
		path, checked = "", ""
		return nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			if err := release(); err != nil {
				return err
			}
			continue
		}
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "worktree":
			path = val
		case "branch":
			checked = val
		}
	}
	return release()
}

// Commit runs git add -A and commits when anything other than the excluded
// result file changed. A clean tree returns an empty hash and a nil error.
func (Git) Commit(ctx context.Context, worktree, message string) (string, error) {
	if strings.TrimSpace(worktree) == "" {
		return "", fmt.Errorf("workspace: commit worktree required")
	}
	if _, err := runGit(ctx, worktree, "add", "-A"); err != nil {
		return "", err
	}
	status, err := runGit(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		return "", nil
	}
	if strings.TrimSpace(message) == "" {
		message = "cardex: workspace commit"
	}
	if _, err := runGit(ctx, worktree,
		"-c", "user.name=cardex",
		"-c", "user.email=cardex@cardex.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "-m", message,
	); err != nil {
		return "", err
	}
	head, err := runGit(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head), nil
}

// Diff reports the patch and changed paths between base and the worktree,
// including untracked files and excluding the result file.
func (Git) Diff(ctx context.Context, worktree, base string) (Diff, error) {
	if strings.TrimSpace(worktree) == "" || strings.TrimSpace(base) == "" {
		return Diff{}, fmt.Errorf("workspace: diff worktree and base required")
	}
	names, err := runGit(ctx, worktree, "diff", "--name-only", "--relative", base)
	if err != nil {
		return Diff{}, err
	}
	untracked, err := runGit(ctx, worktree, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return Diff{}, err
	}
	patch, err := runGitRaw(ctx, worktree, "diff", "--no-ext-diff", base)
	if err != nil {
		return Diff{}, err
	}
	files := splitLines(names)
	extra := splitLines(untracked)
	var buf bytes.Buffer
	buf.Write(patch)
	for _, rel := range extra {
		rel = filepath.ToSlash(rel)
		if rel == "" || rel == ".cardex-result.md" {
			continue
		}
		files = append(files, rel)
		body, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(rel)))
		if err != nil {
			return Diff{}, err
		}
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			buf.WriteByte('\n')
		}
		fmt.Fprintf(&buf, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n", rel, rel, rel)
		for _, line := range bytes.Split(body, []byte("\n")) {
			buf.WriteString("+")
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}
	files = cleanPaths(files)
	return Diff{Patch: buf.String(), Files: files}, nil
}

// Remove deletes the worktree and leaves the card branch in place.
func (Git) Remove(ctx context.Context, worktree string) error {
	if strings.TrimSpace(worktree) == "" {
		return fmt.Errorf("workspace: remove worktree required")
	}
	abs, err := filepath.Abs(worktree)
	if err != nil {
		return err
	}
	common, err := gitCommonDir(ctx, abs)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "--git-dir", common, "worktree", "remove", "--force", abs)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// BlockPush turns on per-worktree config, points core.hooksPath at the
// Cardex reject-all hook, and sets every remote pushurl to an invalid
// address. Inherited pushurls stay in the effective list, and --no-verify
// skips the hook. A push that names a URL directly, including "." and a
// relative path, is rewritten by an empty pushInsteadOf. A longer inherited
// or process-level pushInsteadOf outranks that empty prefix, and a direct
// URL has no receivepack. push.followTags is not a boolean, so git push
// dies while reading config, before --push-option, -o, -c
// push.pushOption, --signed,
// --no-signed, --signed=if-asked, or --no-verify can replace a push
// default. A later -c push.followTags= value does not drop the worktree
// value; git still rejects it. Fetch and ls-remote do not read
// push.followTags. The newline push option and the reject-all hook stay;
// they are not this guarantee.
// The empty rewrite is
// ignored when a remote already has a pushurl, and http(s) ignores
// receivepack, so those pushurls are rewritten with insteadOf. insteadOf
// does not apply to a local fetch URL. protocol.allow is left unset.
// Push-related keys in the main repository config are left alone.
func (Git) BlockPush(ctx context.Context, worktree string) error {
	if strings.TrimSpace(worktree) == "" {
		return fmt.Errorf("workspace: block push worktree required")
	}
	// extensions.worktreeConfig has to live in the common config; otherwise
	// git refuses --worktree. It is not a push URL and does not set hooksPath.
	if _, err := runGit(ctx, worktree, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	dir, err := hookDir()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, worktree, "config", "--worktree", "core.hooksPath", abs); err != nil {
		return err
	}
	// An empty prefix matches every push URL, including "." and "nested/bare.git".
	// Fetch does not consult pushInsteadOf. A longer prefix wins; the
	// non-boolean push.followTags below still refuses that destination.
	if _, err := runGit(ctx, worktree, "config", "--worktree", "--add", "url."+blockedPushURL+"/.pushInsteadOf", ""); err != nil {
		return err
	}
	// pushInsteadOf does not apply to an explicit pushurl. http(s) also
	// ignores receivepack, so rewrite those schemes on every operation that
	// uses the URL. Local paths are not in this list, so fetch and ls-remote
	// of a local remote stay intact.
	for _, prefix := range httpPushSchemes {
		if _, err := runGit(ctx, worktree, "config", "--worktree", "--add", "url."+blockedPushURL+"/.insteadOf", prefix); err != nil {
			return err
		}
	}
	reject, err := exec.LookPath("false")
	if err != nil {
		return fmt.Errorf("workspace: push reject command: %w", err)
	}
	// git push parses every push.followTags value with the boolean parser.
	// A non-boolean dies before command-line push options or signing flags
	// are applied, and a higher-priority -c push.followTags=false does not
	// skip the worktree value. Fetch and ls-remote do not read it.
	if _, err := runGit(ctx, worktree, "config", "--worktree", "push.followTags", blockedPushFollowTags); err != nil {
		return err
	}
	// The newline push option still refuses a push that does not replace it.
	// --push-option and -o replace it, so it is not the guarantee above.
	if _, err := runGit(ctx, worktree, "config", "--worktree", "push.pushOption", blockedPushOption); err != nil {
		return err
	}
	// Worktree values override an inherited push.gpgSign=false. They do not
	// change the main repository config. gpg.format pins OpenPGP so an
	// inherited ssh signer is not used. Turning signing off skips this
	// path; the non-boolean push.followTags above still refuses the push.
	if _, err := runGit(ctx, worktree, "config", "--worktree", "push.gpgSign", "true"); err != nil {
		return err
	}
	if _, err := runGit(ctx, worktree, "config", "--worktree", "gpg.format", "openpgp"); err != nil {
		return err
	}
	if _, err := runGit(ctx, worktree, "config", "--worktree", "gpg.program", reject); err != nil {
		return err
	}
	raw, err := runGit(ctx, worktree, "remote")
	if err != nil {
		return err
	}
	for _, name := range splitLines(raw) {
		if _, err := runGit(ctx, worktree, "config", "--worktree", "remote."+name+".pushurl", blockedPushURL); err != nil {
			return err
		}
		// receivepack runs for local, git, and ssh pushes, including each
		// inherited pushurl, and --no-verify does not skip it. Fetch uses
		// upload-pack and is left alone.
		if _, err := runGit(ctx, worktree, "config", "--worktree", "remote."+name+".receivepack", reject); err != nil {
			return err
		}
	}
	return nil
}

const blockedPushURL = "invalid://cardex.blocked/push"

// blockedPushFollowTags is not a boolean. git push rejects it while
// reading config. Fetch and ls-remote do not consult this key.
const blockedPushFollowTags = "cardex-blocked"

// blockedPushOption contains a newline. git push rejects that only when
// the command does not replace it with --push-option or -o.
const blockedPushOption = "cardex-blocked\nrefuse"

// httpPushSchemes ignore remote.<name>.receivepack. Rewriting them stops an
// inherited pushurl of that scheme from being contacted.
var httpPushSchemes = []string{
	"https://",
	"http://",
	"ftp://",
	"ftps://",
}

func excludeResultFile(ctx context.Context, worktree string) error {
	gitDir, err := runGit(ctx, worktree, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	info := filepath.Join(strings.TrimSpace(gitDir), "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		return err
	}
	path := filepath.Join(info, "exclude")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !bytes.Contains(existing, []byte(".cardex-result.md")) {
		if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
			existing = append(existing, '\n')
		}
		existing = append(existing, []byte(".cardex-result.md\n")...)
		if err := os.WriteFile(path, existing, 0o644); err != nil {
			return err
		}
	}
	// Linked worktrees share $GIT_COMMON_DIR/info. Git does not read
	// $GIT_DIR/info/exclude for them, so point this worktree's own
	// core.excludesFile at the file just written.
	if _, err := runGit(ctx, worktree, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	if _, err := runGit(ctx, worktree, "config", "--worktree", "core.excludesFile", path); err != nil {
		return err
	}
	return nil
}

func gitCommonDir(ctx context.Context, worktree string) (string, error) {
	common, err := runGit(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		common, err = runGit(ctx, worktree, "rev-parse", "--git-common-dir")
		if err != nil {
			return "", err
		}
		common = strings.TrimSpace(common)
		if !filepath.IsAbs(common) {
			common = filepath.Join(worktree, common)
		}
		return filepath.Clean(common), nil
	}
	return filepath.Clean(strings.TrimSpace(common)), nil
}

func cleanPaths(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = filepath.ToSlash(strings.TrimSpace(p))
		if p == "" || p == ".cardex-result.md" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func splitLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := runGitRaw(ctx, dir, args...)
	return strings.TrimSpace(string(out)), err
}

func runGitRaw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.Bytes(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}
