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

// FuzzParseYarnLock checks that yarn.lock parsing never panics and is
// deterministic across both the classic v1 and Berry YAML formats.
func FuzzParseYarnLock(f *testing.F) {
	f.Add([]byte("# yarn lockfile v1\n\nalpha@^1.0.0:\n  version \"1.0.3\"\n"))
	f.Add([]byte("\"@scope/a@^1.0.0\", \"@scope/a@^1.2.0\":\n  version \"1.2.3\"\n"))
	f.Add([]byte("__metadata:\n  version: 8\n\n\"alpha@npm:^1.0.0\":\n  version: 1.0.3\n"))
	f.Add([]byte(":"))
	f.Add([]byte("broken"))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := ParseYarnLock(data)
		if err != nil {
			return
		}
		second, err := ParseYarnLock(data)
		if err != nil {
			t.Fatalf("non-deterministic error on identical input: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("non-deterministic parse: %v vs %v", first, second)
		}
	})
}

// FuzzParseCargoToml checks that Cargo.toml parsing never panics and that any
// manifest which parses cleanly round-trips: rendering it with no changes
// applied and re-parsing must yield an identical set of dependency
// declarations.
func FuzzParseCargoToml(f *testing.F) {
	f.Add([]byte("[package]\nname = \"demo\"\n[dependencies]\na = \"1.0\"\n"))
	f.Add([]byte("[dependencies]\na = { version = \"1\", features = [\"x\"] }\n"))
	f.Add([]byte("[dependencies]\na = { path = \"../a\" }\n"))
	f.Add([]byte("[workspace]\nmembers = [\"crates/*\"]\n"))
	f.Add([]byte("not toml ["))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := ParseCargoToml(data)
		if err != nil {
			return // rejecting malformed input is fine; it must just not panic.
		}
		rendered, err := RenderCargo(c, nil, nil)
		if err != nil {
			t.Fatalf("RenderCargo after successful parse: %v\ninput:\n%s", err, data)
		}
		c2, err := ParseCargoToml(rendered)
		if err != nil {
			t.Fatalf("re-parse of rendered manifest: %v\nrendered:\n%s", err, rendered)
		}
		if diff := DiffCargo(c, c2); len(diff) != 0 {
			t.Fatalf("round-trip changed dependencies: %v\nrendered:\n%s", diff, rendered)
		}
	})
}

// FuzzParseCargoLock checks that Cargo.lock parsing never panics and is
// deterministic: parsing identical bytes twice yields an identical result.
func FuzzParseCargoLock(f *testing.F) {
	f.Add([]byte("version = 3\n[[package]]\nname = \"a\"\nversion = \"1.0.0\"\n"))
	f.Add([]byte("[[package]]\nname = \"a\"\nversion = \"1.0.0\"\n[[package]]\nname = \"a\"\nversion = \"2.0.0\"\n"))
	f.Add([]byte("version = 4\n"))
	f.Add([]byte("not toml ["))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := ParseCargoLock(data)
		if err != nil {
			return
		}
		second, err := ParseCargoLock(data)
		if err != nil {
			t.Fatalf("non-deterministic error on identical input: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("non-deterministic parse: %v vs %v", first, second)
		}
	})
}

// FuzzParsePyproject checks that parsing never panics and that any pyproject.toml
// which parses cleanly round-trips: rendering it with no changes and re-parsing
// must yield an identical set of dependency declarations. This exercises the
// hand-rolled PEP 508 requirement splitter against arbitrary input.
func FuzzParsePyproject(f *testing.F) {
	f.Add([]byte("[project]\nname = \"demo\"\ndependencies = [\"a>=1.0\"]\n"))
	f.Add([]byte("[project]\ndependencies = [\"Flask[async]>=2; python_version >= '3.8'\"]\n"))
	f.Add([]byte("[project]\ndependencies = [\"pkg @ https://example.com/p.whl\", \"bare\"]\n"))
	f.Add([]byte("[tool.uv.workspace]\nmembers = [\"x/*\"]\n"))
	f.Add([]byte("[project]\ndependencies = [\"==1.0\", \"zope.interface\"]\n"))
	f.Add([]byte("not toml ["))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ParsePyproject(data)
		if err != nil {
			return // rejecting malformed input is fine; it must just not panic.
		}
		rendered, err := RenderPyproject(p, nil, nil)
		if err != nil {
			t.Fatalf("RenderPyproject after successful parse: %v\ninput:\n%s", err, data)
		}
		p2, err := ParsePyproject(rendered)
		if err != nil {
			t.Fatalf("re-parse of rendered manifest: %v\nrendered:\n%s", err, rendered)
		}
		if diff := DiffPyproject(p, p2); len(diff) != 0 {
			t.Fatalf("round-trip changed dependencies: %v\nrendered:\n%s", diff, rendered)
		}
	})
}

// FuzzParseUvLock checks that uv.lock parsing never panics and is deterministic:
// parsing identical bytes twice yields an identical result.
func FuzzParseUvLock(f *testing.F) {
	f.Add([]byte("version = 1\n[[package]]\nname = \"a\"\nversion = \"1.0.0\"\n"))
	f.Add([]byte("[[package]]\nname = \"A\"\nversion = \"1.0.0\"\n[[package]]\nname = \"a\"\nversion = \"2.0.0\"\n"))
	f.Add([]byte("version = 1\n"))
	f.Add([]byte("not toml ["))

	f.Fuzz(func(t *testing.T, data []byte) {
		first, err := ParseUvLock(data)
		if err != nil {
			return
		}
		second, err := ParseUvLock(data)
		if err != nil {
			t.Fatalf("non-deterministic error on identical input: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("non-deterministic parse: %v vs %v", first, second)
		}
	})
}
