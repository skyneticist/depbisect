package checkpoint

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// ---------------------------------------------------------------------------
// Filesystem safety: behavioral invariants
// ---------------------------------------------------------------------------

// TestFileStoreStartNormalizesPermissionsOnExistingFile verifies that Start
// tightens permissions to 0o600 even when the checkpoint file already exists
// with broader access. Without this, a manually-edited or incorrectly-umask'd
// file could expose trial data to other users on the same machine.
func TestFileStoreStartNormalizesPermissionsOnExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions not enforced on Windows")
	}
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")

	// Pre-create the file with overly-broad permissions.
	if err := os.WriteFile(path, []byte("old data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewFileStore(path).Start(testCheckpoint()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("permissions after Start = %o, want 0o600 (data exposed to other users)", got)
	}
}

// TestFileStoreStartTruncatesExistingCheckpoint verifies that calling Start
// on an already-existing checkpoint file completely replaces it. Residual
// trial records from a previous run must not survive a fresh Start; if they
// did, a resumed run would re-apply already-tested combinations.
func TestFileStoreStartTruncatesExistingCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	store := NewFileStore(path)
	cp := testCheckpoint()

	// First run: start and record two trials.
	if err := store.Start(cp); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"baseline-old", "baseline-new"} {
		if err := store.Append(engine.Trial{Role: role, Outcome: "pass"}); err != nil {
			t.Fatal(err)
		}
	}

	// Second run: Start again with a different fingerprint.
	cp2 := cp
	cp2.Fingerprint.BaseSHA = "new-base-sha"
	if err := store.Start(cp2); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint.BaseSHA != "new-base-sha" {
		t.Errorf("BaseSHA = %q, want new-base-sha (old header persisted after re-Start)", got.Fingerprint.BaseSHA)
	}
	if len(got.Trials) != 0 {
		t.Errorf("Trials = %v, want none (old trials persisted after re-Start)", got.Trials)
	}
}

// TestFileStoreStartStripsInputTrials verifies that any Trials present on the
// Checkpoint passed to Start are NOT written to the header record. If they
// were, Load would see them twice: once from the header and once from the
// Append records, corrupting the resume state.
func TestFileStoreStartStripsInputTrials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	store := NewFileStore(path)

	// Pass a checkpoint with pre-populated Trials (as the engine might
	// construct when collecting results before calling Start).
	cp := testCheckpoint()
	cp.Trials = []engine.Trial{
		{Role: "candidate", Outcome: "fail"},
		{Role: "candidate", Outcome: "pass"},
	}

	if err := store.Start(cp); err != nil {
		t.Fatal(err)
	}
	// Append one real trial after Start.
	if err := store.Append(engine.Trial{Role: "baseline-old", Outcome: "pass"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Trials) != 1 {
		t.Errorf("Trials = %v; Start should strip pre-existing Trials from the header record, leaving only Appended ones", got.Trials)
	}
	if got.Trials[0].Role != "baseline-old" {
		t.Errorf("unexpected trial role %q after round-trip", got.Trials[0].Role)
	}
}

// TestFileStoreConcurrentAppendAllRecordsLand verifies that simultaneous
// Append calls from multiple goroutines all produce complete, parseable
// records. The store relies on O_APPEND for atomicity; this test would
// surface any record interleaving or partial-write corruption.
func TestFileStoreConcurrentAppendAllRecordsLand(t *testing.T) {
	const goroutines = 20
	path := filepath.Join(t.TempDir(), "concurrent.jsonl")
	store := NewFileStore(path)

	if err := store.Start(testCheckpoint()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			trial := engine.Trial{
				Role:    "candidate",
				Applied: []string{strings.Repeat("x", n+1)},
				Outcome: "fail",
			}
			if err := store.Append(trial); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("Append error: %v", err)
	}
	if t.Failed() {
		return
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load after concurrent Appends: %v", err)
	}
	if len(got.Trials) != goroutines {
		t.Errorf("got %d trials, want %d (some concurrent Appends were lost or corrupted)", len(got.Trials), goroutines)
	}
}

