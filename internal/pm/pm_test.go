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
	if NPM.LockfileName() != "package-lock.json" || PNPM.LockfileName() != "pnpm-lock.yaml" {
		t.Error("wrong lockfile names")
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
}

func TestVersion(t *testing.T) {
	missing := errors.New("not found")
	inst := Installer{Runner: execx.NewFake(), Manager: PNPM, LookPath: func(name string) (string, error) {
		if name == "pnpm" {
			return "", missing
		}
		return "/bin/" + name, nil
	}}
	_, err := inst.Version(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pnpm") {
		t.Errorf("err = %v, want mention of pnpm", err)
	}

	inst.LookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	fake := execx.NewFake()
	fake.Default.Result = execx.Result{Stdout: []byte("9.15.4\n")}
	inst.Runner = fake
	identity, err := inst.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity != "pnpm 9.15.4" {
		t.Errorf("identity = %q", identity)
	}
	call := fake.Calls()[0]
	if !reflect.DeepEqual(call.Args, []string{"--version"}) || !call.AllowTrustedBatch {
		t.Errorf("version command = %+v", call)
	}
}
