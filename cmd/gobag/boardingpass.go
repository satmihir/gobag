package main

import (
	"fmt"
	"strings"
)

// The boarding pass is the last thing pack prints and the only instructions
// that travel with the human. It lives in one place and is golden-tested,
// because a stale command here strands someone on the other machine.
const (
	boardingPassFormat = `
Packed: %s (%s)

To restore — in Claude Code on the target machine:
  /plugin marketplace add satmihir/gobag
  /plugin install gobag@gobag
  /restore %s

Without a session (CI, provisioning, a dying instance):
  curl -fsSL https://raw.githubusercontent.com/satmihir/gobag/main/scripts/install.sh | sh
  gobag install %s
`
	plaintextWarning = `
This archive is NOT encrypted. Anyone who obtains the file can read the
handoff, the memory, and anything else inside it.
`
)

func boardingPass(archivePath, size string, plaintext bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, boardingPassFormat, archivePath, size, archivePath, archivePath)
	if plaintext {
		b.WriteString(plaintextWarning)
	}
	return b.String()
}
