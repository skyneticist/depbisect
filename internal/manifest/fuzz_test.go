package manifest

import (
	"reflect"
	"testing"
)

// FuzzParsePackageJSON checks that parsing never panics and that any manifest
// which parses cleanly round-trips: rendering it with no changes applied and
// re-parsing must yield an identical set of dependency declarations.
func FuzzParsePackageJSON(f *testing.F) {
	f.Add([]byte(`{"name":"demo","dependencies":{"a":"^1.0.0"},"devDependencies":{"b":"2.0.0"}}`))
	f.Add([]byte(`{"dependencies":{},"optionalDependencies":{"c":"1.2.3"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"name":123,"workspaces":["pkgs/*"]}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ParsePackageJSON(data)
		if err != nil {
			return // rejecting malformed input is fine; it must just not panic.
		}
		rendered, err := Render(p, nil, nil)
		if err != nil {
			t.Fatalf("Render after successful parse: %v", err)
		}
		p2, err := ParsePackageJSON(rendered)
		if err != nil {
			t.Fatalf("re-parse of rendered manifest: %v\nrendered:\n%s", err, rendered)
		}
		if diff := Diff(p, p2); len(diff) != 0 {
			t.Fatalf("round-trip changed dependencies: %v\nrendered:\n%s", diff, rendered)
		}
	})
}

// FuzzParsePackageLock checks that npm lockfile parsing never panics and is
// deterministic: parsing identical bytes twice yields an identical result.
func FuzzParsePackageLock(f *testing.F) {
	f.Add([]byte(`{"lockfileVersion":1,"dependencies":{"a":{"version":"1.0.0"}}}`))
	f.Add([]byte(`{"lockfileVersion":3,"packages":{"":{},"node_modules/a":{"version":"1.2.3"}}}`))
	f.Add([]byte(`{"lockfileVersion":2,"packages":{}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`broken`))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := ParsePackageLock(data)
		if err != nil {
			return
		}
		second, err := ParsePackageLock(data)
		if err != nil {
			t.Fatalf("non-deterministic error on identical input: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("non-deterministic parse: %v vs %v", first, second)
		}
	})
}

// FuzzParsePnpmLock checks that pnpm lockfile parsing never panics and is
// deterministic across the v5/v6/v9 shapes the parser accepts.
func FuzzParsePnpmLock(f *testing.F) {
	f.Add([]byte("lockfileVersion: 5.4\ndependencies:\n  a: 1.2.3\n"))
	f.Add([]byte("lockfileVersion: '6.0'\ndependencies:\n  a:\n    specifier: ^1.0.0\n    version: 1.2.3\n"))
	f.Add([]byte("importers:\n  .:\n    dependencies:\n      a:\n        specifier: ^1\n        version: 1.0.0\n"))
	f.Add([]byte(":"))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := ParsePnpmLock(data)
		if err != nil {
			return
		}
		second, err := ParsePnpmLock(data)
		if err != nil {
			t.Fatalf("non-deterministic error on identical input: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("non-deterministic parse: %v vs %v", first, second)
		}
	})
}
