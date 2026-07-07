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

func BenchmarkParsePnpmLock(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf("  dep%03d:\n    specifier: ^1.0.0\n    version: 1.0.%d", i, i)
	}
	data := []byte("lockfileVersion: '6.0'\n\ndependencies:\n" + strings.Join(entries, "\n") + "\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParsePnpmLock(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseYarnLock(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf("dep%03d@^1.0.0:\n  version \"1.0.%d\"\n", i, i)
	}
	data := []byte("# yarn lockfile v1\n\n" + strings.Join(entries, "\n"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseYarnLock(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseCargoToml(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf("dep%03d = \"1.0.%d\"", i, i)
	}
	data := []byte("[package]\nname = \"bench\"\n\n[dependencies]\n" + strings.Join(entries, "\n") + "\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseCargoToml(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseCargoLock(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf("[[package]]\nname = \"dep%03d\"\nversion = \"1.0.%d\"\n", i, i)
	}
	data := []byte("version = 3\n\n" + strings.Join(entries, "\n"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseCargoLock(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseGoMod(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf("\texample.com/dep%03d v1.0.%d", i, i)
	}
	data := []byte("module example.com/bench\n\ngo 1.20\n\nrequire (\n" + strings.Join(entries, "\n") + "\n)\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseGoMod(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseGoSum(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf("example.com/dep%03d v1.0.%d h1:aaa\nexample.com/dep%03d v1.0.%d/go.mod h1:bbb", i, i, i, i)
	}
	data := []byte(strings.Join(entries, "\n") + "\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseGoSum(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParsePyproject(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf("\"dep%03d>=1.0.%d\"", i, i)
	}
	data := []byte("[project]\nname = \"bench\"\nrequires-python = \">=3.9\"\ndependencies = [" + strings.Join(entries, ", ") + "]\n")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParsePyproject(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseUvLock(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf("[[package]]\nname = \"dep%03d\"\nversion = \"1.0.%d\"\n", i, i)
	}
	data := []byte("version = 1\n\n" + strings.Join(entries, "\n"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseUvLock(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseComposerJSON(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf(`"acme/dep%03d":"^1.0.%d"`, i, i)
	}
	data := []byte(`{"name":"acme/bench","require":{` + strings.Join(entries, ",") + `}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseComposerJSON(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseComposerLock(b *testing.B) {
	entries := make([]string, 100)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"name":"acme/dep%03d","version":"1.0.%d"}`, i, i)
	}
	data := []byte(`{"packages":[` + strings.Join(entries, ",") + `],"packages-dev":[]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseComposerLock(data); err != nil {
			b.Fatal(err)
		}
	}
}

// makeBenchRequirements builds a pinned requirements.txt with n packages,
// prefixed by the option and comment lines a pip-compile export carries.
func makeBenchRequirements(n int) []byte {
	lines := make([]string, 0, n+3)
	lines = append(lines, "# bench pins", "--no-index", "--find-links wheels")
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("dep%03d==1.0.%d", i, i))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func BenchmarkParseRequirements(b *testing.B) {
	data := makeBenchRequirements(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseRequirements(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseRequirementsPins(b *testing.B) {
	data := makeBenchRequirements(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ParseRequirementsPins(data); err != nil {
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
