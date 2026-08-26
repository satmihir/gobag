package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/satmihir/gobag/internal/archive"
	"github.com/satmihir/gobag/internal/claudestate"
	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/host"
	"github.com/satmihir/gobag/internal/machine"
	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/overlay"
	"github.com/satmihir/gobag/internal/reconcile"
)

// memoryPrefix is where memory cargo lands inside the workspace. It stays
// staged there unless the user asks for it to be installed into Claude Code's
// own state directory, because writing outside the workspace root is a
// deliberate act, not a side effect of unpacking.
const memoryPrefix = "state/memory/"

func cmdInstall(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "workspace root to restore into (default: current directory)")
	linkMemory := fs.Bool("link-memory", false,
		"also install memory into Claude Code's project state (writes outside the workspace)")
	fs.Usage = func() {
		io.WriteString(stderr, `usage: gobag install [-root DIR] [-link-memory] <archive>

Restores repositories, context, and state, then writes ORIENTATION.md
describing what landed and what changed while the archive was packed.
Every step is idempotent: re-running converges and never destroys your work.
`)
	}
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errUser("install needs exactly one archive path")
	}

	target, err := resolveRoot(*root)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "restoring into %s\n", target)

	registry, err := machine.Load()
	if err != nil {
		// A damaged registry must not block a restore; it only costs the
		// automatic linking of external repositories.
		fmt.Fprintf(stderr, "gobag: %v (continuing without it)\n", err)
		registry = &machine.Registry{Repos: map[string]string{}}
	}

	f, passphrase, err := openArchive(rest[0], stderr)
	if err != nil {
		return err
	}
	defer f.Close()

	unpacked, err := unpack(f, passphrase, target)
	if err != nil {
		return err
	}
	if unpacked.manifest == nil {
		return errUser("archive has no %s — it may not be a gobag archive", manifest.Name)
	}
	m := unpacked.manifest

	in := reconcile.Input{
		Manifest:    m,
		Root:        target,
		Files:       unpacked.files,
		Notes:       unpacked.notes,
		HandoffPath: handoffPath(m),
		CurrentHost: host.Current(),
	}

	// Repositories: clone or converge, never destroy. One source that cannot be
	// restored — an unreachable remote, a ref that was never pushed — must not
	// strand the rest of the workspace. Failures become orientation notes.
	for _, s := range m.Sources {
		// An external repository is never cloned. Either this machine already
		// has a copy recorded, or the install completes around it and says so.
		if s.External {
			res := linkExternal(target, s, registry, stdout)
			in.Repos = append(in.Repos, res)
			if res.Linkable() {
				in.Notes = append(in.Notes, fmt.Sprintf(
					"`%s` is external and not linked on this machine. Point gobag at a local "+
						"clone with:\n  gobag link %s <path-to-clone> --root %s",
					s.Dest, s.Dest, target))
			}
			continue
		}

		res, err := gitops.EnsureRepo(target, s)
		if err != nil {
			fmt.Fprintf(stderr, "  %s: %v\n", s.Dest, err)
			in.Notes = append(in.Notes, fmt.Sprintf("`%s` could not be restored: %v", s.Dest, err))
			continue
		}
		in.Repos = append(in.Repos, res)
		fmt.Fprintf(stdout, "  %s: %s\n", res.Dest, res.Outcome)

		for _, wt := range s.Worktrees {
			wres, err := gitops.EnsureWorktree(target, s.Dest, wt)
			if err != nil {
				fmt.Fprintf(stderr, "  %s: %v\n", wt.Dest, err)
				in.Notes = append(in.Notes,
					fmt.Sprintf("worktree `%s` could not be recreated: %v", wt.Dest, err))
				continue
			}
			in.Repos = append(in.Repos, wres)
			fmt.Fprintf(stdout, "  %s: %s\n", wres.Dest, wres.Outcome)
		}

		reality, err := gitops.RemoteReality(target, s)
		if err != nil {
			// A reality check that fails must not fail the restore; the
			// repository is already on disk either way.
			in.Notes = append(in.Notes,
				fmt.Sprintf("could not compare `%s` against its remote: %v", s.Dest, err))
			continue
		}
		in.Reality = append(in.Reality, reality)
	}

	// Memory: staged in the workspace by default, installed only on request.
	if *linkMemory {
		rekeyed, notes := linkMemoryIntoClaude(target, m.SourceRoot)
		in.MemoryRewritten = rekeyed
		in.Notes = append(in.Notes, notes...)
	} else if unpacked.hasMemory {
		in.Notes = append(in.Notes, fmt.Sprintf(
			"memory is staged at `%s` but not yet visible to Claude Code — "+
				"re-run with --link-memory to install it", memoryPrefix))
	}

	// Keep the manifest in the workspace so `gobag link` can finish the job
	// later without the archive being present.
	if err := saveInstalledManifest(target, m); err != nil {
		return err
	}
	if err := writeOrientation(target, in); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nwrote %s — read it first, then %s\n",
		reconcile.Name, orDefault(in.HandoffPath, "the repositories above"))
	return nil
}

