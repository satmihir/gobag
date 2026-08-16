package main

import "flag"

// parseInterspersed parses flags that may appear before, after, or between
// positional arguments, returning the positionals in order.
//
// Go's flag package stops at the first non-flag argument, so a plain Parse
// would silently ignore the flag in "gobag pack ./ws --plaintext" — producing
// an unencrypted archive for a user who asked for the opposite. Re-parsing
// after each positional is the standard remedy.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, wrapUser(err)
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
}
