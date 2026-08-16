// Command gobag packs a Claude Code workspace into a single portable
// archive and restores it elsewhere.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const usage = `gobag — pack a Claude Code workspace into a portable archive.

usage:
  gobag pack    --plan plan.json [-o out.gobag] [--plaintext] [--transcripts]
  gobag pack    <root>           [-o out.gobag] [--plaintext] [--transcripts]
  gobag install <archive> [--root DIR]
  gobag inspect <archive>
  gobag verify  <archive>

run "gobag <command> -h" for command flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}

	var err error
	switch args[0] {
	case "pack":
		err = cmdPack(args[1:], stdout, stderr)
	case "install":
		err = cmdInstall(args[1:], stdout, stderr)
	case "inspect":
		err = cmdInspect(args[1:], stdout, stderr)
	case "verify":
		err = cmdVerify(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version":
		fmt.Fprintln(stdout, versionString())
		return 0
	default:
		fmt.Fprintf(stderr, "gobag: unknown command %q\n\n%s", args[0], usage)
		return 1
	}

	if err != nil {
		fmt.Fprintf(stderr, "gobag: %v\n", err)
		return exitCode(err)
	}
	return 0
}

// userErr marks a failure caused by user input or the environment (exit 1)
// rather than a defect in gobag itself (exit 2).
type userErr struct{ err error }

func (e userErr) Error() string { return e.err.Error() }
func (e userErr) Unwrap() error { return e.err }

// errUser builds a user-facing error. Use it for anything the user can fix:
// a missing file, an unreachable remote, a wrong passphrase.
func errUser(format string, a ...any) error {
	return userErr{fmt.Errorf(format, a...)}
}

// wrapUser marks an existing error as user-caused, preserving its chain.
func wrapUser(err error) error {
	if err == nil {
		return nil
	}
	return userErr{err}
}

func exitCode(err error) int {
	var ue userErr
	if errors.As(err, &ue) {
		return 1
	}
	return 2
}
