package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// gitEnv isolates test git invocations from the developer's own configuration
// and makes commit hashes reproducible across runs and machines.
func gitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=gobag test",
		"GIT_AUTHOR_EMAIL=test@gobag.invalid",
		"GIT_COMMITTER_NAME=gobag test",
		"GIT_COMMITTER_EMAIL=test@gobag.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
}

// Git runs a git command in dir and returns trimmed stdout, failing the test
// on any error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// RequireGit skips the test when no usable git binary is present.
func RequireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}
