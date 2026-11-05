package cli

import (
	"flag"
	"io"
)

// newFlags returns a FlagSet that reports errors instead of exiting.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("tmx "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse parses flags anywhere among the arguments (`tmx run NAME --project P` as well as
// `tmx run --project P NAME`) and returns the positional ones. "--" ends flag parsing.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageErr("%s: %v", fs.Name(), err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if len(args) > 0 && len(rest) < len(args) && args[len(args)-len(rest)-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}
