package process

import "os/exec"

func Configure(_ *exec.Cmd) {}

func Terminate(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
