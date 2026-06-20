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
