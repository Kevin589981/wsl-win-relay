package buildinfo

import (
	"fmt"
	"io"
)

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

func String(program string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", program, Version, Commit, BuiltAt)
}

// PrintRequested handles version inspection before command-specific argument
// and credential validation. It intentionally accepts only a standalone flag.
func PrintRequested(writer io.Writer, program string, args []string) bool {
	if len(args) != 1 || (args[0] != "-version" && args[0] != "--version") {
		return false
	}
	_, _ = fmt.Fprintln(writer, String(program))
	return true
}
