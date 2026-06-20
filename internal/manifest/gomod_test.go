package manifest

import "testing"

const baseGoMod = `module example.com/app

go 1.20

require (
	github.com/alpha/one v1.0.0
	github.com/beta/two v1.5.0
)

require github.com/indirect/dep v0.1.0 // indirect
`

const newGoMod = `module example.com/app

go 1.20

require (
	github.com/alpha/one v1.1.0
	github.com/beta/two v1.5.0
)

require github.com/indirect/dep v0.1.0 // indirect
`

func mustParseGoMod(t *testing.T, src string) *GoMod {
	t.Helper()
	g, err := ParseGoMod([]byte(src))
	if err != nil {
		t.Fatalf("ParseGoMod: %v", err)
	}
	return g
}

func TestParseGoModDirectOnly(t *testing.T) {
	g := mustParseGoMod(t, baseGoMod)
	if g.Name != "example.com/app" {
		t.Errorf("Name = %q, want example.com/app", g.Name)
	}
	req := g.Sections[GoRequire]
	if len(req) != 2 {
		t.Fatalf("require deps = %v, want 2 direct", req)
	}
	if req["github.com/alpha/one"] != "v1.0.0" {
		t.Errorf("alpha/one = %q, want v1.0.0", req["github.com/alpha/one"])
	}
	if _, ok := req["github.com/indirect/dep"]; ok {
		t.Error("indirect require should be skipped")
	}
}

func TestParseGoModSkipsReplaced(t *testing.T) {
	const src = `module example.com/app

go 1.20

require github.com/alpha/one v1.0.0

replace github.com/alpha/one => github.com/alpha/one v1.0.1
`
	g := mustParseGoMod(t, src)
	if _, ok := g.Sections[GoRequire]["github.com/alpha/one"]; ok {
		t.Error("replaced module should be skipped")
	}
}

func TestDiffGo(t *testing.T) {
	old := mustParseGoMod(t, baseGoMod)
	new := mustParseGoMod(t, newGoMod)
	changes := DiffGo(old, new)
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want 1", changes)
	}
	c := changes[0]
	if c.Name != "github.com/alpha/one" || c.Kind != Updated || c.OldSpec != "v1.0.0" || c.NewSpec != "v1.1.0" {
		t.Errorf("change = %+v", c)
	}
}

func TestRenderGoMod(t *testing.T) {
	new := mustParseGoMod(t, newGoMod)
	changes := DiffGo(mustParseGoMod(t, baseGoMod), new)
	id := changes[0].ID()

	// applied empty -> the change reverts to its old version.
	reverted, err := RenderGoMod(new, changes, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if v := mustParseGoMod(t, string(reverted)).Sections[GoRequire]["github.com/alpha/one"]; v != "v1.0.0" {
		t.Errorf("reverted alpha/one = %q, want v1.0.0", v)
	}

	// applied -> the change keeps its new version; untouched deps are intact.
	kept, err := RenderGoMod(new, changes, map[string]bool{id: true})
	if err != nil {
		t.Fatal(err)
	}
	keptReq := mustParseGoMod(t, string(kept)).Sections[GoRequire]
	if keptReq["github.com/alpha/one"] != "v1.1.0" {
		t.Errorf("kept alpha/one = %q, want v1.1.0", keptReq["github.com/alpha/one"])
	}
	if keptReq["github.com/beta/two"] != "v1.5.0" {
		t.Errorf("beta/two = %q, want v1.5.0", keptReq["github.com/beta/two"])
	}
}

func TestParseGoSum(t *testing.T) {
	const goSum = `github.com/alpha/one v1.0.0 h1:aaa
github.com/alpha/one v1.0.0/go.mod h1:bbb
github.com/beta/two v3.0.0 h1:ccc
github.com/beta/two v3.0.0/go.mod h1:ddd
`
	r, err := ParseGoSum([]byte(goSum))
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 2 {
		t.Fatalf("resolved = %v, want 2 entries (go.mod lines ignored)", r)
	}
	if r["github.com/alpha/one"] != "v1.0.0" || r["github.com/beta/two"] != "v3.0.0" {
		t.Errorf("resolved = %v", r)
	}
}
