package manifest

import "testing"

func TestEcosystemForDispatch(t *testing.T) {
	cases := []struct {
		manager  string
		manifest string
	}{
		{"npm", basePkg},
		{"pnpm", basePkg},
		{"cargo", baseCargo},
		{"go", baseGoMod},
	}
	for _, tc := range cases {
		t.Run(tc.manager, func(t *testing.T) {
			eco, err := EcosystemFor(tc.manager)
			if err != nil {
				t.Fatalf("EcosystemFor(%q): %v", tc.manager, err)
			}
			p, err := eco.Parse([]byte(tc.manifest))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if p.HasWorkspaceLayout() {
				t.Error("HasWorkspaceLayout = true for a plain manifest")
			}
		})
	}
}

func TestEcosystemForUnknown(t *testing.T) {
	if _, err := EcosystemFor("yarn"); err == nil {
		t.Error("expected error for unknown manager")
	}
}

func TestEcosystemDiffRenderRoundTrip(t *testing.T) {
	// Each ecosystem parses, diffs, and renders entirely through the interface.
	js, _ := EcosystemFor("npm")
	oldP, _ := js.Parse([]byte(basePkg))
	newP, _ := js.Parse([]byte(newPkg))
	if changes := js.Diff(oldP, newP); len(changes) == 0 {
		t.Fatal("expected JS changes")
	} else if _, err := js.Render(newP, changes, map[string]bool{}); err != nil {
		t.Fatalf("JS render: %v", err)
	}

	cargo, _ := EcosystemFor("cargo")
	oldC, _ := cargo.Parse([]byte(baseCargo))
	newC, _ := cargo.Parse([]byte(newCargo))
	if changes := cargo.Diff(oldC, newC); len(changes) == 0 {
		t.Fatal("expected Cargo changes")
	} else if _, err := cargo.Render(newC, changes, map[string]bool{}); err != nil {
		t.Fatalf("Cargo render: %v", err)
	}

	gomod, _ := EcosystemFor("go")
	oldG, _ := gomod.Parse([]byte(baseGoMod))
	newG, _ := gomod.Parse([]byte(newGoMod))
	if changes := gomod.Diff(oldG, newG); len(changes) == 0 {
		t.Fatal("expected Go changes")
	} else if _, err := gomod.Render(newG, changes, map[string]bool{}); err != nil {
		t.Fatalf("Go render: %v", err)
	}
}

func TestLockfileOnlyCargo(t *testing.T) {
	// serde's spec is identical in both manifests but its resolved version
	// differs — a lockfile-only change.
	old := mustParseCargo(t, baseCargo)
	new := mustParseCargo(t, baseCargo)
	got := LockfileOnlyCargo(old, new, Resolved{"serde": "1.0.130"}, Resolved{"serde": "1.0.200"})
	if len(got) != 1 || got[0].Name != "serde" {
		t.Fatalf("LockfileOnlyCargo = %+v, want [serde]", got)
	}
	if got[0].OldResolved != "1.0.130" || got[0].NewResolved != "1.0.200" {
		t.Errorf("resolved versions = %+v", got[0])
	}
}

func TestEcosystemParseLock(t *testing.T) {
	cases := []struct {
		manager, lock, name, ver string
	}{
		{"npm", `{"lockfileVersion":3,"packages":{"node_modules/a":{"version":"1.2.3"}}}`, "a", "1.2.3"},
		{"pnpm", "lockfileVersion: '6.0'\ndependencies:\n  a:\n    specifier: ^1\n    version: 1.2.3\n", "a", "1.2.3"},
		{"cargo", "[[package]]\nname = \"a\"\nversion = \"1.2.3\"\n", "a", "1.2.3"},
		{"go", "github.com/a/b v1.2.3 h1:xxx\ngithub.com/a/b v1.2.3/go.mod h1:yyy\n", "github.com/a/b", "v1.2.3"},
	}
	for _, tc := range cases {
		t.Run(tc.manager, func(t *testing.T) {
			eco, err := EcosystemFor(tc.manager)
			if err != nil {
				t.Fatal(err)
			}
			res, err := eco.ParseLock([]byte(tc.lock))
			if err != nil {
				t.Fatalf("ParseLock: %v", err)
			}
			if res[tc.name] != tc.ver {
				t.Errorf("%s resolved = %q, want %q", tc.name, res[tc.name], tc.ver)
			}
		})
	}
}

func TestEcosystemLockfileOnly(t *testing.T) {
	// Each ecosystem flags a dependency whose spec is unchanged but whose
	// resolved version moved, through the interface.
	js, _ := EcosystemFor("npm")
	jsOld, _ := js.Parse([]byte(basePkg))
	jsNew, _ := js.Parse([]byte(basePkg))
	if lo := js.LockfileOnly(jsOld, jsNew, Resolved{"alpha": "1.0.0"}, Resolved{"alpha": "1.5.0"}); len(lo) != 1 || lo[0].Name != "alpha" {
		t.Errorf("js LockfileOnly = %+v, want [alpha]", lo)
	}

	cargo, _ := EcosystemFor("cargo")
	cOld, _ := cargo.Parse([]byte(baseCargo))
	cNew, _ := cargo.Parse([]byte(baseCargo))
	if lo := cargo.LockfileOnly(cOld, cNew, Resolved{"serde": "1.0.130"}, Resolved{"serde": "1.0.200"}); len(lo) != 1 || lo[0].Name != "serde" {
		t.Errorf("cargo LockfileOnly = %+v, want [serde]", lo)
	}

	gomod, _ := EcosystemFor("go")
	gOld, _ := gomod.Parse([]byte(baseGoMod))
	gNew, _ := gomod.Parse([]byte(baseGoMod))
	if lo := gomod.LockfileOnly(gOld, gNew, Resolved{"github.com/alpha/one": "v1.0.0"}, Resolved{"github.com/alpha/one": "v1.5.0"}); len(lo) != 1 || lo[0].Name != "github.com/alpha/one" {
		t.Errorf("go LockfileOnly = %+v, want [github.com/alpha/one]", lo)
	}
}

func TestEcosystemParseError(t *testing.T) {
	cases := map[string]string{"npm": "{not json", "cargo": "not = = toml [[", "go": "module x\nrequire (\n"}
	for manager, bad := range cases {
		eco, _ := EcosystemFor(manager)
		if _, err := eco.Parse([]byte(bad)); err == nil {
			t.Errorf("%s Parse(malformed) = nil error, want error", manager)
		}
	}
}
