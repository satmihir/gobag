package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/satmihir/gobag/internal/archive"
	"github.com/satmihir/gobag/internal/manifest"
)

func cmdInspect(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	files := fs.Bool("files", false, "list every entry, not just the summary")
	fs.Usage = func() {
		io.WriteString(stderr, "usage: gobag inspect [-files] <archive>\n")
	}
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errUser("inspect needs exactly one archive path")
	}

	f, passphrase, err := openArchive(rest[0], stderr)
	if err != nil {
		return err
	}
	defer f.Close()

	c, err := archive.Inspect(f, passphrase)
	if err != nil {
		return archiveErr(err)
	}
	if c.Manifest == nil {
		return errUser("archive has no %s — it may not be a gobag archive", manifest.Name)
	}

	printManifest(stdout, c.Manifest)

	var total int64
	for _, e := range c.Entries {
		total += e.Size
	}
	fmt.Fprintf(stdout, "\n%d entries, %s uncompressed\n", len(c.Entries), humanSize(total))

	if *files {
		fmt.Fprintln(stdout)
		for _, e := range c.Entries {
			fmt.Fprintf(stdout, "  %10s  %s\n", humanSize(e.Size), e.Path)
		}
	}
	return nil
}

func printManifest(w io.Writer, m *manifest.Manifest) {
	fmt.Fprintf(w, "%s — packed %s by gobag %s\n", m.Name, m.Created, m.GobagVersion)
	if m.SourceRoot != "" {
		fmt.Fprintf(w, "packed from %s\n", m.SourceRoot)
	}

	if len(m.Sources) > 0 {
		fmt.Fprintln(w, "\nsources:")
		for _, s := range m.Sources {
			fmt.Fprintf(w, "  %s\n    %s @ %s%s\n", s.Dest, s.Remote, shortRef(s.Ref), branchNote(s.Branch))
			for _, wt := range s.Worktrees {
				fmt.Fprintf(w, "    worktree %s @ %s%s\n", wt.Dest, shortRef(wt.Ref), branchNote(wt.Branch))
			}
		}
	}

	section(w, "context", m.Context)
	section(w, "skills", m.Skills)
	section(w, "memory", m.Memory)
	section(w, "transcripts", m.Transcripts)
	if m.MCP != "" {
		section(w, "mcp", []string{m.MCP})
	}
}

func section(w io.Writer, name string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s:\n", name)
	for _, it := range items {
		fmt.Fprintf(w, "  %s\n", it)
	}
}

func shortRef(ref string) string {
	if len(ref) > 12 {
		return ref[:12]
	}
	return ref
}

func branchNote(branch string) string {
	if branch == "" {
		return " (detached)"
	}
	return " (" + branch + ")"
}
