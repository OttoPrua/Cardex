package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrOptionalCandidateUpdateUnknown = errors.New("optional completion outcome unknown after ref update attempt; inspect local branch before reuse")
	ErrOptionalCandidateHeld          = errors.New("optional completion held")
	ErrOptionalIntegrationChanged     = errors.New("approved integration ref no longer matches")
	ErrOptionalRefProofAmbiguous      = errors.New("local Git ref proof or precommit transaction could not be verified")
	ErrOptionalPrecommitNoWrite       = errors.New("optional completion did not send a ref commit; exact refs still match")
	ErrOptionalPrimaryCheckedOut      = errors.New("primary branch is checked out")
	ErrOptionalCandidateDiverged      = errors.New("candidate cannot fast-forward the approved primary")
	ErrOptionalCandidateChanged       = errors.New("primary branch tip changed")
	ErrOptionalRepositoryBusy         = errors.New("project checkout has an operation in progress")
	errOptionalProof                  = ErrOptionalRefProofAmbiguous
)

func optionalHold(reason error) error {
	return fmt.Errorf("%w: %w", ErrOptionalCandidateHeld, reason)
}

// MergeOptionalCandidate performs only an exact, local ref-only fast-forward.
// It never checks out a branch, runs a merge driver or hook, or creates an
// untested merge commit. A checked-out or diverged primary is a visible hold.
// The Git transaction is not atomic with the caller's database transaction.
func (Git) MergeOptionalCandidate(ctx context.Context, repo, primary, integration, expectedPrimary, candidate string, approvedWorktreeRoot ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	proof, length, err := checkOptionalCandidate(ctx, repo, primary, integration, expectedPrimary, candidate, approvedWorktreeRoot)
	if err != nil {
		return optionalHold(err)
	}
	defer proof.close()
	return optionalRefTransaction(ctx, repo, primary, integration, expectedPrimary, candidate, proof, length)
}

// CheckOptionalCandidate only proves current eligibility. It neither reserves
// the refs nor grants authority, and the same proof is repeated at execution.
func (Git) CheckOptionalCandidate(ctx context.Context, repo, primary, integration, expectedPrimary, candidate string, approvedWorktreeRoot ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	proof, _, err := checkOptionalCandidate(ctx, repo, primary, integration, expectedPrimary, candidate, approvedWorktreeRoot)
	if err != nil {
		return optionalHold(err)
	}
	proof.close()
	return nil
}

func checkOptionalCandidate(ctx context.Context, repo, primary, integration, expectedPrimary, candidate string, approvedWorktreeRoot []string) (*optionalRepoProof, int, error) {
	if !filepath.IsAbs(repo) || (primary != "refs/heads/main" && primary != "refs/heads/master") ||
		!strings.HasPrefix(integration, "refs/heads/") || integration == primary || strings.ContainsAny(integration, " \t\r\n\x00") {
		return nil, 0, ErrOptionalRefProofAmbiguous
	}
	proof, err := proveOptionalRepo(ctx, repo, approvedWorktreeRoot)
	if err != nil {
		return nil, 0, err
	}
	ok := false
	defer func() {
		if !ok {
			proof.close()
		}
	}()
	for _, query := range [][]string{{"--absolute-git-dir"}, {"--path-format=absolute", "--git-common-dir"}} {
		got, err := optionalGitOutput(ctx, repo, append([]string{"rev-parse"}, query...)...)
		if err != nil || filepath.Clean(got) != filepath.Join(repo, ".git") {
			return nil, 0, ErrOptionalRefProofAmbiguous
		}
	}
	if _, err := optionalGitOutput(ctx, repo, "check-ref-format", integration); err != nil {
		return nil, 0, ErrOptionalRefProofAmbiguous
	}
	raw, err := optionalGitOutput(ctx, repo, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return nil, 0, ErrOptionalRefProofAmbiguous
	}
	length := 0
	switch raw {
	case "sha1":
		length = 40
	case "sha256":
		length = 64
	}
	if length == 0 || !optionalOID(expectedPrimary, length) || !optionalOID(candidate, length) {
		return nil, 0, ErrOptionalRefProofAmbiguous
	}
	if err := optionalTipsMatch(ctx, repo, primary, integration, expectedPrimary, candidate); err != nil {
		return nil, 0, err
	}
	if _, err := optionalGitOutput(ctx, repo, "merge-base", "--is-ancestor", expectedPrimary, candidate); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, 0, ErrOptionalCandidateDiverged
		}
		return nil, 0, ErrOptionalRefProofAmbiguous
	}
	if err := optionalTargetFree(ctx, repo, primary, proof, length); err != nil {
		return nil, 0, err
	}
	ok = true
	return proof, length, nil
}

