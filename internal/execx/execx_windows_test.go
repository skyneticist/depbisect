//go:build windows

package execx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeBatchHelper(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "helper.cmd")
	script := "@echo off\r\n" + body + "\r\n"
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunRejectsImplicitBatchCommand(t *testing.T) {
	dir := t.TempDir()
	writeBatchHelper(t, dir, "echo unsafe")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := Local{}.Run(context.Background(), Cmd{
		Name: "helper",
		Args: []string{"argument&echo injected"},
	})
	if err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("err = %v, want a batch-command safety error", err)
	}
}

func TestRunAllowsTrustedBatchCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "directory with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBatchHelper(t, dir, "echo %~1^|%~2")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := Local{}.Run(context.Background(), Cmd{
		Name:              "helper",
		Args:              []string{"install", "--no-audit"},
		AllowTrustedBatch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "install|--no-audit" {
		t.Fatalf("stdout = %q, want trusted arguments preserved", got)
	}
}

func TestRunRejectsUnsafeTrustedBatchArgument(t *testing.T) {
	dir := t.TempDir()
	writeBatchHelper(t, dir, "echo should-not-run")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := Local{}.Run(context.Background(), Cmd{
		Name:              "helper",
		Args:              []string{"install&echo injected"},
		AllowTrustedBatch: true,
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe argument") {
		t.Fatalf("err = %v, want an unsafe-argument error", err)
	}
}

func TestRunTrustedBatchDoesNotBypassErrDot(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	writeBatchHelper(t, dir, `type nul > "`+marker+`"`)
	t.Setenv("PATH", t.TempDir())

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	_, err = Local{}.Run(context.Background(), Cmd{
		Name:              "helper",
		Args:              []string{"install"},
		AllowTrustedBatch: true,
	})
	if err == nil {
		t.Fatal("expected current-directory command lookup to be rejected")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("current-directory batch command ran unexpectedly: %v", statErr)
	}
}

func TestRunCancellationIsPromptOnWindows(t *testing.T) {
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
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %v, want less than 3s", elapsed)
	}
}
