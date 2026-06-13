package checkpoint

import (
	"os"
	"path/filepath"
	"runtime"
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
