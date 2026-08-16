package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/satmihir/gobag/internal/archive"
	"github.com/satmihir/gobag/internal/gitops"
	"github.com/satmihir/gobag/internal/manifest"
	"github.com/satmihir/gobag/internal/plan"
	"github.com/satmihir/gobag/internal/scan"
)

type packOptions struct {
	planPath    string
	root        string
	out         string
	name        string
	plaintext   bool
	transcripts bool
}

func parsePackArgs(args []string, stderr io.Writer) (packOptions, error) {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		io.WriteString(stderr, `usage: gobag pack --plan plan.json [flags]
       gobag pack <root> [flags]

flags:
  -plan FILE      pack from a plan produced by /checkpoint
  -o FILE         output archive (default: <name>-<timestamp>.gobag)
  -name NAME      archive name (default: from the plan, or the root's basename)
  -plaintext      do not encrypt (default: encrypt with a passphrase)
  -transcripts    include session transcripts (default: omitted)
`)
	}

	var opts packOptions
	fs.StringVar(&opts.planPath, "plan", "", "path to plan.json")
	fs.StringVar(&opts.out, "o", "", "output archive path")
	fs.StringVar(&opts.name, "name", "", "archive name")
	fs.BoolVar(&opts.plaintext, "plaintext", false, "do not encrypt")
	fs.BoolVar(&opts.transcripts, "transcripts", false, "include session transcripts")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return opts, err
	}

	switch {
	case opts.planPath != "" && len(rest) > 0:
		return opts, errUser("pack takes either --plan or a root directory, not both")
	case opts.planPath == "" && len(rest) == 0:
		return opts, errUser("pack needs --plan plan.json or a root directory")
	case len(rest) > 1:
		return opts, errUser("pack takes a single root directory")
	case len(rest) == 1:
		opts.root = rest[0]
	}
	return opts, nil
}

func cmdPack(args []string, stdout, stderr io.Writer) error {
	opts, err := parsePackArgs(args, stderr)
	if err != nil {
		return err
	}

	p, sourceRoot, err := loadPlan(opts, stdout)
	if err != nil {
		return err
	}
	if opts.name != "" {
		p.Name = opts.name
	}

	// Structural checks first (cheap, no network), then the git-dependent ones.
	// A plan may be agent-authored, so nothing here is taken on trust.
	problems := append(plan.Validate(p), gitops.VerifyPlan(p)...)
	plan.SortProblems(problems)
	errs, warns := plan.Problems(problems)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(stderr, "  %s\n", problemLine(e))
		}
		return errUser("plan has %d problem(s) that must be fixed before packing", len(errs))
	}

	files, err := expandEntries(p.Entries(opts.transcripts))
	if err != nil {
		return err
	}

	printSummary(stdout, p, files, warns, opts)
	if err := reportSecrets(stdout, files); err != nil {
		return err
	}

	created := time.Now()
	outPath := opts.out
	if outPath == "" {
		outPath = fmt.Sprintf("%s-%s.gobag", p.Name, created.Format("20060102-1504"))
	}

	var passphrase string
	if !opts.plaintext {
		passphrase, err = readPassphrase("passphrase (encrypts the archive): ", true, stderr)
		if err != nil {
			return err
		}
	}

	size, err := writeArchive(outPath, p, files, sourceRoot, created, passphrase, opts.transcripts)
	if err != nil {
		return err
	}

	absOut, err := filepath.Abs(outPath)
	if err != nil {
		absOut = outPath
	}
	fmt.Fprint(stdout, boardingPass(absOut, humanSize(size), opts.plaintext))
	return nil
}

// loadPlan returns the plan plus the workspace root it came from. Walk mode
// discovers a plan; --plan trusts the file's structure but nothing in it.
func loadPlan(opts packOptions, stdout io.Writer) (*plan.Plan, string, error) {
	if opts.planPath != "" {
		p, err := plan.Load(opts.planPath)
		if err != nil {
			return nil, "", wrapUser(err)
		}
		// Without a source root the archive cannot say where "here" was, so
		// memory path-rewriting on the other side is silently lost.
		if p.SourceRoot == "" {
			fmt.Fprintln(stdout, "note: plan has no source_root — memory paths will not be rewritten on restore")
		}
		return p, p.SourceRoot, nil
	}

	root, err := filepath.Abs(opts.root)
	if err != nil {
		return nil, "", wrapUser(fmt.Errorf("resolving %s: %w", opts.root, err))
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, "", errUser("%s is not a directory", opts.root)
	}

	fmt.Fprintf(stdout, "walking %s\n", root)
	p, problems, err := gitops.Discover(root)
	if err != nil {
		return nil, "", err
	}
	errs, _ := plan.Problems(problems)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(stdout, "  %s\n", problemLine(e))
		}
		return nil, "", errUser("discovery found %d problem(s) in %s", len(errs), root)
	}
	return p, root, nil
}

// packFile is one concrete file destined for the archive.
type packFile struct {
	src  string
	dest string
	mode fs.FileMode
	size int64
}

