package credentials

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

// ExecRunner is the default Runner: it runs the program directly, without a shell.
func ExecRunner(cmd []string, input []byte, timeout time.Duration) (Result, error) {
	if len(cmd) == 0 {
		return Result{}, errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	platform.HideWindow(c)
	if input != nil {
		c.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	// A grandchild holding the pipes open must not stall us after the kill.
	c.WaitDelay = 2 * time.Second
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return Result{}, ErrTimeout
	}
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return Result{}, err
		}
		res.Code = exitErr.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Code = -int(ws.Signal())
		}
	}
	return res, nil
}
