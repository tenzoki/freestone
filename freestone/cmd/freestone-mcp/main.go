// freestone-mcp is a personal MCP server for calendar, mail, and files.
//
// Usage:
//
//	freestone-mcp [serve]             — run the MCP server (default)
//	freestone-mcp auth google         — bootstrap Google OAuth2 credentials
package main

import (
	"fmt"
	"os"
)

func main() {
	subcmd := "serve"
	if len(os.Args) > 1 {
		subcmd = os.Args[1]
	}

	var err error
	switch subcmd {
	case "serve":
		// When "serve" is explicit, skip it so the flag set only sees flags.
		// When implicit (no subcommand), os.Args[1:] has no "serve" prefix.
		args := os.Args[1:]
		if len(os.Args) > 1 && os.Args[1] == "serve" {
			args = os.Args[2:]
		}
		err = runServe(args)
	case "auth":
		err = runAuth(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand: %s (expected serve or auth)", subcmd)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "freestone-mcp: %v\n", err)
		os.Exit(1)
	}
}
