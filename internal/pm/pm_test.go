package pm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/skyneticist/depbisect/internal/execx"
	"golang.org/x/mod/module"
	"golang.org/x/mod/zip"
)

func TestManagerValid(t *testing.T) {
	valid := []Manager{NPM, PNPM, CARGO, GO, UV}
	for _, m := range valid {
		if !m.Valid() {
			t.Errorf("Manager(%q).Valid() = false, want true", m)
		}
	}
	// Zero value and unknown strings must be invalid.
	for _, bad := range []Manager{"", "yarn", "bun", "pip"} {
		if bad.Valid() {
			t.Errorf("Manager(%q).Valid() = true, want false", bad)
		}
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name        string
		npmLock     bool
		pnpmLock    bool
		override    string
		want        Manager
		wantErrPart string
	}{
		{name: "npm lockfile", npmLock: true, want: NPM},
		{name: "pnpm lockfile", pnpmLock: true, want: PNPM},
		{name: "both ambiguous", npmLock: true, pnpmLock: true, wantErrPart: "--pm"},
		{name: "both with override", npmLock: true, pnpmLock: true, override: "pnpm", want: PNPM},
		{name: "neither", wantErrPart: "lockfile"},
		{name: "neither with override", override: "npm", want: NPM},
		{name: "cargo override", override: "cargo", want: CARGO},
		{name: "go override", override: "go", want: GO},
		{name: "uv override", override: "uv", want: UV},
		{name: "bad override", override: "yarn", wantErrPart: "yarn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Detect(tc.npmLock, tc.pnpmLock, tc.override)
			if tc.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrPart) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLockfileNames(t *testing.T) {
	if NPM.LockfileName() != "package-lock.json" || PNPM.LockfileName() != "pnpm-lock.yaml" ||
		CARGO.LockfileName() != "Cargo.lock" || GO.LockfileName() != "go.sum" ||
		UV.LockfileName() != "uv.lock" {
		t.Error("wrong lockfile names")
	}
}

func TestManifestNames(t *testing.T) {
	if NPM.ManifestName() != "package.json" || PNPM.ManifestName() != "package.json" ||
		CARGO.ManifestName() != "Cargo.toml" || GO.ManifestName() != "go.mod" ||
		UV.ManifestName() != "pyproject.toml" {
		t.Error("wrong manifest names")
	}
}

func TestInstallInvocation(t *testing.T) {
	// Each manager gets its own subtest so failures are isolated and all managers
	// are exercised even when one assertion fails.

	t.Run("pnpm", func(t *testing.T) {
		dir := t.TempDir()
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: PNPM}).Install(context.Background(), dir, nil); err != nil {
			t.Fatal(err)
		}
		c := fake.Calls()[0]
		if c.Name != "pnpm" || c.Dir != dir {
			t.Errorf("cmd = %+v, want name=pnpm dir=%s", c, dir)
		}
		if !c.AllowTrustedBatch {
			t.Error("pnpm invocation must set AllowTrustedBatch for Windows batch shims")
		}
		// pnpm enables --frozen-lockfile automatically when CI=true; DepBisect
		// must disable it because candidate manifests intentionally disagree with
		// the lockfile.
		if !strings.Contains(strings.Join(c.Args, " "), "--no-frozen-lockfile") {
			t.Errorf("pnpm args missing --no-frozen-lockfile: %v", c.Args)
		}
	})

	t.Run("npm", func(t *testing.T) {
		dir := t.TempDir()
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: NPM}).Install(context.Background(), dir, nil); err != nil {
			t.Fatal(err)
		}
		c := fake.Calls()[0]
		if c.Name != "npm" || c.Args[0] != "install" {
			t.Errorf("npm cmd = %+v, want name=npm args[0]=install", c)
		}
		if len(c.ExtraEnv) != 0 {
			t.Errorf("npm must not inject extra env, got %v", c.ExtraEnv)
		}
	})

	t.Run("cargo", func(t *testing.T) {
		dir := t.TempDir()
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: CARGO}).Install(context.Background(), dir, nil); err != nil {
			t.Fatal(err)
		}
		c := fake.Calls()[0]
		if c.Name != "cargo" || !reflect.DeepEqual(c.Args, []string{"fetch"}) {
			t.Errorf("cargo cmd = %+v, want name=cargo args=[fetch]", c)
		}
		if !c.AllowTrustedBatch {
			t.Error("cargo invocation must set AllowTrustedBatch for Windows batch shims")
		}
	})

	t.Run("go", func(t *testing.T) {
		dir := t.TempDir()
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: GO}).Install(context.Background(), dir, nil); err != nil {
			t.Fatal(err)
		}
		c := fake.Calls()[0]
		if c.Name != "go" || !reflect.DeepEqual(c.Args, []string{"mod", "download", "all"}) {
			t.Errorf("go cmd = %+v, want name=go args=[mod download all]", c)
		}
		// -mod=mod is merged into GOFLAGS at call time so that any other flags
		// the user has in GOFLAGS (e.g. -tags=integration) are preserved.
		if len(c.ExtraEnv) != 1 || !strings.HasPrefix(c.ExtraEnv[0], "GOFLAGS=") || !strings.Contains(c.ExtraEnv[0], "-mod=mod") {
			t.Errorf("go ExtraEnv = %v, want GOFLAGS containing -mod=mod", c.ExtraEnv)
		}
	})

	t.Run("uv", func(t *testing.T) {
		dir := t.TempDir()
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: UV}).Install(context.Background(), dir, nil); err != nil {
			t.Fatal(err)
		}
		c := fake.Calls()[0]
		if c.Name != "uv" || !reflect.DeepEqual(c.Args, []string{"lock"}) {
			t.Errorf("uv cmd = %+v, want name=uv args=[lock]", c)
		}
		if !c.AllowTrustedBatch {
			t.Error("uv invocation must set AllowTrustedBatch for Windows batch shims")
		}
	})
}