func optionalOID(s string, length int) bool {
	return len(s) == length && strings.Trim(s, "0123456789abcdef") == ""
}

func optionalTipsMatch(ctx context.Context, repo, primary, integration, oldOID, newOID string) error {
	for _, tip := range []struct {
		ref, oid string
		changed  error
	}{
		{integration, newOID, ErrOptionalIntegrationChanged},
		{primary, oldOID, ErrOptionalCandidateChanged},
	} {
		_, err := optionalGitOutput(ctx, repo, "symbolic-ref", "--no-recurse", "--quiet", tip.ref)
		var exit *exec.ExitError
		if err == nil {
			return tip.changed
		} // only direct refs are admissible
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return ErrOptionalRefProofAmbiguous
		}
		actual, err := optionalGitOutput(ctx, repo, "show-ref", "--verify", "--hash", tip.ref)
		if err != nil {
			_, existsErr := optionalGitOutput(ctx, repo, "show-ref", "--exists", tip.ref)
			if errors.As(existsErr, &exit) && exit.ExitCode() == 2 {
				return tip.changed
			}
			return ErrOptionalRefProofAmbiguous
		}
		if !optionalOID(actual, len(tip.oid)) {
			return ErrOptionalRefProofAmbiguous
		}
		if actual != tip.oid {
			return tip.changed
		}
		kind, err := optionalGitOutput(ctx, repo, "cat-file", "-t", tip.oid)
		if err != nil || kind != "commit" {
			return ErrOptionalRefProofAmbiguous
		}
	}
	return nil
}

func optionalTargetFree(ctx context.Context, repo, primary string, proof *optionalRepoProof, oidLen int) error {
	if !proof.safe(ctx, repo) {
		return ErrOptionalRefProofAmbiguous
	}
	paths, err := optionalAdminStates(ctx, proof.admin, repo, primary, proof.worktreeRoot)
	if err != nil {
		return err
	}
	raw, err := optionalGitOutput(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return errOptionalProof
	}
	return optionalPorcelain(raw, primary, paths, oidLen)
}

// Accept only full records for independently recognized checkout paths.
func optionalPorcelain(raw, primary string, paths map[string]bool, oidLen int) error {
	groups := strings.Split(raw, "\x00\x00")
	if len(groups) != len(paths)+1 || groups[len(groups)-1] != "" {
		return errOptionalProof
	}
	seen := make(map[string]bool, len(paths))
	for _, record := range groups[:len(groups)-1] {
		row := strings.Split(record, "\x00")
		if len(row) != 3 || !strings.HasPrefix(row[0], "worktree ") || !strings.HasPrefix(row[1], "HEAD ") ||
			!optionalOID(strings.TrimPrefix(row[1], "HEAD "), oidLen) {
			return errOptionalProof
		}
		path := filepath.FromSlash(strings.TrimPrefix(row[0], "worktree "))
		if path != filepath.Clean(path) || !paths[path] || seen[path] {
			return errOptionalProof
		}
		seen[path] = true
		if row[2] == "branch "+primary {
			return ErrOptionalPrimaryCheckedOut
		}
		branch, isBranch := strings.CutPrefix(row[2], "branch refs/heads/")
		if row[2] != "detached" && (!isBranch || branch == "" || strings.ContainsAny(branch, " \t\r\n\x00")) {
			return errOptionalProof
		}
	}
	return nil
}

