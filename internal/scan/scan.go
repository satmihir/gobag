// Package scan performs the pack-time secret scan: a pure-regex pass over
// every file about to enter a .gobag archive, including agent-authored
// handoff documents. Model sanitization is a courtesy, not a security
// boundary, so the binary looks for itself.
//
// The scan warns and never blocks. It reports file:line, a human-readable
// pattern name, and a redacted excerpt — the report itself must never leak a
// full credential. False positives are expected and preferred to silence:
// this is a warning channel, not a gate.
package scan

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// MaxFileSize caps how much of a single file is scanned. Beyond it the
	// remainder is skipped and noted in the findings.
	MaxFileSize = 10 << 20
	// PatternSuspiciousName marks a finding derived from the path alone, with
	// no content match behind it.
	PatternSuspiciousName = "suspicious filename"
	// PatternUnscanned marks a note rather than a match: the file, or the tail
	// of it, was never inspected. Excerpt carries the reason.
	PatternUnscanned = "unscanned"
)

const (
	// readChunk is the line buffer. Lines longer than this are scanned in
	// chunks rather than loaded whole, so no file size can blow up memory.
	readChunk = 256 << 10
	// sniffBytes is how much of a file is inspected for a NUL byte before
	// deciding it is binary and not worth scanning.
	sniffBytes = 8 << 10
	// keepChars is how much of a matched credential survives redaction.
	keepChars = 4
	// maxExcerpt bounds a redacted excerpt, so a pathological line cannot
	// produce a pathological report.
	maxExcerpt = 64

	reasonTooLarge = "over 10MB"
)

// Finding is one warning about one file.
type Finding struct {
	// Path is the path scanned, exactly as the caller gave it.
	Path string
	// Line is 1-indexed, or 0 for findings that are not tied to a line
	// (PatternSuspiciousName, PatternUnscanned).
	Line int
	// Pattern is the human-readable pattern name, e.g. "AWS access key".
	Pattern string
	// Excerpt is the matched text, redacted past the first few characters of
	// the credential itself. Empty for a suspicious filename; the skip reason
	// for PatternUnscanned.
	Excerpt string
}

// pattern is one content rule. secret is the submatch index whose start marks
// the beginning of the credential within the match; 0 means the whole match.
// Everything before that offset is context worth showing (e.g. "api_key: ").
type pattern struct {
	name   string
	re     *regexp.Regexp
	secret int
}

