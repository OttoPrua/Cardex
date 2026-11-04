package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGeneratedLocalReviewOmitsOnlyMirrorHelperRules(t *testing.T) {
	t.Parallel()
	for _, unknown := range []string{"", "Bash(git rev-parse:*)", "Bash(bash /other/verify-mirror-fingerprint.sh:*)"} {
		t.Run(unknown, func(t *testing.T) {
			root := testRoot(t)
			cfg := defaultConfig(root)
			td := cfg.TypeDefaults[typeReview]
			want := append([]string(nil), td.AllowedTools...)
			td.AllowedTools = append(td.AllowedTools,
				"Bash(bash "+filepath.Join(root, "verify-mirror-fingerprint.sh")+":*)",
				"Bash(bash ~/.cardex/verify-mirror-fingerprint.sh:*)")
			if unknown != "" {
				td.AllowedTools = append(td.AllowedTools, unknown)
				want = append(want, unknown)
			}
			cfg.TypeDefaults[typeReview] = td
			original := append([]string(nil), td.AllowedTools...)
			parent := newTask(root, cfg, typeSequence, "implementation", t.TempDir(), []string{"implement"}, 5)
			parent.ReviewAfter = true
			if err := saveTask(root, parent); err != nil {
				t.Fatal(err)
			}
			review, err := ensureReviewAfterTask(root, cfg, parent, nil)
			if err != nil || review == nil {
				t.Fatalf("review generation: review=%+v err=%v", review, err)
			}
			stored, err := findTaskAnywhere(root, review.ID)
			if err != nil || !reflect.DeepEqual(stored.AllowedTools, want) {
				t.Fatalf("persisted review rules: review=%+v err=%v want=%v", stored, err, want)
			}
			args, acp, policy, err := grokBuildProductArgs(cfg, stored, "grok-4.6", "xhigh", "unused")
			if unknown == "" {
				if err != nil || !acp || !reflect.DeepEqual(policy.Consumed, want) || grokBuildArgvHasPrivilegeWidening(args) {
					t.Fatalf("generated review must use frozen ACP rules: args=%v policy=%+v err=%v", args, policy, err)
				}
			} else if err == nil || len(args) != 0 {
				t.Fatalf("unknown generated rule must fail closed: args=%v err=%v", args, err)
			}
			if !reflect.DeepEqual(cfg.TypeDefaults[typeReview].AllowedTools, original) {
				t.Fatal("generation changed shared type defaults")
			}
		})
	}
}

func TestReviewMirrorRulesRetainedWhenMirrorOrLocalityUnproven(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"remote", "fingerprint", "scripts", "dangling-marker", "missing-dir", "existing-review"} {
		t.Run(kind, func(t *testing.T) {
			root := testRoot(t)
			cfg := defaultConfig(root)
			td := cfg.TypeDefaults[typeReview]
			td.AllowedTools = append(td.AllowedTools, "Bash(bash "+filepath.Join(root, "verify-mirror-fingerprint.sh")+":*)", "Bash(bash ~/.cardex/verify-mirror-fingerprint.sh:*)")
			cfg.TypeDefaults[typeReview] = td
			parent := newTask(root, cfg, typeSequence, "implementation", t.TempDir(), []string{"implement"}, 5)
			parent.ReviewAfter = true
			switch kind {
			case "remote":
				parent.RemoteHost = "review-host"
			case "fingerprint":
				if err := os.WriteFile(filepath.Join(parent.Dir, ".claudego-fingerprint"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "scripts":
				if err := os.Mkdir(filepath.Join(parent.Dir, ".claudego-scripts"), 0700); err != nil {
					t.Fatal(err)
				}
			case "dangling-marker":
				if err := os.Symlink(filepath.Join(parent.Dir, "missing"), filepath.Join(parent.Dir, ".claudego-scripts")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "missing-dir":
				parent.Dir = filepath.Join(parent.Dir, "missing")
			case "existing-review":
				review := newTask(root, cfg, typeReview, "existing", parent.Dir, nil, 5)
				review.ReviewOf = parent.ID
				if err := saveTask(root, review); err != nil {
					t.Fatal(err)
				}
			}
			if err := saveTask(root, parent); err != nil {
				t.Fatal(err)
			}
			review, err := ensureReviewAfterTask(root, cfg, parent, nil)
			if err != nil || review == nil || !reflect.DeepEqual(review.AllowedTools, td.AllowedTools) {
				t.Fatalf("required/existing rules changed: review=%+v err=%v", review, err)
			}
			if _, err := consumeGrokFrozenAllowRules(review.AllowedTools); err == nil {
				t.Fatal("mirror helper grants must remain outside Grok's frozen grammar")
			}
		})
	}
}
