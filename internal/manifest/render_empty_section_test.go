package manifest

import (
	"bytes"
	"testing"
)

// TestRenderDropsSectionEmptiedByRevert covers Render's empty-section arm: when
// reverting a change empties a whole dependency section, the section key must be
// omitted from the output rather than emitted as an empty object. Here the new
// manifest's only devDependency is an Added change; with applied empty it is
// dropped, emptying the section.
func TestRenderDropsSectionEmptiedByRevert(t *testing.T) {
	old := mustParse(t, `{"name":"app","dependencies":{"left-pad":"1.0.0"}}`)
	new := mustParse(t, `{"name":"app","dependencies":{"left-pad":"1.0.0"},"devDependencies":{"eslint":"9.0.0"}}`)
	changes := Diff(old, new) // eslint added in devDependencies

	rendered, err := Render(new, changes, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rendered, []byte("devDependencies")) {
		t.Errorf("emptied devDependencies section was not omitted:\n%s", rendered)
	}
	if !bytes.Contains(rendered, []byte("left-pad")) {
		t.Errorf("Render dropped an unrelated dependency:\n%s", rendered)
	}
}
