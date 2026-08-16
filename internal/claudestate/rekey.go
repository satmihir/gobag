package claudestate

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/satmihir/gobag/internal/overlay"
)

// RekeyResult reports what happened to a memory tree.
type RekeyResult struct {
	// Files is one entry per file, carrying the overlay outcome.
	Files []overlay.Result
	// Rewritten lists files whose contents had the old workspace root
	// substituted for the new one.
	Rewritten []string
}

// Conflicts returns the files where the user's version was preserved.
func (r RekeyResult) Conflicts() []overlay.Result {
	var out []overlay.Result
	for _, f := range r.Files {
		if f.Conflicted() {
			out = append(out, f)
		}
	}
	return out
}

// rewritableExt lists the file types whose contents may be path-rewritten.
// Memory files are small prose; anything else travels byte-for-byte, because a
// blind substitution inside an unknown format is how you corrupt data.
var rewritableExt = map[string]bool{".md": true, ".markdown": true}

// Rekey copies a memory tree from srcDir into dstDir under the overlay rule,
// substituting oldRoot for newRoot inside markdown files.
//
// Claude Code keys project state by absolute working directory, so memory
// restored to a different path is invisible to it unless the directory is
// re-keyed and the paths inside are updated. This is best effort by design:
// callers report failures as orientation warnings rather than aborting an
// install, because stale prose is a far smaller problem than a failed restore.
func Rekey(srcDir, dstDir, oldRoot, newRoot string) (RekeyResult, error) {
	var res RekeyResult

	info, err := os.Stat(srcDir)
	if err != nil {
		return res, fmt.Errorf("reading memory directory: %w", err)
	}
	if !info.IsDir() {
		return res, fmt.Errorf("memory source %s is not a directory", srcDir)
	}

	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("locating %s: %w", path, err)
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", rel, err)
		}
		if rewritten, changed := rewritePaths(content, oldRoot, newRoot, filepath.Ext(path)); changed {
			content = rewritten
			res.Rewritten = append(res.Rewritten, filepath.ToSlash(rel))
		}

		mode := fs.FileMode(0o644)
		if fi, err := d.Info(); err == nil {
			mode = fi.Mode().Perm()
		}
		out, err := overlay.Write(dstDir, filepath.ToSlash(rel), mode, bytes.NewReader(content))
		if err != nil {
			return err
		}
		res.Files = append(res.Files, out)
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("re-keying memory: %w", err)
	}
	return res, nil
}

// rewritePaths substitutes the old workspace root for the new one. It is a
// literal prefix substitution, not a heuristic: anything cleverer risks
// mangling prose that merely mentions a similar path.
func rewritePaths(content []byte, oldRoot, newRoot, ext string) ([]byte, bool) {
	if oldRoot == "" || newRoot == "" || oldRoot == newRoot {
		return content, false
	}
	if !rewritableExt[strings.ToLower(ext)] {
		return content, false
	}
	if !bytes.Contains(content, []byte(oldRoot)) {
		return content, false
	}
	return bytes.ReplaceAll(content, []byte(oldRoot), []byte(newRoot)), true
}
