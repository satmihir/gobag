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
// name Claude Code uses under ~/.claude/projects. The scheme replaces each
// path separator with a dash, so /Users/u/ws/proj becomes -Users-u-ws-proj.
func EncodeProjectDir(absPath string) string {
	p := filepath.ToSlash(filepath.Clean(absPath))
	return strings.ReplaceAll(p, "/", "-")
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
