// Package execx runs subprocesses with exact argument vectors.
//
// No shell is ever involved: Cmd.Name is resolved via the OS PATH lookup and
// Cmd.Args are passed to the kernel verbatim, so spaces, quotes, and shell
// metacharacters in arguments are inert. Callers that want shell semantics
// must invoke a shell explicitly.
package execx

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// maxCapturedOutput caps the bytes retained per stream in a Result. The tail
// is kept, since failure evidence usually appears at the end of output.
const maxCapturedOutput = 256 * 1024

// Cmd describes one subprocess invocation.
type Cmd struct {
	// Dir is the working directory ("" means the current directory).
	Dir string
	// Name is the program to run, resolved against PATH if not a path.
	Name string
	// Args are passed verbatim, never interpreted by a shell.
	Args []string
	// ExtraEnv entries ("KEY=VALUE") are appended to the inherited
	// environment of the parent process.
	ExtraEnv []string
	// Stream, when non-nil, additionally receives interleaved stdout and
	// stderr as the process produces them. Writes are serialized.
	Stream io.Writer
}

// Result is the outcome of a completed subprocess.
type Result struct {
	// ExitCode is the process exit status. A nonzero exit is not an error.
	ExitCode int
	// Stdout and Stderr hold captured output, truncated to the final
	// maxCapturedOutput bytes per stream.
	Stdout []byte
	Stderr []byte
	// Duration is the wall-clock time from start to exit.
	Duration time.Duration
}

// Runner runs subprocesses. Implementations must return an error only when
// the process could not run to completion (start failure, cancellation);
// an ordinary nonzero exit is reported via Result.ExitCode.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// Local runs subprocesses on the local machine.
type Local struct{}

// Run implements Runner. On context cancellation the process receives an
// interrupt signal, then is killed after a 10s grace period.
func (Local) Run(ctx context.Context, c Cmd) (Result, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.ExtraEnv) > 0 {
		cmd.Env = append(os.Environ(), c.ExtraEnv...)
	}
	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 10 * time.Second

	stdout := &tailBuffer{limit: maxCapturedOutput}
	stderr := &tailBuffer{limit: maxCapturedOutput}
	if c.Stream != nil {
		shared := &syncWriter{w: c.Stream}
		cmd.Stdout = io.MultiWriter(stdout, shared)
		cmd.Stderr = io.MultiWriter(stderr, shared)
	} else {
		cmd.Stdout = stdout
		cmd.Stderr = stderr
	}

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: time.Since(start),
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && ctx.Err() == nil {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		if ctx.Err() != nil {
			return res, fmt.Errorf("run %s: %w", c.Name, ctx.Err())
		}
		return res, fmt.Errorf("run %s: %w", c.Name, err)
	}
	return res, nil
}

// tailBuffer retains at most limit bytes, discarding the oldest data first.
type tailBuffer struct {
	limit int
	buf   []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) Bytes() []byte { return b.buf }

// syncWriter serializes writes from the stdout and stderr pipe goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
