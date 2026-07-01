package verify

import "testing"

// TestVerdictAllRunsTimedOut covers the helper the engine uses to distinguish a
// baseline that fails purely because the command timed out (a too-short
// --run-timeout) from one that fails on its own merits.
func TestVerdictAllRunsTimedOut(t *testing.T) {
	cases := []struct {
		name string
		v    Verdict
		want bool
	}{
		{"no runs", Verdict{}, false},
		{"all timed out", Verdict{Runs: []RunResult{{TimedOut: true}, {TimedOut: true}}, Failures: 2}, true},
		{"timeout mixed with exit-code failure", Verdict{Runs: []RunResult{{TimedOut: true}, {ExitCode: 1}}, Failures: 2}, false},
		{"all passed", Verdict{Runs: []RunResult{{ExitCode: 0}}, Failures: 0}, false},
	}
	for _, tc := range cases {
		if got := tc.v.AllRunsTimedOut(); got != tc.want {
			t.Errorf("%s: AllRunsTimedOut() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
