package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/satmihir/gobag/internal/archive"
)

func cmdVerify(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		io.WriteString(stderr, "usage: gobag verify <archive>\n")
	}
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errUser("verify needs exactly one archive path")
	}

	f, passphrase, err := openArchive(rest[0], stderr)
	if err != nil {
		return err
	}
	defer f.Close()

	results, err := archive.Verify(f, passphrase)
	if err != nil {
		return archiveErr(err)
	}

	var bad int
	for _, r := range results {
		if r.OK {
			continue
		}
		bad++
		if r.Note != "" {
			fmt.Fprintf(stdout, "  %s: %s\n", r.Path, r.Note)
			continue
		}
		fmt.Fprintf(stdout, "  %s: checksum mismatch (want %s, got %s)\n",
			r.Path, shortSum(r.Want), shortSum(r.Got))
	}

	if bad > 0 {
		return errUser("%d of %d entries failed verification", bad, len(results))
	}
	fmt.Fprintf(stdout, "%d entries verified — archive intact\n", len(results))
	return nil
}

func shortSum(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
