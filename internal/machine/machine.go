// Package machine holds the per-machine registry: where large repositories
// that cannot travel already live on this box.
//
// A monorepo measured in tens of gigabytes cannot be cloned per restore, and
// on any machine where you would restore one, a clone almost certainly already
// exists. The registry is how that answer is given once per machine instead of
// once per restore.
//
// This is the one piece of gobag that persists state outside a workspace, so
// it is written only when the user explicitly links a repository — never as a
// side effect of packing or installing.
package machine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileEnv overrides the registry location. Tests set it; so can anyone who
// wants the registry somewhere other than the default.
const FileEnv = "GOBAG_MACHINE_FILE"

// Registry maps a normalized remote URL to a local clone on this machine.
type Registry struct {
	// Repos is keyed by normalized remote URL, so the same repository is found
	// whether it was recorded as ssh or https, with or without a .git suffix.
	Repos map[string]string `json:"repos"`

	path string
}

// Path returns the registry file location.
func Path() string {
	if p := os.Getenv(FileEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gobag", "machine.json")
}

// Load reads the registry, returning an empty one when the file does not yet
// exist. A machine with no registry is the normal starting state, not an error.
func Load() (*Registry, error) {
	r := &Registry{Repos: map[string]string{}, path: Path()}
	if r.path == "" {
		return r, nil
	}

	b, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", r.path, err)
	}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("reading %s: %w", r.path, err)
	}
	if r.Repos == nil {
		r.Repos = map[string]string{}
	}
	return r, nil
}

// Lookup finds a recorded clone for a remote. A recorded path that has since
// been deleted reports as missing, so a stale entry degrades to "not linked"
// rather than to a confusing git failure.
func (r *Registry) Lookup(remote string) (string, bool) {
	path, ok := r.Repos[NormalizeRemote(remote)]
	if !ok {
		return "", false
	}
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

// Set records a clone location.
func (r *Registry) Set(remote, path string) {
	r.Repos[NormalizeRemote(remote)] = path
}

// Save writes the registry, creating its directory if needed.
func (r *Registry) Save() error {
	if r.path == "" {
		return fmt.Errorf("no home directory, so there is nowhere to record this")
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(r.path), err)
	}

	// Marshal through a sorted view so the file does not churn between writes.
	keys := make([]string, 0, len(r.Repos))
	for k := range r.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(keys))
	for _, k := range keys {
		ordered[k] = r.Repos[k]
	}

	b, err := json.MarshalIndent(struct {
		Repos map[string]string `json:"repos"`
	}{ordered}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding registry: %w", err)
	}
	if err := os.WriteFile(r.path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", r.path, err)
	}
	return nil
}

// NormalizeRemote reduces a remote URL to a comparison key, so the same
// repository matches whether it was cloned over ssh or https. It is
// deliberately conservative: it strips only the parts that never change which
// repository is meant.
func NormalizeRemote(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	u = strings.TrimSuffix(u, "/")

	// git@host:org/repo -> host/org/repo
	if !strings.Contains(u, "://") {
		if at := strings.Index(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		u = strings.Replace(u, ":", "/", 1)
		return strings.ToLower(u)
	}

	// scheme://[user@]host/path -> host/path
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
		u = u[at+1:]
	}
	return strings.ToLower(u)
}
