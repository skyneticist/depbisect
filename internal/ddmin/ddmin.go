// Package ddmin implements Zeller's ddmin delta-debugging algorithm.
//
// The implementation is deterministic and free of I/O: callers supply a Test
// function and receive a 1-minimal failing subset. All candidate subsets
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

// Stats describes the work performed during one Minimize call.
type Stats struct {
	// Tests is the number of Test invocations.
	Tests int
}

// Minimize returns a 1-minimal subset of items that still satisfies
// Test(subset) == Fail, assuming Test(items) == Fail. Callers are expected to
// have verified that precondition; Minimize does not re-test the full input.
//
// The returned slice preserves the relative order of items. Minimize is
// deterministic: identical inputs and Test behavior produce identical results
// and identical Test invocation sequences.
func Minimize[T any](items []T, test Test[T]) ([]T, Stats, error) {
	var stats Stats
	if len(items) <= 1 {
		return append([]T(nil), items...), stats, nil
	}

	current := append([]T(nil), items...)
	n := 2

	runTest := func(subset []T) (Outcome, error) {
		stats.Tests++
		out, err := test(subset)
		if err != nil {
			return out, fmt.Errorf("ddmin test (subset size %d): %w", len(subset), err)
		}
		return out, nil
	}

	for len(current) >= 2 {
		chunks := split(current, n)

		reduced := false

		// Try each chunk: a smaller failing subset?
		for _, chunk := range chunks {
			out, err := runTest(chunk)
			if err != nil {
				return nil, stats, err
			}
			if out == Fail {
				current = chunk
				n = 2
				reduced = true
				break
			}
		}

		// Try each complement, unless chunks are halves (then the
		// complements are the same sets as the chunks).
		if !reduced && n > 2 {
			for i := range chunks {
				comp := complement(chunks, i)
				out, err := runTest(comp)
				if err != nil {
					return nil, stats, err
				}
				if out == Fail {
					current = comp
					n = max(n-1, 2)
					reduced = true
					break
				}
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
