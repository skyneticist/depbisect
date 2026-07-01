package ddmin

import "testing"

// TestOutcomeString pins the stable lower-case names used in reports and logs,
// including the defensive default arm for an out-of-range value.
func TestOutcomeString(t *testing.T) {
	cases := []struct {
		o    Outcome
		want string
	}{
		{Pass, "pass"},
		{Fail, "fail"},
		{Unresolved, "unresolved"},
		{Outcome(99), "outcome(99)"},
	}
	for _, tc := range cases {
		if got := tc.o.String(); got != tc.want {
			t.Errorf("Outcome(%d).String() = %q, want %q", int(tc.o), got, tc.want)
		}
	}
}
