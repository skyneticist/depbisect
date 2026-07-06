package manifest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const baseComposer = `{
  "name": "acme/demo",
  "type": "project",
  "require": {
    "php": ">=8.1",
    "monolog/monolog": "^2.9",
    "guzzlehttp/guzzle": "7.5.0",
    "gone/pkg": "^1.0"
  },
  "require-dev": {
    "phpunit/phpunit": "^9.6"
  },
  "config": {
    "sort-packages": true
  }
}
`

const newComposer = `{
  "name": "acme/demo",
  "type": "project",
  "require": {
    "php": ">=8.1",
    "monolog/monolog": "^3.0",
    "guzzlehttp/guzzle": "7.5.0",
    "symfony/console": "^6.3"
  },
  "require-dev": {
    "phpunit/phpunit": "^10.5"
  },
  "config": {
    "sort-packages": true
  }
}
`

func mustParseComposer(t *testing.T, data string) *ComposerJSON {
	t.Helper()
	c, err := ParseComposerJSON([]byte(data))
	if err != nil {
		t.Fatalf("ParseComposerJSON: %v", err)
	}
	return c
}

// composerSections re-parses rendered composer.json into the flattened
// name->spec view for one section, so render assertions are robust to JSON key
// ordering.
func composerSections(t *testing.T, rendered []byte, sec Section) map[string]string {
	t.Helper()
	c, err := ParseComposerJSON(rendered)
	if err != nil {
		t.Fatalf("re-parse rendered composer.json: %v\n%s", err, rendered)
	}
	return c.Sections[sec]
}

func TestParseComposerJSON(t *testing.T) {
	c := mustParseComposer(t, baseComposer)
	if c.Name != "acme/demo" {
		t.Errorf("Name = %q", c.Name)
	}
	req := c.Sections[ComposerRequire]
	if req["monolog/monolog"] != "^2.9" {
		t.Errorf("monolog spec = %q", req["monolog/monolog"])
	}
	// Platform requirements are kept like any other constraint.
	if req["php"] != ">=8.1" {
		t.Errorf("php constraint = %q, want kept", req["php"])
	}
	dev := c.Sections[ComposerRequireDev]
	if dev["phpunit/phpunit"] != "^9.6" {
		t.Errorf("phpunit spec = %q", dev["phpunit/phpunit"])
	}
	// Composer has no workspace concept.
	if c.HasWorkspaceLayout() {
		t.Error("HasWorkspaceLayout = true, want false for composer")
	}
}

func TestParseComposerJSONNoRequire(t *testing.T) {
	// A library composer.json with neither require nor require-dev must parse
	// with no sections rather than erroring.
	c := mustParseComposer(t, `{"name":"acme/lib","description":"x"}`)
	if len(c.Sections) != 0 {
		t.Errorf("sections = %v, want empty", c.Sections)
	}
}

func TestParseComposerJSONErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"invalid json", "{", "parse composer.json"},
		{"require not object", `{"require": "monolog/monolog"}`, "is not an object"},
		{"non-string constraint", `{"require": {"a/b": 1}}`, "non-string version constraint"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseComposerJSON([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestDiffComposer(t *testing.T) {
	got := DiffComposer(mustParseComposer(t, baseComposer), mustParseComposer(t, newComposer))
	want := []Change{
		{Name: "gone/pkg", Section: ComposerRequire, Kind: Removed, OldSpec: "^1.0"},
		{Name: "monolog/monolog", Section: ComposerRequire, Kind: Updated, OldSpec: "^2.9", NewSpec: "^3.0"},
		{Name: "phpunit/phpunit", Section: ComposerRequireDev, Kind: Updated, OldSpec: "^9.6", NewSpec: "^10.5"},
		{Name: "symfony/console", Section: ComposerRequire, Kind: Added, NewSpec: "^6.3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiffComposer:\n got %+v\nwant %+v", got, want)
	}
}

func TestRenderComposerRevertsUnapplied(t *testing.T) {
	to := mustParseComposer(t, newComposer)
	changes := DiffComposer(mustParseComposer(t, baseComposer), to)
	// Apply only the monolog update; every other change reverts to base.
	rendered, err := RenderComposer(to, changes, map[string]bool{"require:monolog/monolog": true})
	if err != nil {
		t.Fatalf("RenderComposer: %v", err)
	}
	req := composerSections(t, rendered, ComposerRequire)
	if req["monolog/monolog"] != "^3.0" {
		t.Errorf("monolog = %q, want applied ^3.0", req["monolog/monolog"])
	}
	if req["gone/pkg"] != "^1.0" {
		t.Errorf("gone/pkg = %q, want re-added ^1.0", req["gone/pkg"])
	}
	if _, ok := req["symfony/console"]; ok {
		t.Error("symfony/console should be removed when its change is unapplied")
	}
	// require-dev revert is independent of require.
	dev := composerSections(t, rendered, ComposerRequireDev)
	if dev["phpunit/phpunit"] != "^9.6" {
		t.Errorf("phpunit = %q, want reverted ^9.6", dev["phpunit/phpunit"])
	}
	// Untouched constraints and unrelated top-level fields survive.
	if req["php"] != ">=8.1" {
		t.Errorf("php = %q, want preserved", req["php"])
	}
	var doc struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rendered, &doc); err != nil {
		t.Fatalf("unmarshal rendered: %v", err)
	}
	if doc.Config["sort-packages"] != true {
		t.Errorf("unrelated config field not preserved: %v", doc.Config)
	}
}

func TestRenderComposerAllApplied(t *testing.T) {
	to := mustParseComposer(t, newComposer)
	changes := DiffComposer(mustParseComposer(t, baseComposer), to)
	applied := map[string]bool{}
	for _, c := range changes {
		applied[c.ID()] = true
	}
	rendered, err := RenderComposer(to, changes, applied)
	if err != nil {
		t.Fatalf("RenderComposer: %v", err)
	}
	req := composerSections(t, rendered, ComposerRequire)
	if req["monolog/monolog"] != "^3.0" || req["symfony/console"] != "^6.3" {
		t.Errorf("require = %v, want new state", req)
	}
	if _, ok := req["gone/pkg"]; ok {
		t.Error("gone/pkg should stay removed when all changes are applied")
	}
	dev := composerSections(t, rendered, ComposerRequireDev)
	if dev["phpunit/phpunit"] != "^10.5" {
		t.Errorf("phpunit = %q, want ^10.5", dev["phpunit/phpunit"])
	}
}

func TestRenderComposerDropsEmptiedSection(t *testing.T) {
	// A manifest whose only require-dev entry was added drops the whole
	// require-dev key when that addition is reverted.
	const base = `{"name":"a/b","require":{"a/b":"^1"}}`
	const head = `{"name":"a/b","require":{"a/b":"^1"},"require-dev":{"x/y":"^2"}}`
	to := mustParseComposer(t, head)
	changes := DiffComposer(mustParseComposer(t, base), to)
	rendered, err := RenderComposer(to, changes, nil) // apply nothing
	if err != nil {
		t.Fatalf("RenderComposer: %v", err)
	}
	if strings.Contains(string(rendered), "require-dev") {
		t.Errorf("emptied require-dev section should be dropped:\n%s", rendered)
	}
}

func TestParseComposerLock(t *testing.T) {
	const lock = `{
  "packages": [
    {"name": "monolog/monolog", "version": "2.9.1"},
    {"name": "guzzlehttp/guzzle", "version": "7.5.0"}
  ],
  "packages-dev": [
    {"name": "phpunit/phpunit", "version": "9.6.19"}
  ]
}`
	got, err := ParseComposerLock([]byte(lock))
	if err != nil {
		t.Fatalf("ParseComposerLock: %v", err)
	}
	if got["monolog/monolog"] != "2.9.1" {
		t.Errorf("monolog resolved = %q", got["monolog/monolog"])
	}
	// packages-dev resolves require-dev.
	if got["phpunit/phpunit"] != "9.6.19" {
		t.Errorf("phpunit resolved = %q", got["phpunit/phpunit"])
	}
}

func TestLockfileOnlyComposer(t *testing.T) {
	// monolog's constraint is identical in both manifests but its resolved
	// version differs — a lockfile-only change.
	old := mustParseComposer(t, baseComposer)
	new := mustParseComposer(t, baseComposer)
	got := LockfileOnlyComposer(old, new,
		Resolved{"monolog/monolog": "2.9.1"}, Resolved{"monolog/monolog": "2.9.3"})
	if len(got) != 1 || got[0].Name != "monolog/monolog" {
		t.Fatalf("LockfileOnlyComposer = %+v, want [monolog/monolog]", got)
	}
	if got[0].OldResolved != "2.9.1" || got[0].NewResolved != "2.9.3" {
		t.Errorf("resolved versions = %+v", got[0])
	}
}
