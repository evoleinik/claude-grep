package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMainRepoPath_NotInGit confirms ("", false) outside a git repo.
func TestMainRepoPath_NotInGit(t *testing.T) {
	tmp := t.TempDir()
	got, ok := mainRepoPath(tmp)
	if ok || got != "" {
		t.Errorf("non-git dir: got (%q, %v), want (\"\", false)", got, ok)
	}
}

// TestMainRepoPath_MainWorktree confirms ("", false) in the main worktree.
func TestMainRepoPath_MainWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	got, ok := mainRepoPath(repo)
	if ok {
		t.Errorf("main worktree: got (%q, %v), want (\"\", false)", got, ok)
	}
}

// TestMainRepoPath_LinkedWorktree confirms a linked worktree returns the main path.
func TestMainRepoPath_LinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@test")
	runGit(t, repo, "config", "user.name", "test")
	runGit(t, repo, "commit", "--allow-empty", "-q", "-m", "init")

	wtRoot := filepath.Join(repo, ".claude", "worktrees")
	if err := os.MkdirAll(wtRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(wtRoot, "feature")
	runGit(t, repo, "worktree", "add", "-q", wt, "-b", "feature")

	got, ok := mainRepoPath(wt)
	if !ok {
		t.Fatalf("linked worktree: got ok=false, want true")
	}
	// Resolve symlinks (macOS /var → /private/var) before comparing.
	wantResolved, _ := filepath.EvalSymlinks(repo)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("linked worktree: got %q, want %q", gotResolved, wantResolved)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
