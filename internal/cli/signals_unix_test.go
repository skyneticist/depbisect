//go:build !windows

package cli

import (
	"syscall"
	"testing"
)

func TestTerminationSignalsIncludeSIGTERMOnUnix(t *testing.T) {
	for _, sig := range terminationSignals() {
		if sig == syscall.SIGTERM {
			return
		}
	}
	t.Fatal("termination signals do not include SIGTERM")
}
