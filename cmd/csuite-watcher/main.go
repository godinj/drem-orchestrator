// Command csuite-watcher publishes events to the C-Suite event bus.
//
// Usage:
//
//	csuite-watcher <command> [flags]
//
// Commands:
//
//	event    Publish an event to the event bus from a JSON argument
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: csuite-watcher <command> [flags]")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "not implemented")
	os.Exit(1)
}
