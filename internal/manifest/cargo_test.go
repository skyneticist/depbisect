package manifest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const baseCargo = `[package]
name = "demo"
version = "0.1.0"
edition = "2021"

[dependencies]
serde = "1.0.130"
tokio = { version = "1.20", features = ["full"] }
gone = "0.3"
localdep = { path = "../local" }

[dev-dependencies]
criterion = "0.4"

[build-dependencies]
cc = "1.0"
`

const newCargo = `[package]
name = "demo"
version = "0.1.0"
edition = "2021"

[dependencies]
serde = "1.0.150"
tokio = { version = "1.25", features = ["full"] }
added = "2.0"
localdep = { path = "../local" }

[dev-dependencies]
criterion = "0.4"

[build-dependencies]
cc = "1.0"
`

func mustParseCargo(t *testing.T, data string) *CargoToml {
	t.Helper()
	c, err := ParseCargoToml([]byte(data))
	if err != nil {
		t.Fatalf("ParseCargoToml: %v", err)
	}
	return c
}

// cargoSections re-parses rendered Cargo.toml into the version-spec view, so
// render assertions are robust to TOML formatting and key ordering.
func cargoSections(t *testing.T, rendered []byte) map[Section]map[string]string {
	t.Helper()
	c, err := ParseCargoToml(rendered)
	if err != nil {
		t.Fatalf("re-parse rendered Cargo.toml: %v\n%s", err, rendered)
	}
	return c.Sections
}

func TestParseCargoToml(t *testing.T) {
	c := mustParseCargo(t, baseCargo)
	if c.Name != "demo" {
		t.Errorf("Name = %q", c.Name)
	}
	if got := c.Sections[CargoDependencies]["serde"]; got != "1.0.130" {
		t.Errorf("serde spec = %q", got)
	}
	if got := c.Sections[CargoDependencies]["tokio"]; got != "1.20" {
		t.Errorf("tokio spec = %q (want version extracted from table)", got)
	}
	if got := c.Sections[CargoDevDependencies]["criterion"]; got != "0.4" {
		t.Errorf("criterion spec = %q", got)
	}
	if got := c.Sections[CargoBuildDependencies]["cc"]; got != "1.0" {
		t.Errorf("cc spec = %q", got)
	}
	if _, ok := c.Sections[CargoDependencies]["localdep"]; ok {
		t.Error("path dependency should be skipped (no bisectable version)")
	}
	if c.HasWorkspace {
		t.Error("HasWorkspace = true for plain package")
	}
}

func TestParseCargoTomlWorkspace(t *testing.T) {
	c := mustParseCargo(t, "[workspace]\nmembers = [\"crates/*\"]\n")
	if !c.HasWorkspace {
		t.Error("HasWorkspace = false, want true")
	}
}

func TestParseCargoTomlSkipsWorkspaceInherited(t *testing.T) {
	c := mustParseCargo(t, "[package]\nname = \"m\"\n\n[dependencies]\nserde = { workspace = true }\n")
	if _, ok := c.Sections[CargoDependencies]["serde"]; ok {
		t.Error("workspace-inherited dependency should be skipped")
	}
}

func TestParseCargoTomlErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // substring of error
	}{
		{"invalid toml", `[dependencies`, "parse Cargo.toml"},
		{"section not table", `dependencies = "x"`, "dependencies"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCargoToml([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestDiffCargo(t *testing.T) {
	got := DiffCargo(mustParseCargo(t, baseCargo), mustParseCargo(t, newCargo))
	want := []Change{
		{Name: "added", Section: CargoDependencies, Kind: Added, NewSpec: "2.0"},
		{Name: "gone", Section: CargoDependencies, Kind: Removed, OldSpec: "0.3"},
		{Name: "serde", Section: CargoDependencies, Kind: Updated, OldSpec: "1.0.130", NewSpec: "1.0.150"},
		{Name: "tokio", Section: CargoDependencies, Kind: Updated, OldSpec: "1.20", NewSpec: "1.25"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiffCargo:\n got %+v\nwant %+v", got, want)
	}
}

func TestRenderCargoRevertsUnapplied(t *testing.T) {
	to := mustParseCargo(t, newCargo)
	changes := DiffCargo(mustParseCargo(t, baseCargo), to)
	// Apply only the serde update; every other change reverts to base.
	rendered, err := RenderCargo(to, changes, map[string]bool{"dependencies:serde": true})
	if err != nil {
		t.Fatalf("RenderCargo: %v", err)
	}
	deps := cargoSections(t, rendered)[CargoDependencies]
	if deps["serde"] != "1.0.150" {
		t.Errorf("serde = %q, want applied new version 1.0.150", deps["serde"])
	}
	if deps["tokio"] != "1.20" {
		t.Errorf("tokio = %q, want reverted 1.20", deps["tokio"])
	}
	if deps["gone"] != "0.3" {
		t.Errorf("gone = %q, want re-added 0.3", deps["gone"])
	}
	if _, ok := deps["added"]; ok {
		t.Error("added dependency should be removed when its change is unapplied")
	}
}

func TestRenderCargoPreservesTableKeys(t *testing.T) {
	to := mustParseCargo(t, newCargo)
	changes := DiffCargo(mustParseCargo(t, baseCargo), to)
	// Revert everything: tokio drops to 1.20 but must keep its features.
	rendered, err := RenderCargo(to, changes, map[string]bool{})
	if err != nil {
		t.Fatalf("RenderCargo: %v", err)
	}
	var doc struct {
		Dependencies struct {
			Tokio struct {
				Version  string   `toml:"version"`
				Features []string `toml:"features"`
			} `toml:"tokio"`
		} `toml:"dependencies"`
	}
	if err := toml.Unmarshal(rendered, &doc); err != nil {
		t.Fatalf("unmarshal rendered: %v\n%s", err, rendered)
	}
	if doc.Dependencies.Tokio.Version != "1.20" {
		t.Errorf("tokio version = %q, want reverted 1.20", doc.Dependencies.Tokio.Version)
	}
	if !reflect.DeepEqual(doc.Dependencies.Tokio.Features, []string{"full"}) {
		t.Errorf("tokio features = %v, want [full] preserved", doc.Dependencies.Tokio.Features)
	}
}

func TestRenderCargoAllApplied(t *testing.T) {
	to := mustParseCargo(t, newCargo)
	changes := DiffCargo(mustParseCargo(t, baseCargo), to)
	applied := map[string]bool{}
	for _, c := range changes {
		applied[c.ID()] = true
	}
	rendered, err := RenderCargo(to, changes, applied)
	if err != nil {
		t.Fatalf("RenderCargo: %v", err)
	}
	deps := cargoSections(t, rendered)[CargoDependencies]
	want := map[string]string{"serde": "1.0.150", "tokio": "1.25", "added": "2.0"}
	for name, v := range want {
		if deps[name] != v {
			t.Errorf("%s = %q, want %q", name, deps[name], v)
		}
	}
	if _, ok := deps["gone"]; ok {
		t.Error("gone should stay removed when all changes are applied")
	}
}

func TestParseCargoLock(t *testing.T) {
	const lock = `version = 3

[[package]]
name = "serde"
version = "1.0.150"

[[package]]
name = "tokio"
version = "1.25.0"

[[package]]
name = "serde"
version = "0.9.15"
`
	got, err := ParseCargoLock([]byte(lock))
	if err != nil {
		t.Fatalf("ParseCargoLock: %v", err)
	}
	if got["tokio"] != "1.25.0" {
		t.Errorf("tokio resolved = %q", got["tokio"])
	}
	if got["serde"] == "" {
		t.Error("serde missing from resolved set")
	}
}

func TestParseCargoTomlSkipsValuelessDeps(t *testing.T) {
	// git, empty-version, and non-table/non-string deps carry no version and
	// must be skipped rather than bisected.
	c := mustParseCargo(t, "[dependencies]\ngit_dep = { git = \"x\" }\nempty_ver = { version = \"\" }\nnumeric = 1\n")
	if got := c.Sections[CargoDependencies]; len(got) != 0 {
		t.Errorf("expected all valueless deps skipped, got %v", got)
	}
}

func TestRenderCargoReAddsRemovedSection(t *testing.T) {
	// Reverting a removed dependency must recreate its section when the target
	// manifest dropped the whole table.
	base := mustParseCargo(t, "[package]\nname = \"d\"\n\n[build-dependencies]\ncc = \"1.0\"\n")
	to := mustParseCargo(t, "[package]\nname = \"d\"\n")
	changes := DiffCargo(base, to)
	rendered, err := RenderCargo(to, changes, map[string]bool{})
	if err != nil {
		t.Fatalf("RenderCargo: %v", err)
	}
	if got := cargoSections(t, rendered)[CargoBuildDependencies]["cc"]; got != "1.0" {
		t.Errorf("cc = %q, want re-added 1.0 into recreated section", got)
	}
}
