package engine

import (
	"context"

	"github.com/skyneticist/depbisect/internal/verify"
)

// HarnessVerifier adapts verify.Harness to the engine's Verifier interface,
// letting the engine toggle early-stop behavior per phase.
type HarnessVerifier struct {
	Harness verify.Harness
}

// Verify implements Verifier.
func (h HarnessVerifier) Verify(ctx context.Context, dir string, stopOnPass bool) (verify.Verdict, error) {
	hh := h.Harness
	hh.StopOnPass = stopOnPass
	return hh.Verify(ctx, dir)
}
