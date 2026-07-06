package manifest

import (
	"reflect"
	"strings"
	"testing"
)

const baseRequirements = `# app dependencies (pip-compile style)
--no-index
--find-links wheels

requests>=2.28
Flask[async]==2.0.0 ; python_version >= '3.8'  # web framework
gone==0.3
bare-pkg
typing_extensions==4.0.0
urldep @ https://example.com/urldep-1.0-py3-none-any.whl
./local/path.whl
-r extra-requirements.txt
-e ./editable-pkg
multi==1.0 \
    ; python_version >= '3.9'
`

const newRequirements = `# app dependencies (pip-compile style)
--no-index
--find-links wheels

requests>=2.31
Flask[async]==3.0.0 ; python_version >= '3.8'  # web framework
added==2.0
bare-pkg
typing_extensions==4.0.0
urldep @ https://example.com/urldep-1.0-py3-none-any.whl
./local/path.whl
-r extra-requirements.txt
-e ./editable-pkg
multi==1.0 \
    ; python_version >= '3.9'
`

func mustParseReq(t *testing.T, data string) *Requirements {
	t.Helper()
	r, err := ParseRequirements([]byte(data))
	if err != nil {
		t.Fatalf("ParseRequirements: %v", err)
	}
	return r
}

// reqSections re-parses rendered requirements.txt into the version-spec view,
// so render assertions are robust to line placement.
func reqSections(t *testing.T, rendered []byte) map[string]string {
	t.Helper()
	r, err := ParseRequirements(rendered)
	if err != nil {
		t.Fatalf("re-parse rendered requirements.txt: %v\n%s", err, rendered)
	}
	return r.Sections[PyDependencies]
}

func TestParseRequirements(t *testing.T) {
	r := mustParseReq(t, baseRequirements)
	deps := r.Sections[PyDependencies]
	if deps["requests"] != ">=2.28" {
		t.Errorf("requests spec = %q", deps["requests"])
	}
	// Name is normalized (Flask -> flask), extras and marker travel with the
	// spec, and the trailing comment is stripped.
	if got := deps["flask"]; got != "[async]==2.0.0 ; python_version >= '3.8'" {
		t.Errorf("flask spec = %q", got)
	}
	// Underscores normalize to hyphens.
	if deps["typing-extensions"] != "==4.0.0" {
		t.Errorf("typing-extensions spec = %q", deps["typing-extensions"])
	}
	// A bare requirement is recorded with an empty spec.
	if v, ok := deps["bare-pkg"]; !ok || v != "" {
		t.Errorf("bare-pkg spec = %q present=%v, want empty present", v, ok)
	}
	// A backslash continuation joins into one logical requirement.
	if got := deps["multi"]; got != "==1.0     ; python_version >= '3.9'" {
		t.Errorf("multi spec = %q", got)
	}
	// Direct references, bare paths, and option lines are not bisectable.
	for _, name := range []string{"urldep", "local", "path-whl", "-r", "-e", "e"} {
		if _, ok := deps[name]; ok {
			t.Errorf("non-requirement entry %q should be skipped", name)
		}
	}
	if r.HasWorkspaceLayout() {
		t.Error("HasWorkspaceLayout = true; pip has no workspaces")
	}
}

func TestParseRequirementsEmptyAndCommentOnly(t *testing.T) {
	for _, data := range []string{"", "\n", "# only a comment\n\n   \n"} {
		r := mustParseReq(t, data)
		if len(r.Sections) != 0 {
			t.Errorf("Sections for %q = %v, want empty", data, r.Sections)
		}
	}
}

