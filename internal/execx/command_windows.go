//go:build windows

package execx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func commandContext(ctx context.Context, c Cmd) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	ext := strings.ToLower(filepath.Ext(cmd.Path))
	if ext != ".bat" && ext != ".cmd" {
		return cmd, nil
	}
	if !c.AllowTrustedBatch {
		return nil, fmt.Errorf(
			"refusing implicit Windows batch command %q; invoke cmd.exe explicitly if shell semantics are intended",
			cmd.Path)
	}
	for _, arg := range c.Args {
		if !trustedBatchArg(arg) {
			return nil, fmt.Errorf("unsafe argument %q for trusted Windows batch command", arg)
		}
	}
	return trustedBatchCommand(ctx, cmd.Path, c.Args)
}

func trustedBatchArg(arg string) bool {
	if arg == "" {
		return false
	}
	for _, r := range arg {
		switch {
		case 'a' <= r && r <= 'z':
		case 'A' <= r && r <= 'Z':
		case '0' <= r && r <= '9':
		case strings.ContainsRune("-_=./:@", r):
		default:
			return false
		}
	}
	return true
}

func trustedBatchCommand(ctx context.Context, path string, args []string) (*exec.Cmd, error) {
	if strings.Contains(path, "%") {
		return nil, fmt.Errorf("batch path %q contains an unsupported percent sign", path)
	}
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			return nil, fmt.Errorf("neither ComSpec nor SystemRoot is set")
		}
		comspec = filepath.Join(systemRoot, "System32", "cmd.exe")
	}
	if !filepath.IsAbs(comspec) || !strings.EqualFold(filepath.Base(comspec), "cmd.exe") {
		return nil, fmt.Errorf("ComSpec does not identify an absolute cmd.exe path")
	}

	commandLine := `"` + path + `"`
	if len(args) > 0 {
		commandLine += " " + strings.Join(args, " ")
	}
	cmd := exec.CommandContext(ctx, comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: syscall.EscapeArg(comspec) + ` /d /s /v:off /c "` + commandLine + `"`,
	}
	return cmd, nil
}

func configureCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 10 * time.Second
}
