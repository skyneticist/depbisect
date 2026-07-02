package execx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain lets this test binary act as a controllable subprocess when
// re-invoked with EXECX_HELPER set. This keeps subprocess tests hermetic.
func TestMain(m *testing.M) {
	if mode := os.Getenv("EXECX_HELPER"); mode != "" {
		helperMain(mode)
		return
	}
	os.Exit(m.Run())
}

func helperMain(mode string) {
	switch mode {
	case "args":
		// Print each argument NUL-separated so any byte except NUL survives.
		fmt.Print(strings.Join(os.Args[1:], "\x00"))
		os.Exit(0)
	case "exit":
		code, _ := strconv.Atoi(os.Args[1])
		fmt.Fprint(os.Stderr, "some stderr")
		os.Exit(code)
	case "cwd":
		wd, _ := os.Getwd()
		fmt.Print(wd)
		os.Exit(0)
	case "env":
		fmt.Print(os.Getenv(os.Args[1]))
		os.Exit(0)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "spew":
		n, _ := strconv.Atoi(os.Args[1])
		chunk := bytes.Repeat([]byte("x"), 1024)
		for i := 0; i < n; i++ {
			os.Stdout.Write(chunk)
			os.Stderr.Write(chunk)
		}
		fmt.Print("END")
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(2)
	}
}

func helperCmd(mode string, args ...string) Cmd {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return Cmd{
		Name:     exe,
		Args:     args,
		ExtraEnv: []string{"EXECX_HELPER=" + mode},
	}
}

func TestRunPreservesArgsExactly(t *testing.T) {
	args := []string{
		"plain",
		"has spaces",
		`"quoted"`,
		"$HOME and `backticks`",
		"semi;colon && friends | pipe",
		"new\nline",
		"unicode-éπ你好",
		"--flag=value with space",
		"", // empty argument must survive
	}
	res, err := Local{}.Run(context.Background(), helperCmd("args", args...))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(string(res.Stdout), "\x00")
	if len(got) != len(args) {
		t.Fatalf("got %d args %q, want %d", len(got), got, len(args))
	}
	for i := range args {
		if got[i] != args[i] {
			t.Errorf("arg %d = %q, want %q", i, got[i], args[i])
		}
	}
}

