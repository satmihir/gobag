package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/machine"
	"github.com/satmihir/gobag/internal/manifest"
)

// cmdLink points an external repository at a clone that already exists on this
// machine, then remembers the answer.
//
// This is the interactive half of installing a repository too large to travel.
// install stays headless and reports what it could not link; link is where a
// person supplies the one fact only they know — where the monorepo lives here —
// and never has to supply it again on this machine.
func cmdLink(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", "", "workspace root (default: current directory)")
	remember := fs.Bool("remember", true,
		"record this clone in the per-machine registry for future restores")
	fs.Usage = func() {
		io.WriteString(stderr, `usage: gobag link <dest> <path-to-clone> [-root DIR] [-remember=false]

Attaches an external repository to the workspace as a worktree of a clone you
already have, sharing its object store instead of cloning it again.

  gobag link repos/monorepo ~/src/monorepo

The location is remembered per machine (`+machine.FileEnv+` overrides the file),
so later restores link it without asking.
`)
	}
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return errUser("link needs a destination and a path, e.g. gobag link repos/monorepo ~/src/monorepo")
	}
	dest, clonePath := rest[0], rest[1]

	target, err := resolveRoot(*root)
	if err != nil {
		return err
	}
	clonePath, err = filepath.Abs(expandHome(clonePath))
	if err != nil {
		return wrapUser(fmt.Errorf("resolving %s: %w", rest[1], err))
	}

	m, err := loadInstalledManifest(target)
	if err != nil {
		return err
	}
	source, ok := findSource(m, dest)
	if !ok {
		return errUser("%s is not a repository in this workspace (installed from %s)", dest, target)
	}
	if !source.External {
		return errUser("%s is not an external repository — install clones it normally, "+
			"so there is nothing to link", dest)
	}

	// Verify before writing anything: pointing gobag at the wrong repository
	// would otherwise succeed and quietly produce a workspace built on it.
	if err := gitops.LocateExternal(clonePath, source); err != nil {
		return errUser("%v", err)
	}

	res, err := gitops.EnsureExternal(target, source, clonePath)
	if err != nil {
		return wrapUser(err)
	}
	fmt.Fprintf(stdout, "%s: %s\n", res.Dest, res.Outcome)
	if res.Detail != "" {
		fmt.Fprintf(stdout, "  %s\n", res.Detail)
	}

	if *remember {
		registry, err := machine.Load()
		if err != nil {
			return wrapUser(err)
		}
		registry.Set(source.Remote, clonePath)
		if err := registry.Save(); err != nil {
			return wrapUser(err)
		}
		fmt.Fprintf(stdout, "  remembered in %s — future restores will link it without asking\n", machine.Path())
	}

	fmt.Fprintf(stdout, "\nTo undo: git -C %s worktree remove %s\n",
		clonePath, filepath.Join(target, filepath.FromSlash(dest)))
	return nil
}

// loadInstalledManifest reads the manifest install left in the workspace.
func loadInstalledManifest(root string) (*manifest.Manifest, error) {
	f, err := os.Open(installedManifestPath(root))
	if os.IsNotExist(err) {
		return nil, errUser("%s does not look like a restored workspace — no %s found. "+
			"Run gobag install first, or pass -root", root, filepath.Join(".gobag", manifest.Name))
	}
	if err != nil {
		return nil, wrapUser(err)
	}
	defer f.Close()
	return manifest.Decode(f)
}

func findSource(m *manifest.Manifest, dest string) (manifest.Source, bool) {
	dest = strings.TrimSuffix(filepath.ToSlash(dest), "/")
	for _, s := range m.Sources {
		if s.Dest == dest {
			return s, true
		}
	}
	return manifest.Source{}, false
}

// expandHome resolves a leading ~ so a path typed by a person works whether or
// not their shell expanded it.
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