// linkExternal attaches an external repository using a clone this machine has
// already recorded, or reports that none is recorded yet.
func linkExternal(target string, s manifest.Source, registry *machine.Registry, stdout io.Writer) gitops.Result {
	clonePath, ok := registry.Lookup(s.Remote)
	if !ok {
		res := gitops.UnlinkedResult(s)
		fmt.Fprintf(stdout, "  %s: %s\n", res.Dest, res.Outcome)
		return res
	}

	res, err := gitops.EnsureExternal(target, s, clonePath)
	if err != nil {
		res = gitops.Result{
			Dest:    s.Dest,
			Outcome: gitops.OutcomeUnlinked,
			Detail:  fmt.Sprintf("not linked: %v", err),
		}
	}
	fmt.Fprintf(stdout, "  %s: %s\n", res.Dest, res.Outcome)
	return res
}

// installedManifestPath is where install leaves the manifest for later
// commands. Inside the workspace root, so nothing is left elsewhere.
func installedManifestPath(root string) string {
	return filepath.Join(root, ".gobag", manifest.Name)
}

func saveInstalledManifest(root string, m *manifest.Manifest) error {
	dir := filepath.Dir(installedManifestPath(root))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return wrapUser(fmt.Errorf("creating %s: %w", dir, err))
	}
	var b strings.Builder
	if err := m.Encode(&b); err != nil {
		return err
	}
	if err := os.WriteFile(installedManifestPath(root), []byte(b.String()), 0o644); err != nil {
		return wrapUser(fmt.Errorf("recording the manifest: %w", err))
	}
	return nil
}

func resolveRoot(root string) (string, error) {
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolving current directory: %w", err)
		}
		root = cwd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", wrapUser(fmt.Errorf("resolving %s: %w", root, err))
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", wrapUser(fmt.Errorf("creating %s: %w", abs, err))
	}
	// Resolve symlinks so paths recorded in orientation match what the user
	// and the restored agent will actually see.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs, nil
}

type unpackResult struct {
	manifest  *manifest.Manifest
	files     []overlay.Result
	notes     []string
	hasMemory bool
}

// unpack streams the archive onto disk under the overlay rule. Entries are
// small documents and configuration; repositories travel as references and are
// materialized separately by git.
func unpack(r io.Reader, passphrase, target string) (unpackResult, error) {
	var out unpackResult

	ar, err := archive.NewReader(r, passphrase)
	if err != nil {
		return out, archiveErr(err)
	}
	defer ar.Close()

	for {
		hdr, err := ar.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, archiveErr(fmt.Errorf("reading archive: %w", err))
		}
		if hdr.Typeflag != '0' && hdr.Typeflag != 0 {
			continue
		}
		if hdr.Name == archive.ChecksumsName {
			continue
		}

		var buf bytes.Buffer
		if _, err := io.Copy(&buf, ar); err != nil {
			return out, fmt.Errorf("reading %s: %w", hdr.Name, err)
		}

		if hdr.Name == manifest.Name {
			m, err := manifest.Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				return out, err
			}
			out.manifest = m
			continue
		}
		if strings.HasPrefix(hdr.Name, memoryPrefix) {
			out.hasMemory = true
		}

		res, err := overlay.Write(target, hdr.Name, os.FileMode(hdr.Mode), bytes.NewReader(buf.Bytes()))
		if err != nil {
			return out, wrapUser(err)
		}
		out.files = append(out.files, res)
	}
	return out, nil
}

// linkMemoryIntoClaude re-keys staged memory into Claude Code's project state.
// Best effort by design: a failure here is an orientation note, never a failed
// restore, because stale prose is a far smaller problem than a lost workspace.
func linkMemoryIntoClaude(target, sourceRoot string) (rewritten, notes []string) {
	src := filepath.Join(target, filepath.FromSlash(strings.TrimSuffix(memoryPrefix, "/")))
	if _, err := os.Stat(src); err != nil {
		return nil, []string{"no memory travelled in this archive"}
	}

	projects := claudestate.ProjectsRoot()
	if projects == "" {
		return nil, []string{"could not locate Claude Code's project directory; memory left staged"}
	}
	dst := filepath.Join(projects, claudestate.EncodeProjectDir(target))

	res, err := claudestate.Rekey(src, dst, sourceRoot, target)
	if err != nil {
		return nil, []string{fmt.Sprintf("could not install memory: %v", err)}
	}
	for _, c := range res.Conflicts() {
		notes = append(notes, fmt.Sprintf(
			"memory file `%s` already existed and differed; yours was kept", c.Path))
	}
	notes = append(notes, fmt.Sprintf("memory installed into %s", dst))
	return res.Rewritten, notes
}

// writeOrientation always overwrites: the document is gobag's own regenerated
// output, and a stale one is worse than none.
func writeOrientation(target string, in reconcile.Input) error {
	var b strings.Builder
	if err := reconcile.Render(&b, in); err != nil {
		return err
	}
	path := filepath.Join(target, reconcile.Name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return wrapUser(fmt.Errorf("writing %s: %w", reconcile.Name, err))
	}
	return nil
}

func handoffPath(m *manifest.Manifest) string {
	for _, c := range m.Context {
		if strings.HasSuffix(strings.ToLower(c), "handoff.md") {
			return c
		}
	}
	if len(m.Context) > 0 {
		return m.Context[0]
	}
	return ""
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
