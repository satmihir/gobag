// Package gitops is gobag's entire relationship with git: walk-mode discovery
// on the pack side, verification of a plan's repository claims, and the
// idempotent clone/fetch/worktree operations install performs.
//
// Every operation shells out to the system git binary (locked decision: no
// go-git, the dependency budget is full). Two rules govern this package:
//
//   - Nothing hangs. Every invocation carries a context deadline, and network
//     operations are additionally told never to prompt for credentials.
//   - Nothing is destroyed. There is no reset, no force, no delete, and no
//     checkout over uncommitted work anywhere in this package. When the
//     target is not in a state gobag can safely advance, it is left exactly
//     as found and the caller is told why.
package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// NetworkTimeout bounds any git operation that talks to a remote (ls-remote,
// fetch, clone). It is a variable so tests can shorten it; treat it as
// configuration, not as state.
var NetworkTimeout = 60 * time.Second

// localTimeout bounds purely local git invocations. Generous, because it only
// exists to keep a wedged git from wedging gobag.
const localTimeout = 30 * time.Second

// ErrNoGit reports that no git binary is on PATH. Everything in this package
// depends on one.
var ErrNoGit = errors.New("git was not found on PATH")

// gitError carries a failed invocation's stderr, which is what the user
// actually needs to see, plus whether the failure was a timeout.
type gitError struct {
	args    []string
	output  string
	timeout bool
	err     error
}

func (e *gitError) Error() string {
	msg := firstLine(e.output)
	if msg == "" {
		msg = e.err.Error()
	}
	if e.timeout {
		return fmt.Sprintf("git %s timed out after %s", strings.Join(e.args, " "), NetworkTimeout)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.args, " "), msg)
}

func (e *gitError) Unwrap() error { return e.err }

// timedOut reports whether err is a git invocation that hit its deadline.
func timedOut(err error) bool {
	var ge *gitError
	return errors.As(err, &ge) && ge.timeout
}

// gitEnv builds the environment for a git subprocess. It inherits the user's
// environment on purpose — credential helpers, ssh-agent, and url.insteadOf
// rewrites live there, and a tool that clones private repositories cannot
// throw them away. What it overrides is anything that could make git
// interactive, paged, or locale-dependent.
func gitEnv() []string {
	env := os.Environ()
	env = append(env,
		"GIT_TERMINAL_PROMPT=0", // never block waiting for a password
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0", // read-only commands must not touch the index
		"LC_ALL=C",             // parseable, stable output
	)
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		// BatchMode still uses ssh-agent and keys; it only refuses to prompt.
		env = append(env, "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	}
	return env
}

// run executes git in dir under a deadline and returns trimmed stdout.
// Combined output is captured so a failure carries git's own explanation.
func run(dir string, timeout time.Duration, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", ErrNoGit
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	cmd.Stdin = nil

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &gitError{
			args:    args,
			output:  string(out),
			timeout: ctx.Err() != nil,
			err:     err,
		}
	}
	return strings.TrimSpace(string(out)), nil
}

// local runs a git command that needs no network.
func local(dir string, args ...string) (string, error) {
	return run(dir, localTimeout, args...)
}

// network runs a git command that may contact a remote, under NetworkTimeout.
func network(dir string, args ...string) (string, error) {
	return run(dir, NetworkTimeout, args...)
}

// lines splits trimmed git output into non-empty lines.
func lines(out string) []string {
	if out == "" {
		return nil
	}
	raw := strings.Split(out, "\n")
	kept := make([]string, 0, len(raw))
	for _, l := range raw {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, l)
		}
	}
	return kept
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// isRepo reports whether dir is inside a git working tree.
func isRepo(dir string) bool {
	out, err := local(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// head returns the full sha at HEAD.
func head(dir string) (string, error) {
	return local(dir, "rev-parse", "HEAD")
}

// branchOf returns the checked-out branch, or "" for a detached HEAD.
func branchOf(dir string) string {
	out, err := local(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// resolve returns the full sha a ref names, or an error when it does not
// resolve to a commit in this repository.
func resolve(dir, ref string) (string, error) {
	return local(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

// dirty reports whether the working tree has uncommitted changes to tracked
// files. Untracked files are excluded on purpose: build output and scratch
// files are not "work in progress", and git itself refuses to clobber an
// untracked file during a checkout or merge, so they need no separate guard.
func dirty(dir string) (bool, error) {
	out, err := local(dir, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, fmt.Errorf("reading status of %s: %w", dir, err)
	}
	return out != "", nil
}

// remoteURL returns the URL gobag should record for a repository: origin when
// it exists, otherwise the first remote by name. An empty name means the
// repository has no remote at all.
func remoteURL(dir string) (name, url string, err error) {
	out, err := local(dir, "remote")
	if err != nil {
		return "", "", fmt.Errorf("listing remotes of %s: %w", dir, err)
	}
	names := lines(out)
	if len(names) == 0 {
		return "", "", nil
	}
	name = names[0]
	for _, n := range names {
		if n == "origin" {
			name = "origin"
			break
		}
	}
	url, err = local(dir, "remote", "get-url", name)
	if err != nil {
		return "", "", fmt.Errorf("reading remote %s of %s: %w", name, dir, err)
	}
	return name, url, nil
}

// pushed reports whether ref is reachable from some remote-tracking branch,
// i.e. whether a clone elsewhere could ever check it out. Purely local: it
// reads refs/remotes, it does not contact the remote.
func pushed(dir, ref string) (bool, error) {
	out, err := local(dir, "branch", "-r", "--contains", ref)
	if err != nil {
		// A ref with no remote-tracking branches at all makes git exit
		// non-zero on some versions; treat that as "not pushed" rather than
		// as a failure.
		return false, nil
	}
	return len(lines(out)) > 0, nil
}

// sameRemote compares two remote URLs for the narrow purpose of deciding
// whether an existing checkout is the repository the manifest describes. It
// tolerates only cosmetic differences — a trailing slash or .git suffix.
// Anything else counts as a mismatch, because the safe response to a mismatch
// is to touch nothing.
func sameRemote(a, b string) bool {
	return normalizeRemote(a) == normalizeRemote(b)
}

func normalizeRemote(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	return strings.TrimSuffix(u, "/")
}
