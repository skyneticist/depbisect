package cli

import (
	"os"
	"testing"
)

func TestTerminationSignalsIncludeInterrupt(t *testing.T) {
	for _, sig := range terminationSignals() {
		if sig == os.Interrupt {
			return
		}
	}
	t.Fatal("termination signals do not include os.Interrupt")
}