func TestRunExitCode(t *testing.T) {
	res, err := Local{}.Run(context.Background(), helperCmd("exit", "3"))
	if err != nil {
		t.Fatalf("nonzero exit must not be an error, got %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(string(res.Stderr), "some stderr") {
		t.Errorf("Stderr = %q", res.Stderr)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", res.Duration)
	}
}

func TestRunZeroExit(t *testing.T) {
	res, err := Local{}.Run(context.Background(), helperCmd("args", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestRunDirWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "has spaces & (chars)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c := helperCmd("cwd")
	c.Dir = dir
	res, err := Local{}.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	// Compare via Stat: macOS TMPDIR involves symlinks, so paths may differ.
	want, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, statErr := os.Stat(string(res.Stdout))
	if statErr != nil || !os.SameFile(want, got) {
		t.Fatalf("cwd = %q, stat err %v", res.Stdout, statErr)
	}
}

func TestRunExtraEnvAndInheritance(t *testing.T) {
	t.Setenv("EXECX_INHERITED", "from-parent")

	res, err := Local{}.Run(context.Background(), helperCmd("env", "EXECX_INHERITED"))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "from-parent" {
		t.Errorf("inherited env = %q", res.Stdout)
	}

	c := helperCmd("env", "EXECX_EXTRA")
	c.ExtraEnv = append(c.ExtraEnv, "EXECX_EXTRA=v with spaces")
	res, err = Local{}.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "v with spaces" {
		t.Errorf("extra env = %q", res.Stdout)
	}

	t.Setenv("EXECX_OVERRIDE", "old")
	c = helperCmd("env", "EXECX_OVERRIDE")
	c.ExtraEnv = append(c.ExtraEnv, "EXECX_OVERRIDE=new")
	res, err = Local{}.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "new" {
		t.Errorf("overridden env = %q, want new", res.Stdout)
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := Local{}.Run(ctx, helperCmd("sleep"))
	if err == nil {
		t.Fatal("expected error after cancellation")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("cancellation took %v, expected prompt termination", elapsed)
	}
}

func TestRunCommandNotFound(t *testing.T) {
	_, err := Local{}.Run(context.Background(), Cmd{Name: "depbisect-no-such-binary-xyz"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestRunOutputCapped(t *testing.T) {
	// 4 MiB of output per stream must be capped, keeping the tail.
	res, err := Local{}.Run(context.Background(), helperCmd("spew", "4096"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) > maxCapturedOutput {
		t.Errorf("Stdout len = %d, want <= %d", len(res.Stdout), maxCapturedOutput)
	}
	if !bytes.HasSuffix(res.Stdout, []byte("END")) {
		t.Error("capped output must keep the tail")
	}
}

// raceWriter records writes without any locking of its own; under -race it
// proves execx serializes Stream writes from the stdout and stderr pipes.
type raceWriter struct {
	n int
}

func (w *raceWriter) Write(p []byte) (int, error) {
	w.n += len(p) // would race if execx wrote concurrently from both pipes
	return len(p), nil
}

func TestRunStreamIsSynchronized(t *testing.T) {
	w := &raceWriter{}
	c := helperCmd("spew", "256")
	c.Stream = w
	res, err := Local{}.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	if w.n == 0 {
		t.Error("stream writer received no output")
	}
}

func TestFakeRunnerRecordsAndResponds(t *testing.T) {
	fake := NewFake()
	fake.Default = Response{Result: Result{ExitCode: 0}}
	fake.Stub(func(c Cmd) bool { return c.Name == "npm" }, Response{Result: Result{ExitCode: 1, Stderr: []byte("nope")}})

	res, err := fake.Run(context.Background(), Cmd{Name: "npm", Args: []string{"install"}, Dir: "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 1 || string(res.Stderr) != "nope" {
		t.Errorf("stubbed result = %+v", res)
	}

	res, err = fake.Run(context.Background(), Cmd{Name: "git", Args: []string{"status"}})
	if err != nil || res.ExitCode != 0 {
		t.Errorf("default result = %+v, %v", res, err)
	}

	calls := fake.Calls()
	if len(calls) != 2 || calls[0].Name != "npm" || calls[1].Name != "git" {
		t.Errorf("calls = %+v", calls)
	}
}

func TestFakeRunnerHonorsContext(t *testing.T) {
	fake := NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fake.Run(ctx, Cmd{Name: "x"}); err == nil {
		t.Fatal("expected context error")
	}
}

// TestFirstLine verifies the (string, bool) contract — especially that no
// sentinel string escapes to the caller and that blank-only input is signaled
// via the bool rather than a magic string.
func TestFirstLine(t *testing.T) {
	cases := []struct {
		name   string
		input  []byte
		want   string
		wantOK bool
	}{
		{"normal line", []byte("9.15.4\n"), "9.15.4", true},
		{"leading blank lines", []byte("\n\n1.2.3\n"), "1.2.3", true},
		{"trailing content ignored", []byte("first\nsecond\n"), "first", true},
		{"CRLF line endings", []byte("1.0.0\r\n"), "1.0.0", true},
		{"whitespace only", []byte("   \n\t\n  "), "", false},
		{"empty slice", []byte(nil), "", false},
		{"no trailing newline", []byte("1.0.0"), "1.0.0", true},
		{"surrounding whitespace trimmed", []byte("  trimmed  "), "trimmed", true},
		// Sentinel collision defense: a literal "no output" must be returned
		// with ok=true, not be mistaken for the absence of output.
		{"literal 'no output' string", []byte("no output\n"), "no output", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FirstLine(tc.input)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("FirstLine(%q) = (%q, %v), want (%q, %v)",
					tc.input, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestFirstLineOr(t *testing.T) {
	if got := FirstLineOr([]byte("error: install failed\ndetail"), "no output"); got != "error: install failed" {
		t.Errorf("content: got %q", got)
	}
	if got := FirstLineOr([]byte("   \n  \n"), "no output"); got != "no output" {
		t.Errorf("blank input: got %q, want fallback", got)
	}
}
