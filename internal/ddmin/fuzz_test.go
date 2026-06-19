package ddmin

import (
	"reflect"
	"sort"
	"testing"
)

// culpritsFromBytes derives a deterministic, deduplicated, sorted set of indices
// in [0, n) from raw fuzz bytes. The result is non-empty whenever n > 0, which
// keeps the conjunctive predicate below well-defined (an empty culprit set would
// make every subset fail and break 1-minimality).
func culpritsFromBytes(b []byte, n int) []int {
	if n <= 0 {
		return nil
	}
	seen := make(map[int]bool)
	for _, v := range b {
		seen[int(v)%n] = true
	}
	if len(seen) == 0 {
		seen[0] = true
	}
	out := make([]int, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// FuzzMinimize drives ddmin with a conjunctive predicate: the failure
// reproduces only when every culprit is present. Whatever inputs the fuzzer
// supplies, the minimized result must reproduce the failure, be 1-minimal,
// preserve input order, and — because this predicate has a unique minimal
// witness — equal the culprit set exactly.
func FuzzMinimize(f *testing.F) {
	f.Add(10, []byte{0x03})
	f.Add(20, []byte{0xff, 0x01})
	f.Add(1, []byte{0x00})
	f.Add(64, []byte{0x05, 0x3f, 0x10})
	f.Add(0, []byte(nil))

	f.Fuzz(func(t *testing.T, rawN int, sel []byte) {
		// Map the fuzzer's int onto a small, positive bound so each 1-minimality
		// check (O(n) predicate calls) stays cheap.
		n := rawN % 65
		if n < 0 {
			n = -n
		}
		if n == 0 {
			n = 1
		}
		culprits := culpritsFromBytes(sel, n)
		test := conjunctiveTest(culprits)

		got, _, err := Minimize(ints(n), test)
		if err != nil {
			t.Fatalf("n=%d culprits=%v: %v", n, culprits, err)
		}

		// The result reproduces the failure.
		if out, _ := test(got); out != Fail {
			t.Fatalf("n=%d culprits=%v: result %v does not reproduce", n, culprits, got)
		}
		// Removing any single element stops the failure (1-minimal).
		for i := range got {
			smaller := append(append([]int(nil), got[:i]...), got[i+1:]...)
			if out, _ := test(smaller); out == Fail {
				t.Fatalf("n=%d culprits=%v: result %v not 1-minimal (dropping %d still fails)",
					n, culprits, got, got[i])
			}
		}
		// Input order is preserved.
		if !sort.IntsAreSorted(got) {
			t.Fatalf("n=%d: result %v not in input order", n, got)
		}
		// For a conjunctive predicate the unique minimal set is the culprits.
		want := append([]int(nil), culprits...)
		sort.Ints(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("n=%d culprits=%v: got %v, want %v", n, culprits, got, want)
		}
	})
}
