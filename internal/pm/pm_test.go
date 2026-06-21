package pm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/skyneticist/depbisect/internal/execx"
)

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
	fake := execx.NewFake()
	inst := Installer{Runner: fake, Manager: PNPM}
	if _, err := inst.Install(context.Background(), "/work dir", nil); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	if c.Name != "pnpm" || c.Dir != "/work dir" {
		t.Errorf("cmd = %+v", c)
	}
	if !c.AllowTrustedBatch {
		t.Error("package manager invocation must allow its fixed arguments through Windows batch shims")
	}
	// pnpm freezes the lockfile by default when CI=true; DepBisect must
	// always override that, or candidate installs fail on CI runners.
	joined := strings.Join(c.Args, " ")
	if !strings.Contains(joined, "--no-frozen-lockfile") {
		t.Errorf("pnpm args missing --no-frozen-lockfile: %v", c.Args)
	}

	fake2 := execx.NewFake()
	inst2 := Installer{Runner: fake2, Manager: NPM}
	if _, err := inst2.Install(context.Background(), "/w", nil); err != nil {
		t.Fatal(err)
	}
	c2 := fake2.Calls()[0]
	if c2.Name != "npm" || c2.Args[0] != "install" {
		t.Errorf("cmd = %+v", c2)
	}
	if !reflect.DeepEqual(c2.ExtraEnv, []string(nil)) && len(c2.ExtraEnv) != 0 {
		t.Errorf("unexpected extra env: %v", c2.ExtraEnv)
	}

	fakeCargo := execx.NewFake()
	instCargo := Installer{Runner: fakeCargo, Manager: CARGO}
	if _, err := instCargo.Install(context.Background(), "/c", nil); err != nil {
		t.Fatal(err)
	}
	cc := fakeCargo.Calls()[0]
	if cc.Name != "cargo" || !reflect.DeepEqual(cc.Args, []string{"fetch"}) {
		t.Errorf("cargo install cmd = %+v, want cargo fetch", cc)
	}
	if !cc.AllowTrustedBatch {
		t.Error("cargo invocation must allow its fixed arguments through Windows batch shims")
	}

	fakeGo := execx.NewFake()
	instGo := Installer{Runner: fakeGo, Manager: GO}
	if _, err := instGo.Install(context.Background(), "/g", nil); err != nil {
		t.Fatal(err)
	}
	gc := fakeGo.Calls()[0]
	if gc.Name != "go" || !reflect.DeepEqual(gc.Args, []string{"mod", "download"}) {
		t.Errorf("go install cmd = %+v, want go mod download", gc)
	}
	// Candidate go.mod edits add or revert versions; -mod=mod lets `go mod
	// download` populate go.sum (and reconcile go.mod) for them.
	if !reflect.DeepEqual(gc.ExtraEnv, []string{"GOFLAGS=-mod=mod"}) {
		t.Errorf("go install env = %v, want [GOFLAGS=-mod=mod]", gc.ExtraEnv)
	}

	fakeUv := execx.NewFake()
	instUv := Installer{Runner: fakeUv, Manager: UV}
	if _, err := instUv.Install(context.Background(), "/u", nil); err != nil {
		t.Fatal(err)
	}
	uc := fakeUv.Calls()[0]
	if uc.Name != "uv" || !reflect.DeepEqual(uc.Args, []string{"lock"}) {
		t.Errorf("uv install cmd = %+v, want uv lock", uc)
	}
	if !uc.AllowTrustedBatch {
		t.Error("uv invocation must allow its fixed arguments through Windows batch shims")
	}
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