// found is a LookPath stub that resolves any manager to a fake absolute path.
func found(name string) (string, error) { return "/bin/" + name, nil }

func TestVersion(t *testing.T) {
	cases := []struct {
		name string
		// lookPath stubs PATH resolution; nil means "found".
		lookPath func(string) (string, error)
		// stub configures the fake runner's response to `pnpm --version`.
		stub func(*execx.Fake)
		// wantID is the expected identity on success ("" when wantErr is set).
		wantID string
		// wantErr, when non-empty, is a substring the error must contain.
		wantErr string
	}{
		{
			name:   "reports identity from version output",
			stub:   func(f *execx.Fake) { f.Default.Result = execx.Result{Stdout: []byte("9.15.4\n")} },
			wantID: "pnpm 9.15.4",
		},
		{
			name:     "not found on PATH",
			lookPath: func(string) (string, error) { return "", errors.New("not found") },
			wantErr:  "not found on PATH",
		},
		{
			name:    "runner failure",
			stub:    func(f *execx.Fake) { f.Default.Err = errors.New("spawn failed") },
			wantErr: "inspect package manager",
		},
		{
			name: "nonzero exit surfaces stderr",
			stub: func(f *execx.Fake) {
				f.Default.Result = execx.Result{ExitCode: 1, Stderr: []byte("corepack is disabled\n")}
			},
			wantErr: "corepack is disabled",
		},
		{
			name:    "empty output is an error",
			stub:    func(f *execx.Fake) { f.Default.Result = execx.Result{Stdout: []byte("   \n")} },
			wantErr: "produced no output",
		},
		{
			// When the PM exits non-zero with no stderr, the error must still
			// be well-formed — no trailing ": " from an empty format argument.
			name: "nonzero exit with empty stderr is well-formed",
			stub: func(f *execx.Fake) {
				f.Default.Result = execx.Result{ExitCode: 2, Stderr: []byte("")}
			},
			wantErr: "exit 2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := execx.NewFake()
			if tc.stub != nil {
				tc.stub(fake)
			}
			lookPath := tc.lookPath
			if lookPath == nil {
				lookPath = found
			}
			inst := Installer{Runner: fake, Manager: PNPM, LookPath: lookPath}

			id, err := inst.Version(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				// Every Version error names the offending manager.
				if !strings.Contains(err.Error(), "pnpm") {
					t.Errorf("err = %v, want mention of the manager", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tc.wantID {
				t.Errorf("identity = %q, want %q", id, tc.wantID)
			}
			call := fake.Calls()[0]
			if !reflect.DeepEqual(call.Args, []string{"--version"}) || !call.AllowTrustedBatch {
				t.Errorf("version command = %+v, want --version with AllowTrustedBatch", call)
			}
		})
	}
}

// TestInstallDirValidation verifies the upfront directory guard in Install.
// Invalid dirs are rejected before the PM is ever invoked, so the error names
// the manager and the path rather than surfacing an opaque PM-native message.
func TestInstallDirValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("empty dir is rejected", func(t *testing.T) {
		fake := execx.NewFake()
		_, err := (Installer{Runner: fake, Manager: NPM}).Install(ctx, "", nil)
		if err == nil {
			t.Fatal("expected error for empty dir, got nil")
		}
		if !strings.Contains(err.Error(), "must not be empty") {
			t.Errorf("err = %v, want 'must not be empty'", err)
		}
		if len(fake.Calls()) != 0 {
			t.Error("runner must not be invoked when dir is empty")
		}
	})

	t.Run("nonexistent dir is rejected", func(t *testing.T) {
		// Use a guaranteed-nonexistent path: a fresh TempDir has no children.
		missing := filepath.Join(t.TempDir(), "missing")
		fake := execx.NewFake()
		_, err := (Installer{Runner: fake, Manager: NPM}).Install(ctx, missing, nil)
		if err == nil {
			t.Fatal("expected error for nonexistent dir, got nil")
		}
		if !strings.Contains(err.Error(), "target directory") {
			t.Errorf("err = %v, want 'target directory'", err)
		}
		if len(fake.Calls()) != 0 {
			t.Error("runner must not be invoked when dir does not exist")
		}
	})

	t.Run("regular file is rejected", func(t *testing.T) {
		// A regular file passes os.Stat but must be caught by the IsDir check.
		f, err := os.CreateTemp(t.TempDir(), "notadir")
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		fake := execx.NewFake()
		_, err = (Installer{Runner: fake, Manager: NPM}).Install(ctx, f.Name(), nil)
		if err == nil {
			t.Fatal("expected error when dir is a regular file, got nil")
		}
		if !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("err = %v, want 'not a directory'", err)
		}
		if len(fake.Calls()) != 0 {
			t.Error("runner must not be invoked when dir is a file")
		}
	})

	t.Run("valid dir proceeds to runner", func(t *testing.T) {
		dir := t.TempDir()
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: NPM}).Install(ctx, dir, nil); err != nil {
			t.Fatalf("unexpected error for valid dir: %v", err)
		}
		if len(fake.Calls()) != 1 {
			t.Errorf("expected 1 runner call, got %d", len(fake.Calls()))
		}
	})
}

