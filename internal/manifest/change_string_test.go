package manifest

import "testing"

// TestChangeStringVariants covers every arm of Change.String: updated/added/
// removed, the resolved-version preference when both lockfiles supplied one, and
// the trailing section suffix shown for non-default dependency sections.
func TestChangeStringVariants(t *testing.T) {
	cases := []struct {
		name string
		c    Change
		want string
	}{
		{
			"updated uses specs",
			Change{Name: "left-pad", Section: Dependencies, Kind: Updated, OldSpec: "1.0.0", NewSpec: "2.0.0"},
			"left-pad 1.0.0 -> 2.0.0",
		},
		{
			"updated prefers resolved over spec",
			Change{Name: "left-pad", Section: Dependencies, Kind: Updated, OldSpec: "^1", NewSpec: "^2", OldResolved: "1.0.0", NewResolved: "2.3.1"},
			"left-pad 1.0.0 -> 2.3.1",
		},
		{
			"non-default section gets a suffix",
			Change{Name: "eslint", Section: "devDependencies", Kind: Updated, OldSpec: "8.0.0", NewSpec: "9.0.0"},
			"eslint 8.0.0 -> 9.0.0 (devDependencies)",
		},
		{
			"added uses new spec",
			Change{Name: "chalk", Section: Dependencies, Kind: Added, NewSpec: "5.0.0"},
			"chalk 5.0.0 (added)",
		},
		{
			"added prefers resolved",
			Change{Name: "chalk", Section: Dependencies, Kind: Added, NewSpec: "^5", NewResolved: "5.3.0"},
			"chalk 5.3.0 (added)",
		},
		{
			"removed uses old spec",
			Change{Name: "underscore", Section: Dependencies, Kind: Removed, OldSpec: "1.13.0"},
			"underscore 1.13.0 (removed)",
		},
		{
			"removed prefers resolved and keeps suffix",
			Change{Name: "underscore", Section: "optionalDependencies", Kind: Removed, OldSpec: "^1", OldResolved: "1.13.6"},
			"underscore 1.13.6 (removed) (optionalDependencies)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