// patterns are evaluated in order, and that order fixes the order of findings
// reported for a single line.
var patterns = []pattern{
	{"AWS access key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`), 0},
	{"private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`), 0},
	{"GitHub token", regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`), 0},
	{"GitHub token", regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`), 0},
	{"Slack token", regexp.MustCompile(`xox[abps]-[A-Za-z0-9-]+`), 0},
	{"JWT", regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+`), 0},
	// Deliberately noisy: any assignment of a long opaque value to a
	// secret-shaped name. Kept because this is a warning channel, not a gate.
	{"generic secret assignment", regexp.MustCompile(
		`(?i)(?:api[_-]?key|secret|password|token)\s*[:=]\s*['"]?([A-Za-z0-9/+_-]{16,})`), 1},
}

// suspiciousNames are exact base names that warrant a warning regardless of
// content — the file may be binary, encoded, or empty and still be a leak.
var suspiciousNames = map[string]bool{
	"kubeconfig":  true,
	"id_rsa":      true,
	"id_ed25519":  true,
	"credentials": true,
	".env":        true,
}

// suspiciousSuffixes are matched against the lowercased base name.
var suspiciousSuffixes = []string{".env", ".pem", ".p12"}

// File scans one file. A file that cannot be opened or read is an error; a
// file that is binary or oversized is not — those are skipped, and the skip is
// reported through the findings.
func File(path string) ([]Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", path, err)
	}
	defer f.Close()
	return Reader(path, f)
}

// Reader scans an already-open stream. name is used for the path-based rules
// and for every Finding.Path, so it should be the path the user will
// recognize. Findings are returned in deterministic order: the filename
// warning first, then content matches in line order and, within a line, in
// pattern order.
func Reader(name string, r io.Reader) ([]Finding, error) {
	findings := nameFindings(name)

	br := bufio.NewReaderSize(r, readChunk)
	head, err := br.Peek(sniffBytes)
	if err != nil && err != io.EOF {
		return findings, fmt.Errorf("scanning %s: %w", name, err)
	}
	if bytes.IndexByte(head, 0) >= 0 {
		// Binary. Regexes over compressed or encoded bytes produce noise, not
		// signal; the filename rule above still had its say.
		return findings, nil
	}

	var (
		read int64
		line = 1
		// seen keeps a pattern from firing twice on one line, which chunked
		// reads of a very long line would otherwise cause.
		seen = map[int]bool{}
	)
	for {
		chunk, err := br.ReadSlice('\n')
		read += int64(len(chunk))
		findings = appendMatches(findings, name, line, chunk, seen)

		if read > MaxFileSize {
			return append(findings, Finding{
				Path:    name,
				Pattern: PatternUnscanned,
				Excerpt: reasonTooLarge,
			}), nil
		}
		switch {
		case err == bufio.ErrBufferFull:
			continue // Same line, next chunk.
		case err == io.EOF:
			return findings, nil
		case err != nil:
			return findings, fmt.Errorf("scanning %s: %w", name, err)
		}
		line++
		clear(seen)
	}
}

// Format writes a terse warning block. Nothing is written when there is
// nothing to say. Findings are printed in the order given.
func Format(findings []Finding, w io.Writer) error {
	var secrets, notes []Finding
	for _, f := range findings {
		if f.Pattern == PatternUnscanned {
			notes = append(notes, f)
			continue
		}
		secrets = append(secrets, f)
	}
	if len(secrets) == 0 && len(notes) == 0 {
		return nil
	}

	var b strings.Builder
	switch len(secrets) {
	case 0:
	case 1:
		b.WriteString("1 possible secret in a file about to be packed:\n")
	default:
		fmt.Fprintf(&b, "%d possible secrets in files about to be packed:\n", len(secrets))
	}
	for _, f := range secrets {
		loc := f.Path
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
		}
		b.WriteString("  " + loc + "  " + f.Pattern)
		if f.Excerpt != "" {
			b.WriteString("  " + f.Excerpt)
		}
		b.WriteString("\n")
	}
	for _, n := range notes {
		fmt.Fprintf(&b, "Not scanned (%s): %s\n", n.Excerpt, n.Path)
	}
	if len(secrets) > 0 {
		b.WriteString("These are warnings only — gobag is packing them anyway.\n")
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("writing scan report: %w", err)
	}
	return nil
}

// nameFindings applies the path-based rules.
func nameFindings(name string) []Finding {
	base := strings.ToLower(filepath.Base(filepath.FromSlash(name)))
	suspicious := suspiciousNames[base] || strings.HasPrefix(base, ".env.")
	if !suspicious {
		for _, suffix := range suspiciousSuffixes {
			if strings.HasSuffix(base, suffix) {
				suspicious = true
				break
			}
		}
	}
	if !suspicious {
		return nil
	}
	return []Finding{{Path: name, Pattern: PatternSuspiciousName}}
}

// appendMatches runs every pattern over one line (or one chunk of an
// over-long line), recording at most one finding per pattern per line.
func appendMatches(dst []Finding, name string, line int, b []byte, seen map[int]bool) []Finding {
	b = bytes.TrimRight(b, "\r\n")
	if len(b) == 0 {
		return dst
	}
	for i, p := range patterns {
		if seen[i] {
			continue
		}
		loc := p.re.FindSubmatchIndex(b)
		if loc == nil {
			continue
		}
		seen[i] = true
		start, end := loc[0], loc[1]
		secret := start
		if p.secret > 0 && 2*p.secret+1 < len(loc) && loc[2*p.secret] >= 0 {
			secret = loc[2*p.secret]
		}
		dst = append(dst, Finding{
			Path:    name,
			Line:    line,
			Pattern: p.name,
			Excerpt: redact(string(b[start:end]), secret-start),
		})
	}
	return dst
}

// redact keeps the context before the credential plus keepChars of the
// credential itself, and replaces the rest with an ellipsis.
func redact(match string, secretOffset int) string {
	keep := secretOffset + keepChars
	if keep > maxExcerpt {
		keep = maxExcerpt
	}
	if keep >= len(match) {
		return match
	}
	// Back up to a rune boundary; matched text is ASCII by construction, but
	// the context before a generic assignment need not be.
	for keep > 0 && !isRuneStart(match[keep]) {
		keep--
	}
	return match[:keep] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