// TestGoInstallMergesGOFLAGS verifies that Install injects -mod=mod into
// GOFLAGS without clobbering other flags the user may already have there.
func TestGoInstallMergesGOFLAGS(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	t.Run("empty GOFLAGS gets only -mod=mod", func(t *testing.T) {
		t.Setenv("GOFLAGS", "")
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: GO}).Install(ctx, dir, nil); err != nil {
			t.Fatal(err)
		}
		c := fake.Calls()[0]
		if len(c.ExtraEnv) != 1 || c.ExtraEnv[0] != "GOFLAGS=-mod=mod" {
			t.Errorf("empty GOFLAGS: ExtraEnv = %v, want [GOFLAGS=-mod=mod]", c.ExtraEnv)
		}
	})

	t.Run("existing flags are preserved alongside -mod=mod", func(t *testing.T) {
		// This is the critical case: a user with GOFLAGS=-tags=integration must
		// not lose their flag when DepBisect injects -mod=mod.
		t.Setenv("GOFLAGS", "-tags=integration")
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: GO}).Install(ctx, dir, nil); err != nil {
			t.Fatal(err)
		}
		c := fake.Calls()[0]
		if len(c.ExtraEnv) != 1 {
			t.Fatalf("ExtraEnv = %v, want exactly 1 entry", c.ExtraEnv)
		}
		goflags := c.ExtraEnv[0]
		if !strings.HasPrefix(goflags, "GOFLAGS=") {
			t.Errorf("ExtraEnv[0] = %q, want GOFLAGS= prefix", goflags)
		}
		if !strings.Contains(goflags, "-tags=integration") {
			t.Errorf("ExtraEnv[0] = %q, want -tags=integration preserved", goflags)
		}
		if !strings.Contains(goflags, "-mod=mod") {
			t.Errorf("ExtraEnv[0] = %q, want -mod=mod injected", goflags)
		}
	})

	t.Run("conflicting -mod=vendor is replaced not duplicated", func(t *testing.T) {
		t.Setenv("GOFLAGS", "-mod=vendor")
		fake := execx.NewFake()
		if _, err := (Installer{Runner: fake, Manager: GO}).Install(ctx, dir, nil); err != nil {
			t.Fatal(err)
		}
		goflags := fake.Calls()[0].ExtraEnv[0]
		if strings.Contains(goflags, "-mod=vendor") {
			t.Errorf("ExtraEnv[0] = %q, want -mod=vendor removed", goflags)
		}
		if !strings.Contains(goflags, "-mod=mod") {
			t.Errorf("ExtraEnv[0] = %q, want -mod=mod present", goflags)
		}
	})
}

