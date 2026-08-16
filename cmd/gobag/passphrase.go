package main

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// passphraseEnv lets automation supply a passphrase without a terminal — the
// /checkpoint skill runs gobag from a session that has no TTY. The value is
// visible to other processes running as the same user, which is the documented
// trade for being scriptable at all.
const passphraseEnv = "GOBAG_PASSPHRASE"

// readPassphrase obtains a passphrase, preferring the environment and falling
// back to a terminal prompt. confirm asks twice and requires a match, which is
// right for pack (a typo would produce an archive nobody can open) and wrong
// for install (the archive already has whatever passphrase it has).
func readPassphrase(prompt string, confirm bool, stderr io.Writer) (string, error) {
	if v, ok := os.LookupEnv(passphraseEnv); ok {
		if v == "" {
			return "", errUser("%s is set but empty; unset it or give it a value", passphraseEnv)
		}
		return v, nil
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errUser("no terminal available for the passphrase prompt — set %s, or pass --plaintext to opt out of encryption", passphraseEnv)
	}

	fmt.Fprint(stderr, prompt)
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("reading passphrase: %w", err)
	}
	if len(first) == 0 {
		return "", errUser("passphrase was empty")
	}
	if !confirm {
		return string(first), nil
	}

	fmt.Fprint(stderr, "confirm passphrase: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("reading passphrase: %w", err)
	}
	if string(first) != string(second) {
		return "", errUser("passphrases did not match")
	}
	return string(first), nil
}
