// Command kuba is the magical kubectl companion.
package main

import (
	"fmt"
	"os"
)

// version is set by release builds via -ldflags "-X main.version=..."; everything else reports dev.
var version = "dev"

func main() {
	fmt.Fprintf(os.Stderr, "kuba %s: the Go port has no commands yet\n", version)
	os.Exit(2)
}
