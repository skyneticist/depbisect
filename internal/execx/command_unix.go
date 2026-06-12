//go:build !windows

package execx

import (
	"context"
	"os"
	"os/exec"
	"time"
)

func commandContext(ctx context.Context, c Cmd) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, c.Name, c.Args...), nil
}

func configureCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 10 * time.Second
}