// TestFileStoreAppendAfterFileDeletion verifies that Append returns a clear,
// actionable error (os.ErrNotExist) when the checkpoint file is removed
// between Start and the first Append. The engine must surface this rather than
// silently losing trial data.
func TestFileStoreAppendAfterFileDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	store := NewFileStore(path)

	if err := store.Start(testCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	err := store.Append(engine.Trial{Role: "baseline-old", Outcome: "pass"})
	if err == nil {
		t.Fatal("Append after file deletion succeeded; expected an error")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Append error = %v; want a not-exist error so callers know the checkpoint is gone", err)
	}
}

// TestFileStorePathIsExactAndExclusive verifies that Start and Append write
// only to the configured path and create no additional files in its directory.
// A store that spills temp files or lock files could interfere with the user's
// working tree.
func TestFileStorePathIsExactAndExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.jsonl")
	store := NewFileStore(path)

	if err := store.Start(testCheckpoint()); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(engine.Trial{Role: "baseline-old", Outcome: "pass"}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "checkpoint.jsonl" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dir contains %v; want only [checkpoint.jsonl] (store must not create side files)", names)
	}
}

// ---------------------------------------------------------------------------
// Error-path coverage
// ---------------------------------------------------------------------------

// TestWriteRecordFailsOnWriteError verifies that writeRecord propagates a
// write failure rather than swallowing it. A write error means the trial
// record was not durably stored; the caller must see the error so it can
// surface it and prevent a false "successfully resumed" state.
//
// The test uses the read end of an os.Pipe as the target: writing to a
// read-only file descriptor returns EBADF on POSIX systems, triggering the
// error path without requiring a failing storage device.
func TestWriteRecordFailsOnWriteError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pipe write-to-read-end behavior differs on Windows")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	rec := record{Type: "trial", Trial: &engine.Trial{Role: "candidate", Outcome: "fail"}}
	if err := writeRecord(r, rec); err == nil {
		t.Fatal("writeRecord to read-end of pipe succeeded; expected a write error")
	}
}

// TestFileStoreStartChmodErrorReturnsErr verifies that Start returns the error
// if Chmod fails rather than continuing with incorrect permissions. Continuing
// would leave the file accessible to other users on a shared machine.
//
// /dev/null is used because it is owned by root; a non-root process cannot
// fchmod it, so the Chmod call reliably fails with EPERM without any
// I/O-device side-effects.
func TestFileStoreStartChmodErrorReturnsErr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Chmod semantics not applicable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root: Chmod on any file succeeds, cannot trigger EPERM")
	}

	store := NewFileStore("/dev/null")
	if err := store.Start(testCheckpoint()); err == nil {
		t.Fatal("Start with a root-owned path succeeded; expected Chmod to fail with EPERM")
	}
}

// TestFileStoreAppendWriteErrorReturnsErr verifies that Append surfaces a
// write failure so the engine can stop and report it rather than continuing
// with an incomplete checkpoint that would silently corrupt a future --resume.
//
// This test relies on /dev/full, a Linux character device that returns ENOSPC
// on every write. On platforms where /dev/full is absent the test is skipped;
// it runs reliably on the Linux CI environment.
func TestFileStoreAppendWriteErrorReturnsErr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not applicable on Windows")
	}
	if _, err := os.Stat("/dev/full"); os.IsNotExist(err) {
		t.Skip("/dev/full not available on this platform (present on Linux CI)")
	}

	// Point the store directly at /dev/full. Start is bypassed deliberately:
	// Append's OpenFile uses only O_APPEND|O_WRONLY (no O_CREATE), so it will
	// open the device, and the subsequent Write will return ENOSPC.
	store := NewFileStore("/dev/full")
	err := store.Append(engine.Trial{Role: "baseline-old", Outcome: "pass"})
	if err == nil {
		t.Fatal("Append to /dev/full succeeded; expected ENOSPC write error")
	}
}
