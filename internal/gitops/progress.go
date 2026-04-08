package gitops

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// ProgressFunc receives real-time output lines during sync operations.
type ProgressFunc func(line string)

// runStreaming executes a command, streaming each output line to progressFn in
// real time. Returns the combined output and any error.
// If progressFn is nil, behaves like cmd.CombinedOutput().
func runStreaming(ctx context.Context, progressFn ProgressFunc, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return runStreamingCmd(cmd, progressFn)
}

// runStreamingCmd executes a pre-built *exec.Cmd, streaming each output line
// to progressFn. The caller must set cmd.Dir and cmd.Env before calling.
func runStreamingCmd(cmd *exec.Cmd, progressFn ProgressFunc) (string, error) {
	if progressFn == nil {
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Merge stdout and stderr into a single pipe.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	var collected strings.Builder
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			collected.WriteString(line)
			collected.WriteByte('\n')
			progressFn(line)
		}
	}()

	err := cmd.Start()
	if err != nil {
		pw.Close()
		pr.Close()
		return "", fmt.Errorf("start: %w", err)
	}

	runErr := cmd.Wait()
	pw.Close()
	wg.Wait()
	pr.Close()

	return collected.String(), runErr
}
