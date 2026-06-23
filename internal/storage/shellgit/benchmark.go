package shellgit

import (
	"context"
	"os/exec"
	"time"
)

type Result struct {
	Command  string
	Duration time.Duration
	Output   []byte
}

func RunFixedArgs(ctx context.Context, repoPath string, args ...string) (Result, error) {
	start := time.Now()
	base := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", base...)
	out, err := cmd.CombinedOutput()
	return Result{Command: "git", Duration: time.Since(start), Output: out}, err
}
