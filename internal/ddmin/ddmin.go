// Package ddmin implements Zeller's ddmin delta-debugging algorithm.
//
// The implementation is deterministic and free of I/O: callers supply a Test
// function and receive a reduced failing subset. When every required
// neighboring test resolves, the result is 1-minimal. All candidate subsets
// preserve the relative order of the input slice.
package ddmin

import "fmt"

// Outcome is the result of testing one candidate subset.
type Outcome int

const (
	// Pass means the failure was NOT reproduced with this subset.
	Pass Outcome = iota
	// Fail means the failure WAS reproduced with this subset.
	Fail
	// Unresolved means the test could not determine an outcome (for
	// example, the candidate could not be materialized). ddmin treats
	// Unresolved conservatively, as if the failure did not reproduce.
	Unresolved
)

// String returns a stable lower-case name for the outcome.
func (o Outcome) String() string {
	switch o {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	case Unresolved:
		return "unresolved"
	default:
		return fmt.Sprintf("outcome(%d)", int(o))
	}
}

// Test reports whether the failure reproduces when only the given subset of
// items is applied. A non-nil error aborts minimization immediately.
type Test[T any] func(subset []T) (Outcome, error)

// BatchTest evaluates an ordered list of candidate subsets and reports which
// one ddmin should adopt. It returns sel, the index of the lowest-indexed
// subset whose outcome is Fail, or -1 when none fail; and evaluated, the
// number of subsets actually tested (used only for Stats). A non-nil error
// aborts minimization immediately.
//
// The executor owns evaluation order and concurrency: a sequential executor
// evaluates in index order and may stop at the first failure, while a parallel
// executor may evaluate several subsets at once. Either way sel must be the
// lowest-indexed failing subset, so the minimized result is independent of how
// the executor schedules the work.
type BatchTest[T any] func(subsets [][]T) (sel int, evaluated int, err error)

// Stats describes the work performed during one Minimize call.
type Stats struct {
	// Tests is the number of Test invocations.
	Tests int
}

// Minimize returns a reduced subset of items that still satisfies
// Test(subset) == Fail, assuming Test(items) == Fail. Callers are expected to
// have verified that precondition; Minimize does not re-test the full input.
// If Test returns Unresolved for a required neighboring subset, callers must
// not claim that the result is proven 1-minimal without a separate check.
//
// The returned slice preserves the relative order of items. Minimize is
// deterministic: identical inputs and Test behavior produce identical results
// and identical Test invocation sequences. It is a thin wrapper over
// MinimizeBatch using a sequential, early-breaking executor.
func Minimize[T any](items []T, test Test[T]) ([]T, Stats, error) {
	return MinimizeBatch(items, sequential(test))
}

// MinimizeBatch is Minimize expressed in terms of a BatchTest. The algorithm
// is identical to classic ddmin; it differs only in handing each granularity
// level's candidate subsets to the executor as one batch, so a parallel
// executor can evaluate them concurrently. Because the executor always reports
// the lowest-indexed failing subset, the minimized result is identical to the
// sequential Minimize regardless of evaluation order or concurrency.
func MinimizeBatch[T any](items []T, test BatchTest[T]) ([]T, Stats, error) {
	var stats Stats
	if len(items) <= 1 {
		return append([]T(nil), items...), stats, nil
	}

	current := append([]T(nil), items...)
	n := 2

	for len(current) >= 2 {
		chunks := split(current, n)

		// Try each chunk: a smaller failing subset?
		sel, evaluated, err := test(chunks)
		stats.Tests += evaluated
		if err != nil {
			return nil, stats, err
		}
		if sel >= 0 {
			current = chunks[sel]
			n = 2
			continue
		}

		// Try each complement, unless chunks are halves (then the
		// complements are the same sets as the chunks).
		reduced := false
		if n > 2 {
			comps := complements(chunks)
			sel, evaluated, err := test(comps)
			stats.Tests += evaluated
			if err != nil {
				return nil, stats, err
			}
			if sel >= 0 {
				current = comps[sel]
				n = max(n-1, 2)
				reduced = true
			}
		}

		if !reduced {
			if n >= len(current) {
				break // already at singleton granularity; current is 1-minimal
			}
			n = min(n*2, len(current))
		}
	}

	return current, stats, nil
}

// sequential adapts a single-subset Test into a BatchTest that evaluates
// candidates in index order and stops at the first failure. This reproduces
// the exact invocation sequence — and therefore the Stats — of classic ddmin.
func sequential[T any](test Test[T]) BatchTest[T] {
	return func(subsets [][]T) (int, int, error) {
		for i, subset := range subsets {
			out, err := test(subset)
			if err != nil {
				return -1, i + 1, fmt.Errorf("ddmin test (subset size %d): %w", len(subset), err)
			}
			if out == Fail {
				return i, i + 1, nil
			}
		}
		return -1, len(subsets), nil
	}
}

// split partitions items into n contiguous chunks of near-equal size,
// preserving order. n must be <= len(items); every chunk is non-empty.
func split[T any](items []T, n int) [][]T {
	chunks := make([][]T, 0, n)
	size := len(items) / n
	rem := len(items) % n
	start := 0
	for i := 0; i < n; i++ {
		end := start + size
		if i < rem {
			end++
		}
		chunks = append(chunks, items[start:end])
		start = end
	}
	return chunks
}

// complement concatenates every chunk except chunks[skip], preserving order.
func complement[T any](chunks [][]T, skip int) []T {
	var out []T
	for i, c := range chunks {
		if i == skip {
			continue
		}
		out = append(out, c...)
	}
	return out
}

// complements returns the complement of each chunk, in chunk order. comps[i]
// omits chunks[i]; this is the ordered candidate list ddmin tests after no
// single chunk reproduces the failure.
func complements[T any](chunks [][]T) [][]T {
	comps := make([][]T, len(chunks))
	for i := range chunks {
		comps[i] = complement(chunks, i)
	}
	return comps
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
