package cli

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/skyneticist/depbisect/internal/pm"
)

// TestCompletionScriptsCoverAllFlags pins the completion scripts to the real
// flag set: every flag runFlagSet registers must be offered by both shells.
// This is the drift guard — adding a run flag without completion coverage
// fails here instead of shipping unnoticed.
func TestCompletionScriptsCoverAllFlags(t *testing.T) {
	scripts := map[string]string{"bash": bashCompletion, "zsh": zshCompletion}
	var opts runOptions
	var styleName string
	runFlagSet(&opts, &styleName).VisitAll(func(f *flag.Flag) {
		want := "--" + f.Name
		if len(f.Name) == 1 {
			want = "-" + f.Name
		}
		for shell, script := range scripts {
			if !strings.Contains(script, want) {
				t.Errorf("%s completion is missing the %s flag", shell, want)
			}
		}
	})
}

// TestCompletionScriptsCoverAllManagers pins the --pm value completion to the
// exact supported manager set in both shells. The uv omission shipped
// unnoticed because earlier checks compared flag names, not value lists.
func TestCompletionScriptsCoverAllManagers(t *testing.T) {
	managers := []pm.Manager{pm.NPM, pm.PNPM, pm.YARN, pm.CARGO, pm.GO, pm.UV, pm.COMPOSER, pm.PIP}

	extract := func(t *testing.T, script string, re *regexp.Regexp, shell string) []string {
		t.Helper()
		m := re.FindStringSubmatch(script)
		if m == nil {
			t.Fatalf("%s completion has no --pm value list", shell)
		}
		return strings.Fields(m[1])
	}
	lists := map[string][]string{
		"zsh":  extract(t, zshCompletion, regexp.MustCompile(`--pm\[[^\]]*\]:pm:\(([^)]*)\)`), "zsh"),
		"bash": extract(t, bashCompletion, regexp.MustCompile(`(?s)--pm\)\s*\n\s*COMPREPLY=\( \$\(compgen -W "([^"]*)"`), "bash"),
	}
	for shell, values := range lists {
		if len(values) != len(managers) {
			t.Errorf("%s --pm values = %v, want exactly the %d supported managers", shell, values, len(managers))
		}
		for _, m := range managers {
			found := false
			for _, v := range values {
				if v == string(m) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s --pm values %v are missing %q", shell, values, m)
			}
		}
	}
}

// TestCompletionScriptsStyleValues pins --style value completion in both
// shells to the styles parseOutputStyle accepts.
func TestCompletionScriptsStyleValues(t *testing.T) {
	for _, style := range []string{"modern", "classic"} {
		for shell, script := range map[string]string{"bash": bashCompletion, "zsh": zshCompletion} {
			if !strings.Contains(script, style) {
				t.Errorf("%s completion is missing the --style value %q", shell, style)
			}
		}
	}
}

// TestCompletionScriptsRefWiring pins the git-ref completion contract in both
// shells: --base and --to must route to the _depbisect_refs helper, and the
// helper must read refs via for-each-ref while honoring an earlier --repo.
func TestCompletionScriptsRefWiring(t *testing.T) {
	for shell, script := range map[string]string{"bash": bashCompletion, "zsh": zshCompletion} {
		for _, want := range []string{"_depbisect_refs", "for-each-ref", "--repo"} {
			if !strings.Contains(script, want) {
				t.Errorf("%s completion is missing %q", shell, want)
			}
		}
	}
	if !strings.Contains(bashCompletion, "--base|--to)") {
		t.Error("bash completion does not dispatch --base/--to to ref completion")
	}
	for _, spec := range []string{":rev:_depbisect_refs'", "'--to[target revision]:rev:_depbisect_refs'"} {
		if !strings.Contains(zshCompletion, spec) {
			t.Errorf("zsh completion is missing the ref action spec %q", spec)
		}
	}
}

