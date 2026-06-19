package checkpoint

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/skyneticist/depbisect/internal/engine"
)

func testCheckpoint() engine.Checkpoint {
	return engine.Checkpoint{
		SchemaVersion: engine.CheckpointSchemaVersion,
		Fingerprint: engine.CheckpointFingerprint{
			BaseSHA:               "base-sha",
			ToSHA:                 "target-sha",
			PackageManager:        "npm",
			PackageManagerVersion: "npm 10.8.2",
			Command:               []string{"npm", "test"},
			Runs:                  3,
			Changes:               []string{"dependencies:a", "dependencies:b"},
			Context:               "run-timeout=1m0s",
		},
		StartedAt: time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC),
	}
}

func TestFileStoreRoundTripAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint with spaces.jsonl")
	store := NewFileStore(path)
	cp := testCheckpoint()
	trials := []engine.Trial{
		{
			Role: "baseline-old", Outcome: "pass",
			PrepareDuration: 100 * time.Millisecond,
			InstallDuration: 600 * time.Millisecond,
			VerifyDuration:  300 * time.Millisecond,
			Duration:        time.Second,
		},
		{Role: "candidate", Applied: []string{"dependencies:b"}, Outcome: "fail", RunsExecuted: 3, Failures: 3},
	}

	if err := store.Start(cp); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("checkpoint permissions = %o, want 600", got)
		}
	}
	for _, trial := range trials {
		if err := store.Append(trial); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint.BaseSHA != cp.Fingerprint.BaseSHA ||
		len(got.Trials) != len(trials) ||
		got.Trials[1].Applied[0] != "dependencies:b" ||
		got.Trials[0].PrepareDuration != 100*time.Millisecond ||
		got.Trials[0].InstallDuration != 600*time.Millisecond ||
		got.Trials[0].VerifyDuration != 300*time.Millisecond {
		t.Fatalf("loaded checkpoint = %+v", got)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("checkpoint still exists after clear: %v", err)
	}
}

func TestFileStoreIgnoresCrashTruncatedFinalRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	store := NewFileStore(path)
	if err := store.Start(testCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(engine.Trial{Role: "baseline-old", Outcome: "pass"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"trial","trial":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Trials) != 1 {
		t.Fatalf("trials = %+v, want one complete record", got.Trials)
	}
}

func TestFileStoreRejectsCorruptInteriorRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	store := NewFileStore(path)
	if err := store.Start(testCheckpoint()); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{broken}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{broken-again}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load(); err == nil {
		t.Fatal("expected corrupt checkpoint error")
	}
}

// TestFileStoreLoadRejectsInvalidStructure pins the structural invariants Load
// enforces on a syntactically valid but logically malformed checkpoint file:
// exactly one leading header, every trial preceded by it, and only known record
// types. A regression here would silently resume from a corrupt checkpoint.
func TestFileStoreLoadRejectsInvalidStructure(t *testing.T) {
	header := `{"type":"checkpoint","checkpoint":{"schemaVersion":1}}` + "\n"
	cases := []struct {
		name string
		body string
		want string
	}{
		{"empty file", "", "no complete header"},
		{"only blank lines", "\n   \n\t\n", "no complete header"},
		{"header missing payload", `{"type":"checkpoint"}` + "\n", "invalid checkpoint header"},
		{"two headers", header + header, "invalid checkpoint header"},
		{"trial before header", `{"type":"trial","trial":{"role":"x"}}` + "\n", "trial before checkpoint header"},
		{"trial missing payload", header + `{"type":"trial"}` + "\n", "trial before checkpoint header"},
		{"unknown record type", header + `{"type":"bogus"}` + "\n", "unknown checkpoint record type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewFileStore(path).Load()
			if err == nil {
				t.Fatalf("Load() succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load() error = %q, want substring %q", err, tc.want)
			}
		})
	}
}

// TestFileStoreLoadMissingFile reports a not-exist error when asked to resume a
// checkpoint that was never started.
func TestFileStoreLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.jsonl")
	if _, err := NewFileStore(path).Load(); !os.IsNotExist(err) {
		t.Fatalf("Load() of a missing checkpoint = %v, want a not-exist error", err)
	}
}

// TestFileStoreAppendRequiresPriorStart documents that Append opens the file
// without O_CREATE: a trial can only be recorded after Start has written the
// header, never to a missing file.
func TestFileStoreAppendRequiresPriorStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	err := NewFileStore(path).Append(engine.Trial{Role: "candidate"})
	if err == nil {
		t.Fatal("Append without a prior Start succeeded, want error")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Append error = %v, want a not-exist error", err)
	}
}

// TestFileStoreStartFailsWhenParentMissing surfaces the open error when the
// checkpoint's parent directory does not exist.
func TestFileStoreStartFailsWhenParentMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "checkpoint.jsonl")
	if err := NewFileStore(path).Start(testCheckpoint()); err == nil {
		t.Fatal("Start under a missing parent directory succeeded, want error")
	}
}

// TestFileStoreClearPropagatesNonAbsenceErrors verifies Clear swallows only
// ErrNotExist (already gone) and surfaces any other removal failure. A
// non-empty directory at the checkpoint path makes os.Remove fail portably.
func TestFileStoreClearPropagatesNonAbsenceErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewFileStore(path).Clear(); err == nil {
		t.Fatal("Clear of a non-empty directory succeeded, want error")
	}
}

// TestFileStoreClearIsIdempotent verifies clearing an already-absent checkpoint
// is a no-op rather than an error: the engine calls Clear on a successful run
// whether or not a checkpoint file was ever written.
func TestFileStoreClearIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never-created.jsonl")
	if err := NewFileStore(path).Clear(); err != nil {
		t.Fatalf("Clear of an absent checkpoint = %v, want nil", err)
	}
}
