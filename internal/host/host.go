// Package host identifies the machine an archive was packed on.
//
// It exists because of a specific, dangerous failure: a bag packed in an
// ephemeral devcontainer was restored onto a different machine whose layout
// was deliberately uniform, so same-named clones sat at the same paths. Every
// available signal — matching paths, matching repository names, a pack only
// minutes old — pointed at "same machine, stale bag", and the restored agent
// confidently concluded exactly that. Nothing in the archive could contradict
// it, because nothing in the archive said which machine it came from.
//
// Identity is recorded as a hash rather than the raw machine id: the archive
// only ever needs to answer "same host or not", and a stable hardware
// identifier is not something to ship in a file that may be handed to someone
// else.
package host

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Info is what an archive records about where it was packed.
type Info struct {
	// Name is the hostname, for a human to recognize. Empty when unknown.
	Name string `json:"name,omitempty"`
	// ID is a truncated hash of a stable machine identifier. Empty when this
	// platform offers nothing stable, in which case comparison is inconclusive
	// and must be reported that way rather than guessed.
	ID string `json:"id,omitempty"`
	// Container reports that the pack happened inside a container, where a
	// hostname is typically random and a machine id may be inherited.
	Container bool `json:"container,omitempty"`
}

// Comparison is the verdict when an archive lands somewhere.
type Comparison int

const (
	// Unknown means at least one side lacks a stable identifier. The honest
	// answer, and the one that must be printed rather than smoothed over.
	Unknown Comparison = iota
	// SameHost means both sides carry the same stable identifier.
	SameHost
	// DifferentHost means both sides carry identifiers and they differ.
	DifferentHost
)

// Current reads this machine's identity.
func Current() Info {
	info := Info{Container: inContainer()}
	if name, err := os.Hostname(); err == nil {
		info.Name = name
	}
	if raw := machineID(); raw != "" {
		sum := sha256.Sum256([]byte(raw))
		info.ID = hex.EncodeToString(sum[:])[:16]
	}
	return info
}

// Compare reports whether two hosts are the same machine.
func Compare(packed, current Info) Comparison {
	if packed.ID == "" || current.ID == "" {
		return Unknown
	}
	if packed.ID == current.ID {
		return SameHost
	}
	return DifferentHost
}

// Describe renders a host for a sentence a person or an agent will read.
func (i Info) Describe() string {
	switch {
	case i.Name != "" && i.Container:
		return i.Name + " (a container)"
	case i.Name != "":
		return i.Name
	case i.ID != "":
		return "an unnamed host (" + i.ID + ")"
	default:
		return "an unidentified host"
	}
}

// machineID returns a stable per-machine identifier, or "" when the platform
// offers none. Never fabricated: an unknown identity must stay unknown so the
// comparison can say so.
func machineID() string {
	if runtime.GOOS == "darwin" {
		return darwinPlatformUUID()
	}
	for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}

// darwinPlatformUUID reads the hardware UUID macOS exposes through ioreg.
func darwinPlatformUUID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "IOPlatformUUID") {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

// inContainer reports whether this process is running inside a container.
// Best effort: a false negative costs a nuance in one sentence, never a wrong
// same-or-different verdict, which rests on the identifier alone.
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		for _, marker := range []string{"docker", "containerd", "kubepods", "lxc"} {
			if strings.Contains(s, marker) {
				return true
			}
		}
	}
	return false
}