// TestMergeModFlag verifies the GOFLAGS merge logic in isolation.
func TestMergeModFlag(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		want     string
	}{
		{"empty", "", "-mod=mod"},
		{"no mod flag", "-tags=integration", "-tags=integration -mod=mod"},
		{"vendor overridden", "-mod=vendor", "-mod=mod"},
		{"readonly overridden", "-mod=readonly", "-mod=mod"},
		{"mod=mod preserved (no dup)", "-mod=mod", "-mod=mod"},
		{"other flags preserved around mod", "-race -mod=vendor -count=1", "-race -count=1 -mod=mod"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeModFlag(tc.existing)
			if got != tc.want {
				t.Errorf("mergeModFlag(%q) = %q, want %q", tc.existing, got, tc.want)
			}
		})
	}
}

// TestVersionDefaultLookPath verifies that when LookPath is nil, Version falls
// back to exec.LookPath. We use a manager name that cannot possibly be on PATH
// so the lookup fails deterministically without needing a fake binary.
func TestVersionDefaultLookPath(t *testing.T) {
	inst := Installer{
		Runner:   execx.NewFake(), // never reached — LookPath failure returns first
		Manager:  Manager("depbisect-no-such-binary-xyz"),
		LookPath: nil, // exercise the exec.LookPath fallback
	}
	_, err := inst.Version(context.Background())
	if err == nil {
		t.Fatal("expected error for binary not on PATH, got nil")
	}
	if !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("err = %v, want 'not found on PATH'", err)
	}
}

