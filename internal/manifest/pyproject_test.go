package manifest

import (
	"reflect"
	"strings"
	"testing"
)

const basePyproject = `[project]
name = "demo"
version = "0.1.0"
requires-python = ">=3.9"
dependencies = [
    "requests>=2.28",
    "Flask[async]>=2.0; python_version >= '3.8'",
    "gone==0.3",
    "bare-pkg",
    "urldep @ https://example.com/urldep-1.0-py3-none-any.whl",
]
`

const newPyproject = `[project]
name = "demo"
version = "0.1.0"
requires-python = ">=3.9"
dependencies = [
    "requests>=2.31",
    "Flask[async]>=3.0; python_version >= '3.8'",
    "added==2.0",
    "bare-pkg",
    "urldep @ https://example.com/urldep-1.0-py3-none-any.whl",
]
`

func mustParsePy(t *testing.T, data string) *PyProject {
	t.Helper()
	p, err := ParsePyproject([]byte(data))
	if err != nil {
		t.Fatalf("ParsePyproject: %v", err)
	}
	return p
}

// pySections re-parses rendered pyproject.toml into the version-spec view, so
// render assertions are robust to TOML formatting and array ordering.
func pySections(t *testing.T, rendered []byte) map[string]string {
	t.Helper()
	p, err := ParsePyproject(rendered)
	if err != nil {
		t.Fatalf("re-parse rendered pyproject.toml: %v\n%s", err, rendered)
	}
	return p.Sections[PyDependencies]
}

func TestParsePyproject(t *testing.T) {
	p := mustParsePy(t, basePyproject)
	if p.Name != "demo" {
		t.Errorf("Name = %q", p.Name)
	}
	deps := p.Sections[PyDependencies]
	if deps["requests"] != ">=2.28" {
		t.Errorf("requests spec = %q", deps["requests"])
	}
	// Name is normalized (Flask -> flask) and extras+marker travel with the spec.
	if got := deps["flask"]; got != "[async]>=2.0; python_version >= '3.8'" {
		t.Errorf("flask spec = %q", got)
	}
	if deps["gone"] != "==0.3" {
		t.Errorf("gone spec = %q", deps["gone"])
	}
	// A bare requirement is recorded with an empty spec.
	if v, ok := deps["bare-pkg"]; !ok || v != "" {
		t.Errorf("bare-pkg spec = %q present=%v, want empty present", v, ok)
	}
	// A direct URL reference carries no bisectable version and is skipped.
	if _, ok := deps["urldep"]; ok {
		t.Error("URL reference dependency should be skipped")
	}
	if p.HasWorkspace {
		t.Error("HasWorkspace = true for a plain project")
	}
}

func TestParsePyprojectWorkspace(t *testing.T) {
	p := mustParsePy(t, "[tool.uv.workspace]\nmembers = [\"packages/*\"]\n")
	if !p.HasWorkspace {
		t.Error("HasWorkspace = false, want true for [tool.uv.workspace]")
	}
}

func TestParsePyprojectNoProjectTable(t *testing.T) {
	// A manifest without a [project] table has nothing to bisect but must parse.
	p := mustParsePy(t, "[build-system]\nrequires = [\"hatchling\"]\n")
	if len(p.Sections) != 0 {
		t.Errorf("sections = %v, want empty", p.Sections)
	}
}

func TestParsePyprojectErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"invalid toml", "[project", "parse pyproject.toml"},
		{"deps not array", "[project]\ndependencies = \"requests\"\n", "not an array"},
		{"dep entry not string", "[project]\ndependencies = [123]\n", "not a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePyproject([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestSplitRequirement(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantSpec string
		wantOK   bool
	}{
		{"requests>=2.28", "requests", ">=2.28", true},
		{"Flask", "flask", "", true},
		{"ruff [lint] == 0.1", "ruff", "[lint] == 0.1", true},
		{"zope.interface>=5", "zope-interface", ">=5", true},
		{"foo_bar.baz", "foo-bar-baz", "", true},
		{"urldep @ https://x/y.whl", "", "", false},
		{"   ", "", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			name, _, spec, ok := splitRequirement(tc.in)
			if ok != tc.wantOK || name != tc.wantName || spec != tc.wantSpec {
				t.Errorf("splitRequirement(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.in, name, spec, ok, tc.wantName, tc.wantSpec, tc.wantOK)
			}
		})
	}
}

