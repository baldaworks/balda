package pluginapp

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

// Source URLs may contain credentials. Keep argv, stderr and host directories
// out of logs and returned errors, including errors from starting the process.
func marketplaceGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	var output marketplaceGitBuffer
	command.Stdout = &output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", plugincmd.ErrUnavailable
	}
	return output.String(), nil
}

func marketplaceGitRun(ctx context.Context, dir string, args ...string) error {
	_, err := marketplaceGitOutput(ctx, dir, args...)
	return err
}

type marketplaceGitBuffer struct{ bytes.Buffer }

func (b *marketplaceGitBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, plugincmd.ErrUnavailable
	}
	return b.Buffer.Write(p)
}
