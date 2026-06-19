package manifest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const basePkg = `{
  "name": "demo",
  "version": "1.0.0",
  "scripts": {"test": "node test.js"},
  "dependencies": {
    "alpha": "^1.0.0",
    "beta": "2.0.0",
    "gone": "^3.0.0"
  },
  "devDependencies": {
    "delta": "~4.0.0"
  }
}`

const newPkg = `{
  "name": "demo",
  "version": "1.0.0",
  "scripts": {"test": "node test.js"},
  "dependencies": {
    "alpha": "^1.5.0",
    "beta": "2.0.0",
    "added": "1.0.0"
  },
  "devDependencies": {
    "delta": "~4.2.0"
  }
}`

func mustParse(t *testing.T, data string) *PackageJSON {
	t.Helper()
	p, err := ParsePackageJSON([]byte(data))
	if err != nil {
		t.Fatalf("ParsePackageJSON: %v", err)
	}
	return p
}

func TestParsePackageJSON(t *testing.T) {
	p := mustParse(t, basePkg)
	if p.Name != "demo" {
		t.Errorf("Name = %q", p.Name)
	}
	if got := p.Sections[Dependencies]["alpha"]; got != "^1.0.0" {
		t.Errorf("alpha spec = %q", got)
	}
	if got := p.Sections[DevDependencies]["delta"]; got != "~4.0.0" {
		t.Errorf("delta spec = %q", got)
	}
	if p.HasWorkspaces {
		t.Error("HasWorkspaces = true for plain package")
	}
}

func TestParsePackageJSONErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // substring of error
	}{
		{"invalid json", `{`, "parse package.json"},
		{"not an object", `[1,2]`, "parse package.json"},
		{"non-string spec", `{"dependencies": {"a": 1}}`, `"a"`},
		{"section not object", `{"dependencies": "x"}`, "dependencies"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePackageJSON([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestParsePackageJSONWorkspaces(t *testing.T) {
	p := mustParse(t, `{"name":"ws","workspaces":["packages/*"]}`)
	if !p.HasWorkspaces {
		t.Error("HasWorkspaces = false, want true")
	}
}

func TestDiff(t *testing.T) {
	old := mustParse(t, basePkg)
	new := mustParse(t, newPkg)
	got := Diff(old, new)
	want := []Change{
		{Name: "added", Section: Dependencies, Kind: Added, NewSpec: "1.0.0"},
		{Name: "alpha", Section: Dependencies, Kind: Updated, OldSpec: "^1.0.0", NewSpec: "^1.5.0"},
		{Name: "delta", Section: DevDependencies, Kind: Updated, OldSpec: "~4.0.0", NewSpec: "~4.2.0"},
		{Name: "gone", Section: Dependencies, Kind: Removed, OldSpec: "^3.0.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Diff:\n got %+v\nwant %+v", got, want)
	}
}

func TestDiffZeroChanges(t *testing.T) {
	old := mustParse(t, basePkg)
	new := mustParse(t, basePkg)
	if got := Diff(old, new); len(got) != 0 {
		t.Errorf("Diff identical = %+v, want empty", got)
	}
}

func TestDiffSectionMove(t *testing.T) {
	// Moving a dep between sections is a removal plus an addition.
	old := mustParse(t, `{"dependencies":{"x":"1.0.0"}}`)
	new := mustParse(t, `{"devDependencies":{"x":"1.0.0"}}`)
	got := Diff(old, new)
	if len(got) != 2 {
		t.Fatalf("Diff = %+v, want 2 changes", got)
	}
	if got[0].Kind != Removed || got[0].Section != Dependencies ||
		got[1].Kind != Added || got[1].Section != DevDependencies {
		t.Errorf("Diff = %+v", got)
	}
}

func sectionsOf(t *testing.T, rendered []byte) map[Section]map[string]string {
	t.Helper()
	p, err := ParsePackageJSON(rendered)
	if err != nil {
		t.Fatalf("re-parse rendered: %v", err)
	}
	return p.Sections
}

func TestRenderAllAndNone(t *testing.T) {
	old := mustParse(t, basePkg)
	new := mustParse(t, newPkg)
	changes := Diff(old, new)

	all := map[string]bool{}
	for _, c := range changes {
		all[c.ID()] = true
	}

	// All changes applied: dependency sections must equal the new manifest's.
	rendered, err := Render(new, changes, all)
	if err != nil {
		t.Fatal(err)
	}
	if got := sectionsOf(t, rendered); !reflect.DeepEqual(got, new.Sections) {
		t.Errorf("all applied:\n got %v\nwant %v", got, new.Sections)
	}

	// No changes applied: dependency sections must equal the old manifest's.
	rendered, err = Render(new, changes, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if got := sectionsOf(t, rendered); !reflect.DeepEqual(got, old.Sections) {
		t.Errorf("none applied:\n got %v\nwant %v", got, old.Sections)
	}
}

func TestRenderSubset(t *testing.T) {
	old := mustParse(t, basePkg)
	new := mustParse(t, newPkg)
	changes := Diff(old, new)

	// Apply only the "alpha" update.
	applied := map[string]bool{string(Dependencies) + ":alpha": true}
	rendered, err := Render(new, changes, applied)
	if err != nil {
		t.Fatal(err)
	}
	secs := sectionsOf(t, rendered)
	if got := secs[Dependencies]["alpha"]; got != "^1.5.0" {
		t.Errorf("alpha = %q, want new spec", got)
	}
	if got := secs[Dependencies]["gone"]; got != "^3.0.0" {
		t.Errorf("gone = %q, want restored", got)
	}
	if _, ok := secs[Dependencies]["added"]; ok {
		t.Error("added present, want removed (change not applied)")
	}
	if got := secs[DevDependencies]["delta"]; got != "~4.0.0" {
		t.Errorf("delta = %q, want old spec", got)
	}
}

func TestRenderPreservesOtherFields(t *testing.T) {
	old := mustParse(t, basePkg)
	new := mustParse(t, newPkg)
	changes := Diff(old, new)
	rendered, err := Render(new, changes, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rendered, &m); err != nil {
		t.Fatalf("rendered output is not valid JSON: %v", err)
	}
	var scripts map[string]string
	if err := json.Unmarshal(m["scripts"], &scripts); err != nil || scripts["test"] != "node test.js" {
		t.Errorf("scripts not preserved: %s err=%v", m["scripts"], err)
	}
	if string(m["version"]) != `"1.0.0"` {
		t.Errorf("version not preserved: %s", m["version"])
	}
}

func TestRenderDeterministic(t *testing.T) {
	old := mustParse(t, basePkg)
	new := mustParse(t, newPkg)
	changes := Diff(old, new)
	a, err := Render(new, changes, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		b, err := Render(new, changes, map[string]bool{})
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatal("Render output differs between calls")
		}
	}
}

func TestRenderDoesNotMutateInput(t *testing.T) {
	old := mustParse(t, basePkg)
	new := mustParse(t, newPkg)
	before := mustParse(t, newPkg)
	changes := Diff(old, new)
	if _, err := Render(new, changes, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(new.Sections, before.Sections) {
		t.Error("Render mutated its input manifest")
	}
}

const packageLockV3 = `{
  "name": "demo",
  "lockfileVersion": 3,
  "packages": {
    "": {"dependencies": {"alpha": "^1.0.0"}},
    "node_modules/alpha": {"version": "1.0.3"},
    "node_modules/@scope/pkg": {"version": "2.1.0"},
    "node_modules/alpha/node_modules/nested": {"version": "9.9.9"}
  }
}`

const packageLockV1 = `{
  "name": "demo",
  "lockfileVersion": 1,
  "dependencies": {
    "alpha": {"version": "1.0.3"},
    "beta": {"version": "2.0.0"}
  }
}`

func TestParsePackageLock(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Resolved
	}{
		{"v3 packages", packageLockV3, Resolved{"alpha": "1.0.3", "@scope/pkg": "2.1.0"}},
		{"v1 dependencies", packageLockV1, Resolved{"alpha": "1.0.3", "beta": "2.0.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePackageLock([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParsePackageLockMalformed(t *testing.T) {
	for _, in := range []string{"{", "[]", `{"lockfileVersion": 3}`} {
		if _, err := ParsePackageLock([]byte(in)); err == nil {
			t.Errorf("no error for %q", in)
		}
	}
}

const pnpmLockV6 = `lockfileVersion: '6.0'
dependencies:
  alpha:
    specifier: ^1.0.0
    version: 1.0.3
  withpeer:
    specifier: ^2.0.0
    version: 2.0.0(react@18.2.0)
devDependencies:
  '@scope/tool':
    specifier: ~3.0.0
    version: 3.0.1
`

const pnpmLockV9 = `lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      alpha:
        specifier: ^1.0.0
        version: 1.0.3
`

const pnpmLockV5 = `lockfileVersion: 5.4
specifiers:
  alpha: ^1.0.0
  withpeer: ^2.0.0
dependencies:
  alpha: 1.0.3
  withpeer: 2.0.0_react@18.2.0
`

func TestParsePnpmLock(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Resolved
	}{
		{"v6", pnpmLockV6, Resolved{"alpha": "1.0.3", "withpeer": "2.0.0", "@scope/tool": "3.0.1"}},
		{"v9 importers", pnpmLockV9, Resolved{"alpha": "1.0.3"}},
		{"v5 string values", pnpmLockV5, Resolved{"alpha": "1.0.3", "withpeer": "2.0.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePnpmLock([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParsePnpmLockLinkVersionKept(t *testing.T) {
	in := "lockfileVersion: '6.0'\ndependencies:\n  local:\n    specifier: link:../local\n    version: link:../local\n"
	got, err := ParsePnpmLock([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if got["local"] != "link:../local" {
		t.Errorf("link version mangled: %q", got["local"])
	}
}

func TestParsePnpmLockMalformed(t *testing.T) {
	for _, in := range []string{":\nbad", "lockfileVersion: '6.0'\ndependencies: 12"} {
		if _, err := ParsePnpmLock([]byte(in)); err == nil {
			t.Errorf("no error for %q", in)
		}
	}
}

func TestAnnotateResolved(t *testing.T) {
	changes := []Change{
		{Name: "alpha", Section: Dependencies, Kind: Updated, OldSpec: "^1.0.0", NewSpec: "^1.5.0"},
		{Name: "added", Section: Dependencies, Kind: Added, NewSpec: "1.0.0"},
	}
	AnnotateResolved(changes, Resolved{"alpha": "1.0.3"}, Resolved{"alpha": "1.5.2", "added": "1.0.0"})
	if changes[0].OldResolved != "1.0.3" || changes[0].NewResolved != "1.5.2" {
		t.Errorf("alpha resolved = %q -> %q", changes[0].OldResolved, changes[0].NewResolved)
	}
	if changes[1].OldResolved != "" || changes[1].NewResolved != "1.0.0" {
		t.Errorf("added resolved = %q -> %q", changes[1].OldResolved, changes[1].NewResolved)
	}
}

func TestLockfileOnly(t *testing.T) {
	old := mustParse(t, `{"dependencies":{"same":"^1.0.0","bumped":"^2.0.0"}}`)
	new := mustParse(t, `{"dependencies":{"same":"^1.0.0","bumped":"^2.1.0"}}`)
	oldR := Resolved{"same": "1.0.0", "bumped": "2.0.0"}
	newR := Resolved{"same": "1.9.0", "bumped": "2.1.0"}

	got := LockfileOnly(old, new, oldR, newR)
	want := []LockfileChange{{Name: "same", Section: Dependencies, Spec: "^1.0.0", OldResolved: "1.0.0", NewResolved: "1.9.0"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLockfileOnlyMissingResolution(t *testing.T) {
	// Unknown resolution on either side must not produce a phantom change.
	old := mustParse(t, `{"dependencies":{"a":"^1.0.0"}}`)
	new := mustParse(t, `{"dependencies":{"a":"^1.0.0"}}`)
	if got := LockfileOnly(old, new, Resolved{}, Resolved{"a": "1.2.0"}); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestChangeStringAndID(t *testing.T) {
	c := Change{Name: "alpha", Section: Dependencies, Kind: Updated, OldSpec: "^1.0.0", NewSpec: "^1.5.0", OldResolved: "1.0.3", NewResolved: "1.5.2"}
	if c.ID() != "dependencies:alpha" {
		t.Errorf("ID = %q", c.ID())
	}
	s := c.String()
	for _, want := range []string{"alpha", "1.0.3", "1.5.2"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q missing %q", s, want)
		}
	}
	added := Change{Name: "x", Section: Dependencies, Kind: Added, NewSpec: "1.0.0"}
	if !strings.Contains(added.String(), "added") {
		t.Errorf("added String() = %q", added.String())
	}
	removed := Change{Name: "y", Section: Dependencies, Kind: Removed, OldSpec: "2.0.0"}
	if !strings.Contains(removed.String(), "removed") {
		t.Errorf("removed String() = %q", removed.String())
	}
}

// TestChangeKindString pins the kind names. report.New writes Kind.String()
// straight into the report JSON "kind" field (and the Markdown table renders it
// too), so these are an output contract — a rename would corrupt every report.
func TestChangeKindString(t *testing.T) {
	cases := []struct {
		kind ChangeKind
		want string
	}{
		{Updated, "updated"},
		{Added, "added"},
		{Removed, "removed"},
		{ChangeKind(99), "kind(99)"}, // out-of-range fallback
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("ChangeKind(%d).String() = %q, want %q", int(tc.kind), got, tc.want)
		}
	}
}