// expandEntries resolves plan entries into individual files, walking any that
// name a directory. Results are sorted so archives are reproducible for
// identical inputs.
func expandEntries(entries []plan.Entry) ([]packFile, error) {
	var out []packFile
	seen := map[string]string{}

	for _, e := range entries {
		if err := plan.ValidateDest(e.Dest); err != nil {
			return nil, wrapUser(err)
		}
		info, err := os.Lstat(e.Path)
		if err != nil {
			return nil, wrapUser(fmt.Errorf("reading %s: %w", e.Path, err))
		}

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			// v1 stores content, not link topology. Skipping is safer than
			// silently materializing whatever the link points at.
			continue
		case info.IsDir():
			err = filepath.WalkDir(e.Path, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || !d.Type().IsRegular() {
					return nil
				}
				rel, err := filepath.Rel(e.Path, p)
				if err != nil {
					return err
				}
				fi, err := d.Info()
				if err != nil {
					return err
				}
				out = append(out, packFile{
					src:  p,
					dest: path.Join(e.Dest, filepath.ToSlash(rel)),
					mode: fi.Mode().Perm(),
					size: fi.Size(),
				})
				return nil
			})
			if err != nil {
				return nil, wrapUser(fmt.Errorf("walking %s: %w", e.Path, err))
			}
		default:
			out = append(out, packFile{src: e.Path, dest: e.Dest, mode: info.Mode().Perm(), size: info.Size()})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].dest < out[j].dest })
	for _, f := range out {
		if prev, dup := seen[f.dest]; dup {
			return nil, errUser("two files map to %s (%s and %s)", f.dest, prev, f.src)
		}
		seen[f.dest] = f.src
	}
	return out, nil
}

func writeArchive(outPath string, p *plan.Plan, files []packFile, sourceRoot string,
	created time.Time, passphrase string, withTranscripts bool) (int64, error) {

	f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return 0, errUser("%s already exists — choose another name with -o", outPath)
		}
		return 0, wrapUser(fmt.Errorf("creating %s: %w", outPath, err))
	}
	// A failed pack must not leave a truncated archive that looks usable.
	success := false
	defer func() {
		f.Close()
		if !success {
			os.Remove(outPath)
		}
	}()

	w, err := archive.NewWriter(f, passphrase)
	if err != nil {
		return 0, err
	}

	m := manifest.FromPlan(p, versionString(), sourceRoot, created, withTranscripts)
	var mb strings.Builder
	if err := m.Encode(&mb); err != nil {
		return 0, err
	}
	if err := w.AddBytes(manifest.Name, 0o644, []byte(mb.String())); err != nil {
		return 0, err
	}

	for _, pf := range files {
		src, err := os.Open(pf.src)
		if err != nil {
			return 0, wrapUser(fmt.Errorf("opening %s: %w", pf.src, err))
		}
		err = w.AddFile(pf.dest, pf.mode, pf.size, src)
		src.Close()
		if err != nil {
			return 0, err
		}
	}

	if err := w.Close(); err != nil {
		return 0, err
	}
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	success = true
	return info.Size(), nil
}

func printSummary(w io.Writer, p *plan.Plan, files []packFile, warns []plan.Problem, opts packOptions) {
	fmt.Fprintf(w, "\npacking %q — %s, %s\n", p.Name,
		plural(len(p.Sources), "source"), plural(len(files), "file"))

	if len(p.Sources) > 0 {
		fmt.Fprintln(w, "\nsources:")
		for _, s := range p.Sources {
			fmt.Fprintf(w, "  %s\n    %s @ %s%s\n", s.Dest, s.Remote, shortRef(s.Ref), branchNote(s.Branch))
			for _, wt := range s.Worktrees {
				fmt.Fprintf(w, "    worktree %s @ %s%s\n", wt.Dest, shortRef(wt.Ref), branchNote(wt.Branch))
			}
		}
	}

	if n := len(p.State.Transcripts); n > 0 && !opts.transcripts {
		fmt.Fprintf(w, "\nskipping %d session transcript(s) — pass --transcripts to include them\n", n)
	}

	if len(warns) > 0 {
		fmt.Fprintf(w, "\n%d warning(s):\n", len(warns))
		for _, warn := range warns {
			fmt.Fprintf(w, "  %s\n", problemLine(warn))
		}
	}
}

// reportSecrets scans everything about to be sealed, including documents the
// agent wrote. It warns and continues: blocking a checkpoint over a regex hit
// would strand the user's work on a machine that is about to disappear.
func reportSecrets(w io.Writer, files []packFile) error {
	var findings []scan.Finding
	for _, f := range files {
		found, err := scan.File(f.src)
		if err != nil {
			return wrapUser(err)
		}
		for i := range found {
			found[i].Path = f.dest
		}
		findings = append(findings, found...)
	}
	if len(findings) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	return scan.Format(findings, w)
}

func problemLine(p plan.Problem) string {
	if p.Dest == "" {
		return p.Message
	}
	return p.Dest + ": " + p.Message
}
