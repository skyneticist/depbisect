package ddmin

import "testing"

// spreadCulprits returns k indices spread evenly across [0, n), used to exercise
// ddmin against a failure that depends on several scattered elements.
func spreadCulprits(n, k int) []int {
	if k > n {
		k = n
	}
	step := n / k
	if step == 0 {
		step = 1
	}
	out := make([]int, k)
	for i := 0; i < k; i++ {
		out[i] = (i * step) % n
	}
	return out
}

func BenchmarkMinimize(b *testing.B) {
	cases := []struct {
		name     string
		n        int
		culprits []int
	}{
		{"n16_single", 16, []int{7}},
		{"n64_pair", 64, []int{5, 60}},
		{"n128_triple", 128, []int{0, 64, 127}},
		{"n256_spread16", 256, spreadCulprits(256, 16)},
	}
	for _, tc := range cases {
		tc := tc
		b.Run(tc.name, func(b *testing.B) {
			items := ints(tc.n)
			test := conjunctiveTest(tc.culprits)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := Minimize(items, test); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