func TestNormalizePyName(t *testing.T) {
	cases := map[string]string{
		"Django":         "django",
		"zope.interface": "zope-interface",
		"a__b":           "a-b",
		"PyYAML":         "pyyaml",
		"already-norm":   "already-norm",
		"Foo.._-Bar":     "foo-bar",
	}
	for in, want := range cases {
		if got := normalizePyName(in); got != want {
			t.Errorf("normalizePyName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiffPyproject(t *testing.T) {
	got := DiffPyproject(mustParsePy(t, basePyproject), mustParsePy(t, newPyproject))
	want := []Change{
		{Name: "added", Section: PyDependencies, Kind: Added, NewSpec: "==2.0"},
		{Name: "flask", Section: PyDependencies, Kind: Updated,
			OldSpec: "[async]>=2.0; python_version >= '3.8'", NewSpec: "[async]>=3.0; python_version >= '3.8'"},
		{Name: "gone", Section: PyDependencies, Kind: Removed, OldSpec: "==0.3"},
		{Name: "requests", Section: PyDependencies, Kind: Updated, OldSpec: ">=2.28", NewSpec: ">=2.31"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiffPyproject:\n got %+v\nwant %+v", got, want)
	}
}

func TestRenderPyprojectRevertsUnapplied(t *testing.T) {
	to := mustParsePy(t, newPyproject)
	changes := DiffPyproject(mustParsePy(t, basePyproject), to)
	// Apply only the requests update; every other change reverts to base.
	rendered, err := RenderPyproject(to, changes, map[string]bool{"dependencies:requests": true})
	if err != nil {
		t.Fatalf("RenderPyproject: %v", err)
	}
	deps := pySections(t, rendered)
	if deps["requests"] != ">=2.31" {
		t.Errorf("requests = %q, want applied >=2.31", deps["requests"])
	}
	if deps["flask"] != "[async]>=2.0; python_version >= '3.8'" {
		t.Errorf("flask = %q, want reverted spec with extras+marker", deps["flask"])
	}
	if deps["gone"] != "==0.3" {
		t.Errorf("gone = %q, want re-added ==0.3", deps["gone"])
	}
	if _, ok := deps["added"]; ok {
		t.Error("added dependency should be removed when its change is unapplied")
	}
	// Untouched requirements — including the skipped URL reference — survive.
	if _, ok := deps["bare-pkg"]; !ok {
		t.Error("bare-pkg should be preserved")
	}
	if !strings.Contains(string(rendered), "urldep @ https://example.com/urldep-1.0-py3-none-any.whl") {
		t.Errorf("URL reference dependency not preserved in rendered output:\n%s", rendered)
	}
}

func TestRenderPyprojectAllApplied(t *testing.T) {
	to := mustParsePy(t, newPyproject)
	changes := DiffPyproject(mustParsePy(t, basePyproject), to)
	applied := map[string]bool{}
	for _, c := range changes {
		applied[c.ID()] = true
	}
	rendered, err := RenderPyproject(to, changes, applied)
	if err != nil {
		t.Fatalf("RenderPyproject: %v", err)
	}
	deps := pySections(t, rendered)
	want := map[string]string{"requests": ">=2.31", "flask": "[async]>=3.0; python_version >= '3.8'", "added": "==2.0"}
	for name, v := range want {
		if deps[name] != v {
			t.Errorf("%s = %q, want %q", name, deps[name], v)
		}
	}
	if _, ok := deps["gone"]; ok {
		t.Error("gone should stay removed when all changes are applied")
	}
}

func TestParseUvLock(t *testing.T) {
	const lock = `version = 1
requires-python = ">=3.9"

[[package]]
name = "requests"
version = "2.31.0"

[[package]]
name = "Flask"
version = "3.0.0"

[[package]]
name = "demo"
version = "0.1.0"
`
	got, err := ParseUvLock([]byte(lock))
	if err != nil {
		t.Fatalf("ParseUvLock: %v", err)
	}
	if got["requests"] != "2.31.0" {
		t.Errorf("requests resolved = %q", got["requests"])
	}
	// uv.lock names are normalized to match manifest keys (Flask -> flask).
	if got["flask"] != "3.0.0" {
		t.Errorf("flask resolved = %q (normalized?)", got["flask"])
	}
}

func TestLockfileOnlyPyproject(t *testing.T) {
	// requests' spec is identical in both manifests but its resolved version
	// differs — a lockfile-only change.
	old := mustParsePy(t, basePyproject)
	new := mustParsePy(t, basePyproject)
	got := LockfileOnlyPyproject(old, new, Resolved{"requests": "2.28.0"}, Resolved{"requests": "2.31.0"})
	if len(got) != 1 || got[0].Name != "requests" {
		t.Fatalf("LockfileOnlyPyproject = %+v, want [requests]", got)
	}
	if got[0].OldResolved != "2.28.0" || got[0].NewResolved != "2.31.0" {
		t.Errorf("resolved versions = %+v", got[0])
	}
}