// TestVersionErrorPathsAllManagers verifies that the error branches in Version
// (runner failure and empty output) behave identically regardless of which
// manager is used. The table in TestVersion exercises these paths only for
// PNPM; this test covers GO and CARGO to guard against any future
// manager-specific divergence.
func TestVersionErrorPathsAllManagers(t *testing.T) {
	cases := []struct {
		manager Manager
		name    string
		stub    func(*execx.Fake)
		wantErr string
	}{
		{
			manager: GO,
			name:    "runner failure",
			stub:    func(f *execx.Fake) { f.Default.Err = errors.New("spawn failed") },
			wantErr: "inspect package manager",
		},
		{
			manager: GO,
			name:    "empty stdout",
			stub:    func(f *execx.Fake) { f.Default.Result = execx.Result{Stdout: []byte("   \n\t\n")} },
			wantErr: "produced no output",
		},
		{
			manager: CARGO,
			name:    "nonzero exit",
			stub: func(f *execx.Fake) {
				f.Default.Result = execx.Result{ExitCode: 1, Stderr: []byte("cargo: error\n")}
			},
			wantErr: "cargo: error",
		},
		{
			manager: UV,
			name:    "empty stdout",
			stub:    func(f *execx.Fake) { f.Default.Result = execx.Result{Stdout: []byte("")} },
			wantErr: "produced no output",
		},
	}
	for _, tc := range cases {
		t.Run(string(tc.manager)+"/"+tc.name, func(t *testing.T) {
			fake := execx.NewFake()
			tc.stub(fake)
			inst := Installer{Runner: fake, Manager: tc.manager, LookPath: found}
			_, err := inst.Version(context.Background())
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want substring %q", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), string(tc.manager)) {
				t.Errorf("err = %v, want manager name %q mentioned", err, tc.manager)
			}
		})
	}
}

func TestVersionCargo(t *testing.T) {
	// cargo --version already includes the "cargo" prefix; Version must not
	// double it the way it prefixes the bare npm/pnpm numbers.
	fake := execx.NewFake()
	fake.Default.Result = execx.Result{Stdout: []byte("cargo 1.75.0 (1d8b05cdd 2023-11-20)\n")}
	inst := Installer{Runner: fake, Manager: CARGO, LookPath: found}
	id, err := inst.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id != "cargo 1.75.0 (1d8b05cdd 2023-11-20)" {
		t.Errorf("identity = %q, want unprefixed cargo version", id)
	}
}

func TestVersionGo(t *testing.T) {
	// `go version` (not `go --version`) prints "go version goX.Y ..."; Version
	// must invoke the subcommand and must not double the leading "go".
	fake := execx.NewFake()
	fake.Default.Result = execx.Result{Stdout: []byte("go version go1.22.0 darwin/arm64\n")}
	inst := Installer{Runner: fake, Manager: GO, LookPath: found}
	id, err := inst.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id != "go version go1.22.0 darwin/arm64" {
		t.Errorf("identity = %q", id)
	}
	if call := fake.Calls()[0]; !reflect.DeepEqual(call.Args, []string{"version"}) {
		t.Errorf("version args = %v, want [version]", call.Args)
	}
}

func TestVersionUv(t *testing.T) {
	// `uv --version` prints "uv x.y.z"; the leading "uv" must not be doubled.
	fake := execx.NewFake()
	fake.Default.Result = execx.Result{Stdout: []byte("uv 0.5.11 (abc1234 2024-12-01)\n")}
	inst := Installer{Runner: fake, Manager: UV, LookPath: found}
	id, err := inst.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id != "uv 0.5.11 (abc1234 2024-12-01)" {
		t.Errorf("identity = %q, want unprefixed uv version", id)
	}
	if call := fake.Calls()[0]; !reflect.DeepEqual(call.Args, []string{"--version"}) {
		t.Errorf("version args = %v, want [--version]", call.Args)
	}
}

