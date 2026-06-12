//go:build windows

package cli

import "testing"

func TestTerminationSignalsAreSupportedOnWindows(t *testing.T) {
	if got := len(terminationSignals()); got != 1 {
		t.Fatalf("termination signal count = %d, want 1 (os.Interrupt)", got)
	}
}
