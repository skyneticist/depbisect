package manifest

import (
	"fmt"
	"strings"
	"testing"
)

// makeBenchManifest builds a package.json with n dependencies. When bump is
// true, every other dependency carries a higher version, so diffing a bumped
// manifest against an unbumped one yields ~n/2 changes.
func makeBenchManifest(n int, bump bool) []byte {
	entries := make([]string, n)
	for i := 0; i < n; i++ {
		v := "1.0.0"
		if bump && i%2 == 0 {
			v = "2.0.0"
		}
		entries[i] = fmt.Sprintf(`"dep%03d":"^%s"`, i, v)
	}
	return []byte(fmt.Sprintf(
		`{"name":"bench","version":"1.0.0","dependencies":{%s}}`,
		strings.Join(entries, ","),
	))
}

// makeBenchLock builds a v3 package-lock.json resolving n dependencies.
func makeBenchLock(n int) []byte {
	entries := make([]string, n+1)
	entries[0] = `"":{}`
	for i := 0; i < n; i++ {
		entries[i+1] = fmt.Sprintf(`"node_modules/dep%03d":{"version":"1.0.%d"}`, i, i)
	}
	return []byte(fmt.Sprintf(
		`{"lockfileVersion":3,"packages":{%s}}`,
		strings.Join(entries, ","),
	))
}

func BenchmarkParsePackageJSON(b *testing.B) {
	data := makeBenchManifest(100, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParsePackageJSON(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParsePackageLock(b *testing.B) {
	data := makeBenchLock(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParsePackageLock(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDiff(b *testing.B) {
	base, err := ParsePackageJSON(makeBenchManifest(100, false))
	if err != nil {
		b.Fatal(err)
	}
	updated, err := ParsePackageJSON(makeBenchManifest(100, true))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Diff(base, updated)
	}
}

func BenchmarkRender(b *testing.B) {
	base, err := ParsePackageJSON(makeBenchManifest(100, false))
	if err != nil {
		b.Fatal(err)
	}
	updated, err := ParsePackageJSON(makeBenchManifest(100, true))
	if err != nil {
		b.Fatal(err)
	}
	changes := Diff(base, updated)
	applied := make(map[string]bool, len(changes))
	for i, c := range changes {
		if i%2 == 0 {
			applied[c.ID()] = true
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Render(updated, changes, applied); err != nil {
			b.Fatal(err)
		}
	}
}