// TestCompletionScriptsParse runs each script through its shell's syntax
// checker (bash -n / zsh -n), so a quoting or expansion typo in the embedded
// strings fails here rather than at the user's first tab press.
func TestCompletionScriptsParse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell syntax check is unix-only")
	}
	for shell, script := range map[string]string{"bash": bashCompletion, "zsh": zshCompletion} {
		bin, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("%s not on PATH; skipping its syntax check", shell)
			continue
		}
		path := filepath.Join(t.TempDir(), "completion."+shell)
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s -n rejected the completion script: %v\n%s", shell, err, out)
		}
	}
}

// TestBashRefCompletionFunctional drives the real bash completion function
// against a real git repository: sourcing the script, setting COMP_WORDS as
// bash would, and asserting COMPREPLY contains the repo's branches and tags —
// both from the repo's own directory and via an earlier --repo flag.
func TestBashRefCompletionFunctional(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash functional test is unix-only")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "x")
	git("tag", "v9.9.9")
	git("branch", "feature/ref-target")

	scriptPath := filepath.Join(t.TempDir(), "completion.bash")
	if err := os.WriteFile(scriptPath, []byte(bashCompletion), 0o644); err != nil {
		t.Fatal(err)
	}

	complete := func(t *testing.T, cwd string, words ...string) string {
		t.Helper()
		var b strings.Builder
		b.WriteString("source " + shellQuote(scriptPath) + "\n")
		b.WriteString("cd " + shellQuote(cwd) + "\n")
		b.WriteString("COMP_WORDS=(")
		for _, w := range words {
			b.WriteString(shellQuote(w) + " ")
		}
		// The word being completed is the empty final word.
		b.WriteString(`"")` + "\n")
		b.WriteString("COMP_CWORD=" + strconv.Itoa(len(words)) + "\n")
		b.WriteString("_depbisect\n")
		b.WriteString(`printf '%s\n' "${COMPREPLY[@]}"` + "\n")
		out, err := exec.Command(bash, "-c", b.String()).CombinedOutput()
		if err != nil {
			t.Fatalf("bash completion run failed: %v\n%s", err, out)
		}
		return string(out)
	}

	t.Run("from the repo directory", func(t *testing.T) {
		out := complete(t, repo, "depbisect", "run", "--base")
		for _, want := range []string{"main", "v9.9.9", "feature/ref-target"} {
			if !strings.Contains(out, want) {
				t.Errorf("COMPREPLY missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("via an earlier --repo flag", func(t *testing.T) {
		out := complete(t, t.TempDir(), "depbisect", "run", "--repo", repo, "--to")
		for _, want := range []string{"main", "v9.9.9", "feature/ref-target"} {
			if !strings.Contains(out, want) {
				t.Errorf("COMPREPLY missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("outside any repo completes nothing", func(t *testing.T) {
		out := complete(t, t.TempDir(), "depbisect", "run", "--base")
		if strings.TrimSpace(out) != "" {
			t.Errorf("expected empty COMPREPLY outside a repo, got:\n%s", out)
		}
	})
}

// shellQuote single-quotes s for safe interpolation into a bash -c script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestCompletionScriptRegistration pins the installation contracts:
//   - bash registers via `complete -F` so `source <(...)` works;
//   - zsh must handle BOTH routes: autoloaded from $fpath (the trailing
//     funcstack-guarded self-call) and sourced directly (compdef), because a
//     plain eval/source used to define the function and register nothing;
//   - the zsh run branch must shift the subcommand word before _arguments —
//     without it _arguments treats "run" as an unmatched positional and flag
//     completion silently offers nothing.
func TestCompletionScriptRegistration(t *testing.T) {
	if !strings.Contains(bashCompletion, "complete -F _depbisect depbisect") {
		t.Error("bash completion does not register with complete -F")
	}
	for _, want := range []string{
		`if [ "$funcstack[1]" = "_depbisect" ]`,
		"compdef _depbisect depbisect",
		"shift words",
		"(( CURRENT-- ))",
	} {
		if !strings.Contains(zshCompletion, want) {
			t.Errorf("zsh completion is missing %q", want)
		}
	}
}
