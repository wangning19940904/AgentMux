// Command amux is the unified entrypoint: it runs the daemon, serves
// the WebUI, reports token usage, and manages providers. CLI / WebUI / desktop
// all share the same Go core.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