// Only rooted ordinary linked checkout descriptors are supported. For a
// foreign/unsupported link we can still report a typed busy/primary hold from
// its Git-owned admin, but never hand its path to a subprocess as evidence.
func optionalAdminStates(ctx context.Context, root *os.Root, repo, primary, worktreeRoot string) (map[string]bool, error) {
	paths := map[string]bool{repo: true}
	if err := optionalAdminIdle(root); err != nil {
		return nil, err
	}
	mainHead, err := optionalFileBytes(root, "HEAD")
	if err != nil {
		return nil, errOptionalProof
	}
	if strings.TrimSpace(string(mainHead)) == "ref: "+primary {
		return nil, ErrOptionalPrimaryCheckedOut
	}
	if _, err := root.Lstat("worktrees"); errors.Is(err, os.ErrNotExist) {
		return paths, nil
	}
	admins, _, err := openOptionalChild(root, "worktrees")
	if err != nil {
		return nil, errOptionalProof
	}
	defer admins.Close()
	dir, err := admins.Open(".")
	if err != nil {
		return nil, errOptionalProof
	}
	const maxWorktrees = 4096
	entries, err := dir.ReadDir(maxWorktrees + 1)
	_ = dir.Close()
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > maxWorktrees {
		return nil, errOptionalProof
	}
	for _, entry := range entries {
		child, _, err := openOptionalChild(admins, entry.Name())
		if err != nil {
			return nil, errOptionalProof
		}
		if err := optionalAdminIdle(child); err != nil {
			_ = child.Close()
			return nil, err
		}
		head, e1 := optionalFileBytes(child, "HEAD")
		pointer, e2 := optionalFileBytes(child, "gitdir")
		common, e3 := optionalFileBytes(child, "commondir")
		cfgErr := optionalWorktreeConfig(ctx, child, repo, filepath.Join(repo, ".git", "worktrees", entry.Name()))
		_ = child.Close()
		if e1 != nil || e2 != nil || e3 != nil || cfgErr != nil {
			return nil, errOptionalProof
		}
		if strings.TrimSpace(string(head)) == "ref: "+primary {
			return nil, ErrOptionalPrimaryCheckedOut
		}
		gitdir := strings.TrimSuffix(string(pointer), "\n")
		linked := filepath.Dir(gitdir)
		rel, relErr := filepath.Rel(repo, linked)
		insideRepo := relErr == nil && rel != "." && filepath.IsLocal(rel)
		atConfiguredRoot := worktreeRoot != "" && filepath.Dir(linked) == worktreeRoot
		if (!insideRepo && !atConfiguredRoot) || filepath.Base(gitdir) != ".git" ||
			!filepath.IsAbs(gitdir) || gitdir != filepath.Clean(gitdir) || paths[linked] ||
			filepath.FromSlash(strings.TrimSpace(string(common))) != filepath.Join("..", "..") {
			return nil, errOptionalProof
		}
		linkedRoot, _, err := openOptionalAbsoluteDir(linked)
		if err != nil {
			return nil, errOptionalProof
		}
		back, err := optionalFileBytes(linkedRoot, ".git")
		_ = linkedRoot.Close()
		want := "gitdir: " + filepath.Join(repo, ".git", "worktrees", entry.Name())
		if err != nil || strings.TrimSuffix(string(back), "\n") != want {
			return nil, errOptionalProof
		}
		paths[linked] = true
	}
	return paths, nil
}

func optionalAdminIdle(root *os.Root) error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "BISECT_START", "BISECT_LOG"} {
		if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			return ErrOptionalRepositoryBusy
		}
	}
	return nil
}

func optionalRefTransaction(ctx context.Context, repo, primary, integration, oldOID, newOID string, proof *optionalRepoProof, oidLen int) error {
	cmd := optionalGit(ctx, repo, "update-ref", "--no-deref", "--stdin")
	var diagnostics optionalPrivateDiagnostic
	cmd.Stderr = &diagnostics
	in, err := cmd.StdinPipe()
	if err != nil {
		return optionalHold(errOptionalProof)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return optionalHold(errOptionalProof)
	}
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		return optionalHold(errOptionalProof)
	}
	waited := false
	defer func() {
		_ = in.Close() // EOF before "commit" aborts the locked transaction
		if !waited {
			_ = cmd.Wait()
		}
	}()
	read := bufio.NewScanner(io.LimitReader(out, 128))
	ack := func(expected string) bool { return read.Scan() && read.Text() == expected+": ok" }
	packet := fmt.Sprintf("start\nverify %s %s\nupdate %s %s %s\nprepare\n", integration, newOID, primary, newOID, oldOID)
	n, writeErr := io.WriteString(in, packet)
	startAck := writeErr == nil && n == len(packet) && ack("start")
	if !startAck || !ack("prepare") {
		// No commit packet was sent. Reap before comparing the bounded,
		// private diagnostic and never surface its path or text.
		_ = in.Close()
		exitErr := cmd.Wait()
		waited = true
		if !proof.safe(ctx, repo) {
			return optionalHold(ErrOptionalRefProofAmbiguous)
		}
		tipsErr := optionalTipsMatch(ctx, repo, primary, integration, oldOID, newOID)
		exitCode := -1
		var exited *exec.ExitError
		if errors.As(exitErr, &exited) {
			exitCode = exited.ExitCode()
		}
		return optionalHold(optionalPrepareOutcome(repo, primary, integration, startAck, exitCode, diagnostics, tipsErr))
	}
	if err := optionalTargetFree(ctx, repo, primary, proof, oidLen); err != nil {
		return optionalHold(err)
	}
	if err := optionalTipsMatch(ctx, repo, primary, integration, oldOID, newOID); err != nil {
		return optionalHold(err)
	}
	return optionalCommitTransaction(in, ack, func() error {
		err := cmd.Wait()
		waited = true
		return err
	})
}

