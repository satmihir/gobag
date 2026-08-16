// Package claudestate handles Claude Code's on-disk project state: the
// encoded-cwd directory scheme under ~/.claude/projects and the rules for
// moving that state to a new workspace root.
//
// Claude Code keys project state by absolute working directory, encoding it
// into a single directory name. gobag must re-key that state on install,
// because the restored workspace rarely sits at the same absolute path.
package claudestate

import (
	"os"
	"path/filepath"
	"strings"
)

// EncodeProjectDir converts an absolute workspace path into the directory
// name Claude Code uses under ~/.claude/projects. The scheme, established
// empirically against Claude Code itself, replaces every character that is not
// a letter or digit with a dash — not just path separators: /a/dot.ted_dir
// becomes -a-dot-ted-dir. Getting this wrong installs memory where Claude
// Code will never look, which is silent amnesia.
func EncodeProjectDir(absPath string) string {
	p := filepath.ToSlash(filepath.Clean(absPath))
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, p)
}

// ProjectsRoot returns the directory holding per-project Claude Code state.
// It honors CLAUDE_CONFIG_DIR when set so tests never touch a real home.
func ProjectsRoot() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}
