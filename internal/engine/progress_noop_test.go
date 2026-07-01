package engine

import (
	"testing"
	"time"
)

// TestNopProgressIsNoop exercises the default Progress sink the engine installs
// when a caller passes none (engine.go: `progress = nopProgress{}`). The methods
// must accept the full Progress signatures and do nothing — never panic — so a
// run without a Progress observer behaves exactly like one with a silent sink.
func TestNopProgressIsNoop(t *testing.T) {
	var p Progress = nopProgress{}
	p.Step("install", "installing %s", "leftpad")
	p.Detail("resolved %d candidates", 3)
	p.Trial(1, "candidate", 2, 5, "verify", 250*time.Millisecond)
}
