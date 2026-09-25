package cli

import "os/exec"

func configureSubprocess(_ *exec.Cmd) {}

func terminateSubprocess(cmd *exec.Cmd) error {
	// WaitDelay still bounds inherited pipes when Windows cannot kill descendants.
	return cmd.Process.Kill()
}