// TestGoInstallReconcilesGoSumZipChecksums is a real-toolchain regression test
// for the gap that `go mod download all` closes. When a candidate reverts a
// dependency, the worktree's go.sum lacks that version's module-zip checksum;
// the install step must add it or the verification build fails with "missing
// go.sum entry". A bare `go mod download` records only the "<mod>/go.mod"
// checksum, leaving the build broken — TestInstallInvocation's fake runner
// cannot catch that, so this drives the actual `go` toolchain end to end.
//
// It runs fully offline against a one-module file:// GOPROXY built here, so it
// depends on nothing outside this repo's own dependencies.
func TestGoInstallReconcilesGoSumZipChecksums(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go toolchain integration test in -short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("file:// GOPROXY path handling differs on Windows; covered on POSIX")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not found on PATH")
	}

	const modPath, modVer = "depbisect.test/leaf", "v1.0.0"
	leafGoMod := "module " + modPath + "\n\ngo 1.16\n"

	// A canonical module zip for the leaf dependency, with no requirements of
	// its own so `go mod download all` needs only this one zip.
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "go.mod"), leafGoMod)
	mustWrite(t, filepath.Join(src, "leaf.go"),
		"package leaf\n\n// Hello is referenced by the consumer so its build compiles this module.\nfunc Hello() string { return \"hi\" }\n")

	// An offline file:// GOPROXY containing only the leaf module.
	proxy := t.TempDir()
	modDir := filepath.Join(proxy, modPath, "@v")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(modDir, modVer+".mod"), leafGoMod)
	mustWrite(t, filepath.Join(modDir, modVer+".info"), `{"Version":"`+modVer+`","Time":"2020-01-01T00:00:00Z"}`)
	mustWrite(t, filepath.Join(modDir, "list"), modVer+"\n")
	zf, err := os.Create(filepath.Join(modDir, modVer+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := zip.CreateFromDir(zf, module.Version{Path: modPath, Version: modVer}, src); err != nil {
		_ = zf.Close()
		t.Fatalf("create module zip: %v", err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOWORK", "off")

	// A consumer module that imports the leaf package, deliberately written
	// without a go.sum: the reverted-candidate state where the module-zip
	// checksum is absent.
	work := t.TempDir()
	mustWrite(t, filepath.Join(work, "go.mod"),
		"module depbisecttest\n\ngo 1.21\n\nrequire "+modPath+" "+modVer+"\n")
	mustWrite(t, filepath.Join(work, "use.go"),
		"package depbisecttest\n\nimport \""+modPath+"\"\n\nvar _ = leaf.Hello\n")

	// Drive the production install path (go mod download all, GOFLAGS=-mod=mod).
	res, err := (Installer{Runner: execx.Local{}, Manager: GO}).Install(context.Background(), work, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("install exited %d:\n%s", res.ExitCode, res.Stderr)
	}

	// The regression assertion: a strictly read-only build must succeed, which
	// is possible only if go.sum now holds the leaf module-zip checksum. With a
	// bare `go mod download` install this fails with "missing go.sum entry".
	build := exec.Command(goBin, "build", "-mod=readonly", "./...")
	build.Dir = work
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("read-only build after install failed — go.sum zip checksum missing?\n%v\n%s", err, out)
	}
}

// TestFirstNonEmptyLine verifies the (string, bool) contract — especially that
// no sentinel string escapes to the caller and that whitespace-only input is
// correctly signaled as "no output" via the bool rather than a magic string.
func TestFirstNonEmptyLine(t *testing.T) {
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
		// Sentinel collision defense: a literal "no output" must succeed, not
		// be misidentified as the "produced no output" error condition.
		{"literal 'no output' string", []byte("no output\n"), "no output", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := firstNonEmptyLine(tc.input)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("firstNonEmptyLine(%q) = (%q, %v), want (%q, %v)",
					tc.input, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// mustWrite writes body to path, failing the test on error.
func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDependencyFiles(t *testing.T) {
	files := DependencyFiles()
	want := []string{
		"package.json", "package-lock.json", "pnpm-lock.yaml",
		"Cargo.toml", "Cargo.lock", "go.mod", "go.sum",
		"pyproject.toml", "uv.lock",
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("DependencyFiles() = %v, want %v", files, want)
	}
	// package.json is shared by npm and pnpm; it must appear exactly once.
	seen := make(map[string]int)
	for _, f := range files {
		seen[f]++
	}
	for f, n := range seen {
		if n != 1 {
			t.Errorf("%q appears %d times; want deduplicated", f, n)
		}
	}
}