func TestParseRequirementsRejectsHashes(t *testing.T) {
	data := "requests==2.31.0 \\\n    --hash=sha256:deadbeef\n"
	_, err := ParseRequirements([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "--hash") {
		t.Fatalf("err = %v, want hash-checking mode rejection", err)
	}
	if !strings.Contains(err.Error(), "requests==2.31.0") {
		t.Errorf("err = %v, want the offending requirement named", err)
	}
}

func TestParseRequirementsSkipsPerRequirementOptions(t *testing.T) {
	r := mustParseReq(t, "tuned==1.0 --config-settings=--build-option=x\nplain==2.0\n")
	deps := r.Sections[PyDependencies]
	if _, ok := deps["tuned"]; ok {
		t.Error("requirement with per-requirement options should be skipped")
	}
	if deps["plain"] != "==2.0" {
		t.Errorf("plain spec = %q", deps["plain"])
	}
}

func TestParseRequirementsCRLF(t *testing.T) {
	r := mustParseReq(t, "requests==2.28\r\ngone==0.3\r\n")
	deps := r.Sections[PyDependencies]
	if deps["requests"] != "==2.28" || deps["gone"] != "==0.3" {
		t.Errorf("deps = %v", deps)
	}
}

func TestDiffRequirements(t *testing.T) {
	changes := DiffRequirements(mustParseReq(t, baseRequirements), mustParseReq(t, newRequirements))
	want := []Change{
		{Name: "added", Section: PyDependencies, Kind: Added, NewSpec: "==2.0"},
		{Name: "flask", Section: PyDependencies, Kind: Updated,
			OldSpec: "[async]==2.0.0 ; python_version >= '3.8'", NewSpec: "[async]==3.0.0 ; python_version >= '3.8'"},
		{Name: "gone", Section: PyDependencies, Kind: Removed, OldSpec: "==0.3"},
		{Name: "requests", Section: PyDependencies, Kind: Updated, OldSpec: ">=2.28", NewSpec: ">=2.31"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Errorf("Diff = %+v\nwant %+v", changes, want)
	}
}

func TestRenderRequirementsRevertsUnapplied(t *testing.T) {
	base := mustParseReq(t, baseRequirements)
	to := mustParseReq(t, newRequirements)
	changes := DiffRequirements(base, to)

	// Apply only the flask update; requests reverts, added is dropped, gone
	// comes back.
	rendered, err := RenderRequirements(to, changes, map[string]bool{"dependencies:flask": true})
	if err != nil {
		t.Fatalf("RenderRequirements: %v", err)
	}
	deps := reqSections(t, rendered)
	if deps["flask"] != "[async]==3.0.0 ; python_version >= '3.8'" {
		t.Errorf("applied flask spec = %q", deps["flask"])
	}
	if deps["requests"] != ">=2.28" {
		t.Errorf("reverted requests spec = %q", deps["requests"])
	}
	if _, ok := deps["added"]; ok {
		t.Error("reverted addition should be dropped")
	}
	if deps["gone"] != "==0.3" {
		t.Errorf("reverted removal spec = %q", deps["gone"])
	}

	// Untouched non-requirement lines survive verbatim.
	text := string(rendered)
	for _, line := range []string{
		"# app dependencies (pip-compile style)",
		"--no-index",
		"--find-links wheels",
		"urldep @ https://example.com/urldep-1.0-py3-none-any.whl",
		"./local/path.whl",
		"-r extra-requirements.txt",
		"-e ./editable-pkg",
		"multi==1.0 \\\n    ; python_version >= '3.9'",
	} {
		if !strings.Contains(text, line+"\n") {
			t.Errorf("rendered output lost %q:\n%s", line, text)
		}
	}
	// The rewritten line keeps the verbatim name token.
	if !strings.Contains(text, "requests>=2.28\n") {
		t.Errorf("reverted requests line not rewritten in place:\n%s", text)
	}
}

func TestRenderRequirementsAllApplied(t *testing.T) {
	base := mustParseReq(t, baseRequirements)
	to := mustParseReq(t, newRequirements)
	changes := DiffRequirements(base, to)
	applied := map[string]bool{}
	for _, c := range changes {
		applied[c.ID()] = true
	}
	rendered, err := RenderRequirements(to, changes, applied)
	if err != nil {
		t.Fatalf("RenderRequirements: %v", err)
	}
	if string(rendered) != newRequirements {
		t.Errorf("all-applied render should reproduce the target file verbatim:\n%s", rendered)
	}
}

func TestRenderRequirementsNoneApplied(t *testing.T) {
	base := mustParseReq(t, baseRequirements)
	to := mustParseReq(t, newRequirements)
	changes := DiffRequirements(base, to)
	rendered, err := RenderRequirements(to, changes, nil)
	if err != nil {
		t.Fatalf("RenderRequirements: %v", err)
	}
	if !reflect.DeepEqual(reqSections(t, rendered), base.Sections[PyDependencies]) {
		t.Errorf("none-applied render should match the base dependency view:\n%s", rendered)
	}
}

func TestRenderRequirementsRevertedUpdateWithoutLine(t *testing.T) {
	// A reverted update whose line vanished from the target manifest (only
	// possible with hand-built change lists, mirroring uv's behavior) appends
	// a normalized requirement instead of dropping the revert.
	to := mustParseReq(t, "other==1.0\n")
	changes := []Change{{Name: "ghost", Section: PyDependencies, Kind: Updated, OldSpec: "==0.9", NewSpec: "==1.0"}}
	rendered, err := RenderRequirements(to, changes, nil)
	if err != nil {
		t.Fatalf("RenderRequirements: %v", err)
	}
	if deps := reqSections(t, rendered); deps["ghost"] != "==0.9" {
		t.Errorf("ghost spec = %q, want appended ==0.9\n%s", deps["ghost"], rendered)
	}
}

func TestParseRequirementsPinsPropagatesHashError(t *testing.T) {
	if _, err := ParseRequirementsPins([]byte("a==1.0 --hash=sha256:x\n")); err == nil {
		t.Fatal("want the parser's hash rejection to propagate")
	}
}

func TestParseRequirementsPins(t *testing.T) {
	pins, err := ParseRequirementsPins([]byte(baseRequirements))
	if err != nil {
		t.Fatalf("ParseRequirementsPins: %v", err)
	}
	want := Resolved{
		"flask":             "2.0.0",
		"gone":              "0.3",
		"typing-extensions": "4.0.0",
		"multi":             "1.0",
	}
	if !reflect.DeepEqual(pins, want) {
		t.Errorf("pins = %v, want %v", pins, want)
	}
}

func TestExactPin(t *testing.T) {
	cases := []struct {
		spec string
		want string
		ok   bool
	}{
		{"==1.2.3", "1.2.3", true},
		{"== 1.2.3", "1.2.3", true},
		{"===1.2.3", "1.2.3", true},
		{"[extra]==1.2.3", "1.2.3", true},
		{"==1.2.3 ; python_version >= '3.8'", "1.2.3", true},
		{"==1.2.*", "", false},
		{"==1.2,<2", "", false},
		{">=1.2.3", "", false},
		{"", "", false},
		{"[broken", "", false},
	}
	for _, c := range cases {
		got, ok := exactPin(c.spec)
		if got != c.want || ok != c.ok {
			t.Errorf("exactPin(%q) = %q,%v want %q,%v", c.spec, got, ok, c.want, c.ok)
		}
	}
}

func TestLockfileOnlyRequirements(t *testing.T) {
	// Pins derive from the specs themselves, so identical specs can never
	// resolve differently; the seam method must report nothing even when fed
	// inconsistent Resolved maps.
	old := mustParseReq(t, "a==1.0\n")
	new := mustParseReq(t, "a==1.0\n")
	got := LockfileOnlyRequirements(old, new, Resolved{"a": "1.0"}, Resolved{"a": "2.0"})
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("lockfileOnly plumbing = %v, want the shared helper's verdict", got)
	}
	pinsOld, _ := ParseRequirementsPins([]byte("a==1.0\n"))
	pinsNew, _ := ParseRequirementsPins([]byte("a==1.0\n"))
	if got := LockfileOnlyRequirements(old, new, pinsOld, pinsNew); len(got) != 0 {
		t.Errorf("identical pins produced lockfile-only changes: %v", got)
	}
}

func TestIsValidPyName(t *testing.T) {
	valid := []string{"a", "requests", "typing_extensions", "zope.interface", "a2-b_c.d"}
	invalid := []string{"", "-a", "a-", ".a", "a.", "https://example.com/x.whl", "./local/path.whl", `C:\pkg`}
	for _, n := range valid {
		if !isValidPyName(n) {
			t.Errorf("isValidPyName(%q) = false, want true", n)
		}
	}
	for _, n := range invalid {
		if isValidPyName(n) {
			t.Errorf("isValidPyName(%q) = true, want false", n)
		}
	}
}
