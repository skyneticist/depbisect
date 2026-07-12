package manifest

import (
	"strings"
	"testing"
)

func lockChange(name, spec, oldR, newR string) LockfileChange {
	return LockfileChange{Name: name, Section: Dependencies, Spec: spec, OldResolved: oldR, NewResolved: newR}
}

func TestPinChangesJS(t *testing.T) {
	eco := jsEcosystem{parseLock: ParsePackageLock}
	pins := eco.PinChanges([]LockfileChange{
		lockChange("alpha", "^1.0.0", "1.2.0", "1.3.0"),
		lockChange("aliased", "npm:other@^1.0.0", "1.2.0", "1.3.0"),
		lockChange("linked", "file:pkgs/linked", "file:pkgs/linked", "file:pkgs/linked2"),
		lockChange("hosted", "user/repo", "1.0.0", "1.1.0"),
	})
	if len(pins) != 1 {
		t.Fatalf("PinChanges = %d changes, want 1 (protocol/path specs unpinnable): %v", len(pins), pins)
	}
	c := pins[0]
	if c.Name != "alpha" || c.OldSpec != "1.2.0" || c.NewSpec != "^1.0.0" {
		t.Errorf("pin = %+v", c)
	}
	if c.Kind != Updated || !c.LockfileOnly {
		t.Errorf("pin kind/marker = %v/%v", c.Kind, c.LockfileOnly)
	}
}

func TestPinChangesCargo(t *testing.T) {
	pins := cargoEcosystem{}.PinChanges([]LockfileChange{
		lockChange("serde", "1.0", "1.0.100", "1.0.200"),
	})
	if len(pins) != 1 || pins[0].OldSpec != "=1.0.100" {
		t.Fatalf("cargo pin = %+v, want =1.0.100", pins)
	}
}

func TestPinChangesPyPreservesExtrasAndMarker(t *testing.T) {
	pins := pyEcosystem{}.PinChanges([]LockfileChange{
		lockChange("requests", `[socks]>=2; python_version < "3.12"`, "2.31.0", "2.32.0"),
		lockChange("bare", "", "1.0.0", "1.1.0"),
	})
	if len(pins) != 2 {
		t.Fatalf("PinChanges = %d changes, want 2", len(pins))
	}
	if got, want := pins[0].OldSpec, `[socks]==2.31.0; python_version < "3.12"`; got != want {
		t.Errorf("extras+marker pin = %q, want %q", got, want)
	}
	if got := pins[1].OldSpec; got != "==1.0.0" {
		t.Errorf("bare pin = %q, want ==1.0.0", got)
	}
}

func TestPinChangesComposer(t *testing.T) {
	pins := composerEcosystem{}.PinChanges([]LockfileChange{
		lockChange("acme/lib", "^2.9", "v2.9.1", "v2.10.0"),
		lockChange("acme/branch", "dev-main", "dev-main", "dev-main"),
		lockChange("acme/alias", "dev-main as 2.0.x-dev", "dev-main", "dev-main"),
	})
	if len(pins) != 1 || pins[0].OldSpec != "v2.9.1" {
		t.Fatalf("composer pins = %+v, want single v2.9.1 pin", pins)
	}
}

func TestPinChangesGoAndPipNil(t *testing.T) {
	lcs := []LockfileChange{lockChange("dep", "v1.2.3", "v1.2.3", "v1.2.4")}
	if pins := (goEcosystem{}).PinChanges(lcs); pins != nil {
		t.Errorf("go PinChanges = %v, want nil", pins)
	}
	if pins := (pipEcosystem{}).PinChanges(lcs); pins != nil {
		t.Errorf("pip PinChanges = %v, want nil", pins)
	}
}

func TestSplitPySpec(t *testing.T) {
	cases := []struct {
		spec, extras, specifier, marker string
	}{
		{"", "", "", ""},
		{">=2.0", "", ">=2.0", ""},
		{"[socks]>=2", "[socks]", ">=2", ""},
		{`>=2; python_version < "3.12"`, "", ">=2", `; python_version < "3.12"`},
		{`[a,b]~=1.4; os_name == "posix"`, "[a,b]", "~=1.4", `; os_name == "posix"`},
	}
	for _, tc := range cases {
		extras, specifier, marker := splitPySpec(tc.spec)
		if extras != tc.extras || specifier != tc.specifier || marker != tc.marker {
			t.Errorf("splitPySpec(%q) = %q,%q,%q want %q,%q,%q",
				tc.spec, extras, specifier, marker, tc.extras, tc.specifier, tc.marker)
		}
	}
}

// TestRenderPinnedChangeJS proves the executor contract for synthetic pins:
// reverting writes the exact old pin into the manifest, applying leaves the
// target manifest's spec untouched so the checked-out lockfile still governs.
func TestRenderPinnedChangeJS(t *testing.T) {
	to := mustParse(t, basePkg)
	pins := jsEcosystem{}.PinChanges([]LockfileChange{
		lockChange("beta", "2.0.0", "2.0.3", "2.0.7"),
	})

	reverted, err := Render(to, pins, nil)
	if err != nil {
		t.Fatalf("Render reverted: %v", err)
	}
	if !strings.Contains(string(reverted), `"beta": "2.0.3"`) {
		t.Errorf("reverted candidate lacks pin: %s", reverted)
	}

	applied, err := Render(to, pins, map[string]bool{pins[0].ID(): true})
	if err != nil {
		t.Fatalf("Render applied: %v", err)
	}
	if !strings.Contains(string(applied), `"beta": "2.0.0"`) {
		t.Errorf("applied candidate should keep the manifest spec: %s", applied)
	}
}

func TestRenderPinnedChangePyproject(t *testing.T) {
	toDoc := `[project]
name = "demo"
dependencies = ["pkg>=1"]
`
	to, err := ParsePyproject([]byte(toDoc))
	if err != nil {
		t.Fatalf("ParsePyproject: %v", err)
	}
	pins := pyEcosystem{}.PinChanges([]LockfileChange{
		lockChange("pkg", ">=1", "1.0.0", "2.0.0"),
	})
	reverted, err := RenderPyproject(to, pins, nil)
	if err != nil {
		t.Fatalf("RenderPyproject: %v", err)
	}
	if !strings.Contains(string(reverted), "pkg==1.0.0") {
		t.Errorf("reverted candidate lacks pin: %s", reverted)
	}
}
