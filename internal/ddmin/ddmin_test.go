package ddmin

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// conjunctiveTest fails iff the subset contains every culprit.
func conjunctiveTest(culprits []int) Test[int] {
	return func(subset []int) (Outcome, error) {
		have := make(map[int]bool, len(subset))
		for _, s := range subset {
			have[s] = true
		}
		for _, c := range culprits {
			if !have[c] {
				return Pass, nil
			}
		}
		return Fail, nil
	}
}

// disjunctiveTest fails iff the subset contains at least one culprit.
func disjunctiveTest(culprits []int) Test[int] {
	return func(subset []int) (Outcome, error) {
		have := make(map[int]bool, len(subset))
		for _, s := range subset {
			have[s] = true
		}
		for _, c := range culprits {
			if have[c] {
				return Fail, nil
			}
		}
		return Pass, nil
	}
}

func ints(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func TestMinimizeConjunctive(t *testing.T) {
	cases := []struct {
		name     string
		n        int
		culprits []int
	}{
		{"single culprit of one", 1, []int{0}},
		{"single culprit", 10, []int{3}},
		{"single culprit last", 7, []int{6}},
		{"pair", 10, []int{2, 7}},
		{"triple", 20, []int{0, 10, 19}},
		{"all are culprits", 4, []int{0, 1, 2, 3}},
		{"large single", 100, []int{42}},
		{"large pair", 64, []int{5, 63}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, stats, err := Minimize(ints(tc.n), conjunctiveTest(tc.culprits))
			if err != nil {
				t.Fatalf("Minimize: %v", err)
			}
			want := append([]int(nil), tc.culprits...)
			sort.Ints(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
			if stats.Tests <= 0 && tc.n > 1 {
				t.Errorf("expected at least one test invocation, got %d", stats.Tests)
			}
		})
	}
}

func TestMinimizeDisjunctive(t *testing.T) {
	// Any one of several culprits suffices; result must be exactly one of them.
	culprits := []int{2, 5, 8}
	got, _, err := Minimize(ints(10), disjunctiveTest(culprits))
	if err != nil {
		t.Fatalf("Minimize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected singleton result, got %v", got)
	}
	found := false
	for _, c := range culprits {
		if got[0] == c {
			found = true
		}
	}
	if !found {
		t.Errorf("result %v is not a culprit (%v)", got, culprits)
	}
}

func TestMinimizeDeterministic(t *testing.T) {
	first, firstStats, err := Minimize(ints(30), disjunctiveTest([]int{4, 17, 23}))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, stats, err := Minimize(ints(30), disjunctiveTest([]int{4, 17, 23}))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, first) || stats.Tests != firstStats.Tests {
			t.Fatalf("run %d differs: got %v (%d tests), want %v (%d tests)",
				i, got, stats.Tests, first, firstStats.Tests)
		}
	}
}

func TestMinimizePropertyRandomConjunctive(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 200; trial++ {
		n := 1 + rng.Intn(40)
		k := 1 + rng.Intn(n)
		perm := rng.Perm(n)
		culprits := append([]int(nil), perm[:k]...)
		test := conjunctiveTest(culprits)

		got, _, err := Minimize(ints(n), test)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		// Invariant: result reproduces the failure.
		if out, _ := test(got); out != Fail {
			t.Fatalf("trial %d: result %v does not fail", trial, got)
		}
		// Invariant: result is 1-minimal.
		for i := range got {
			smaller := append(append([]int(nil), got[:i]...), got[i+1:]...)
			if out, _ := test(smaller); out == Fail {
				t.Fatalf("trial %d: result %v not 1-minimal (removing %d still fails)", trial, got, got[i])
			}
		}
		// Invariant: result preserves the original element order.
		if !sort.IntsAreSorted(got) {
			t.Fatalf("trial %d: result %v not in input order", trial, got)
		}
	}
}

// fullBatch adapts a single-subset Test into a BatchTest that mimics a
// parallel executor: it evaluates every subset in the batch (no early break)
// and returns the lowest-indexed failure. Its result must match the sequential
// executor's, even though it evaluates more subsets.
func fullBatch[T any](test Test[T]) BatchTest[T] {
	return func(subsets [][]T) (int, int, error) {
		sel := -1
		for i, subset := range subsets {
			out, err := test(subset)
			if err != nil {
				return -1, len(subsets), err
			}
			if out == Fail && sel == -1 {
				sel = i
			}
		}
		return sel, len(subsets), nil
	}
}

// TestMinimizeBatchMatchesSequential pins the core parallel-safety invariant:
// evaluating a whole granularity level (as a parallel pool does) yields the
// same minimized set as the sequential, early-breaking executor.
func TestMinimizeBatchMatchesSequential(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 300; trial++ {
		n := 1 + rng.Intn(40)
		k := 1 + rng.Intn(n)
		culprits := append([]int(nil), rng.Perm(n)[:k]...)

		seq, _, err := Minimize(ints(n), conjunctiveTest(culprits))
		if err != nil {
			t.Fatalf("trial %d: sequential: %v", trial, err)
		}
		par, _, err := MinimizeBatch(ints(n), fullBatch(conjunctiveTest(culprits)))
		if err != nil {
			t.Fatalf("trial %d: batch: %v", trial, err)
		}
		if !reflect.DeepEqual(seq, par) {
			t.Fatalf("trial %d culprits=%v: sequential %v != batch %v", trial, culprits, seq, par)
		}
	}
}

func TestMinimizeUnresolvedTreatedAsNotFailing(t *testing.T) {
	// Subsets of size < 3 are unresolved; failure needs culprits {1, 2}.
	test := func(subset []int) (Outcome, error) {
		if len(subset) < 3 {
			return Unresolved, nil
		}
		return conjunctiveTest([]int{1, 2})(subset)
	}
	got, _, err := Minimize(ints(6), test)
	if err != nil {
		t.Fatal(err)
	}
	// The minimum *resolvable* failing set has size 3; it must contain the culprits.
	if out, _ := test(got); out != Fail {
		t.Fatalf("result %v does not fail", got)
	}
	have := map[int]bool{}
	for _, g := range got {
		have[g] = true
	}
	if !have[1] || !have[2] {
		t.Errorf("result %v missing culprits 1, 2", got)
	}
}

func TestMinimizeErrorAborts(t *testing.T) {
	boom := errors.New("boom")
	calls := 0
	_, _, err := Minimize(ints(8), func(subset []int) (Outcome, error) {
		calls++
		if calls == 3 {
			return Unresolved, boom
		}
		return Pass, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected wrapped boom error, got %v", err)
	}
}

func TestMinimizeEmptyAndSingle(t *testing.T) {
	got, stats, err := Minimize(nil, conjunctiveTest(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || stats.Tests != 0 {
		t.Errorf("empty input: got %v, %d tests", got, stats.Tests)
	}

	got, _, err = Minimize([]int{7}, func(s []int) (Outcome, error) { return Fail, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{7}) {
		t.Errorf("single input: got %v", got)
	}
}

func TestMinimizeTerminationBound(t *testing.T) {
	// Even with an adversarial test that never fails for proper subsets,
	// the number of invocations must stay polynomial (~n^2 + 3n for ddmin).
	n := 60
	calls := 0
	test := func(subset []int) (Outcome, error) {
		calls++
		if calls > n*n+10*n {
			return Pass, fmt.Errorf("too many test invocations: %d", calls)
		}
		if len(subset) == n {
			return Fail, nil
		}
		return Pass, nil
	}
	got, _, err := Minimize(ints(n), test)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Errorf("expected irreducible input returned whole, got %d elements", len(got))
	}
}
