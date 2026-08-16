package main

import "runtime/debug"

// version is overwritten at release time via -ldflags "-X main.version=v0.1.0".
var version = "dev"

func versionString() string {
	if version != "dev" {
		return "gobag " + version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return "gobag dev (" + s.Value[:7] + ")"
			}
		}
	}
	return "gobag dev"
}