// optionalPrivateDiagnostic never lets a subprocess make its raw output
// visible in workflow errors or logs. Overflow also defeats lock proof.
type optionalPrivateDiagnostic struct {
	buf      []byte
	overflow bool
}

func (d *optionalPrivateDiagnostic) Write(p []byte) (int, error) {
	const max = 4096
	n := len(p)
	if len(p) > max-len(d.buf) {
		d.overflow = true
		p = p[:max-len(d.buf)]
	}
	d.buf = append(d.buf, p...)
	return n, nil
}

func optionalPrepareOutcome(repo, primary, integration string, startAck bool, exitCode int, diagnostics optionalPrivateDiagnostic, tipsErr error) error {
	// Only a proven current integration mismatch is conclusive after a
	// failed prepare. An apparent primary-only change cannot rule out an
	// integration compare failure followed by an external reset.
	if errors.Is(tipsErr, ErrOptionalIntegrationChanged) {
		return ErrOptionalIntegrationChanged
	}
	if tipsErr != nil {
		return ErrOptionalRefProofAmbiguous
	}
	if !startAck || exitCode != 128 ||
		diagnostics.overflow || !optionalExactLockDiagnostic(repo, primary, integration, string(diagnostics.buf)) {
		return ErrOptionalRefProofAmbiguous
	}
	return ErrOptionalPrecommitNoWrite
}

func optionalExactLockDiagnostic(repo, primary, integration, raw string) bool {
	if strings.ContainsAny(repo, "\r\n\x00") {
		return false
	}
	// Accept only the known files-backend EEXIST diagnostic for one of the
	// two admitted refs and its literal lockfile. A compare mismatch ("is at
	// ... but expected ...") can never match even if later tips are reset.
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	line, tail, _ := strings.Cut(raw, "\n")
	const guidance = "\nAnother git process seems to be running in this repository, e.g.\nan editor opened by 'git commit'. Please make sure all processes\nare terminated then try again. If it still fails, a git process\nmay have crashed in this repository earlier:\nremove the file manually to continue.\n"
	const newerGuidance = "Another git process seems to be running in this repository, or the lock file may be stale"
	if strings.TrimSpace(tail) != "" && strings.TrimSpace(tail) != strings.TrimSpace(guidance) && strings.TrimSpace(tail) != newerGuidance {
		return false
	}
	for _, ref := range []string{primary, integration} {
		lock := filepath.Join(repo, ".git", filepath.FromSlash(ref)+".lock")
		for _, path := range []string{lock, filepath.ToSlash(lock)} {
			want := "fatal: prepare: cannot lock ref '" + ref + "': Unable to create '" + path + "': File exists."
			if line == want {
				return true
			}
		}
	}
	return false
}

// Once the commit write has been attempted, even failure to write/observe
// the protocol acknowledgement is not evidence that mutation did not occur.
func optionalCommitTransaction(in io.WriteCloser, ack func(string) bool, wait func() error) error {
	if n, err := io.WriteString(in, "commit\n"); err != nil || n != len("commit\n") || !ack("commit") {
		return ErrOptionalCandidateUpdateUnknown
	}
	_ = in.Close()
	if err := wait(); err != nil {
		return ErrOptionalCandidateUpdateUnknown
	}
	return nil
}

func optionalGit(ctx context.Context, repo string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "--no-replace-objects", "--no-optional-locks",
		"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "protocol.allow=never"}, args...)...)
	cmd.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0",
		"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=", "LC_ALL=C"}
	for _, k := range []string{"PATH", "SystemRoot", "WINDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	return cmd
}

func optionalGitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	// Inputs are local metadata only. Bound worktree list output and kill
	// the invocation if it exceeds the limit instead of draining forever.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := optionalGit(ctx, repo, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	const limit = 1 << 20
	b, err := io.ReadAll(io.LimitReader(out, limit+1))
	if err != nil || len(b) > limit {
		cancel()
		_ = cmd.Wait()
		return "", errOptionalProof
	}
	if err := cmd.Wait(); err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(b)), nil
}
