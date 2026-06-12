// Command depbisect finds the smallest set of dependency updates between two
// Git revisions that makes a verification command fail.
package main

import (
	"os"

	"github.com/skyneticist/depbisect/internal/cli"
)

// version is overridden at release time via:
//
//	go build -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr, version))
}
