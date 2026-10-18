package hookprovider

import "os/exec"

// waitOwnedProcess terminates the owned group before reaping its leader. A
// completed leader can leave descendants alive; reaping it first also releases
// the PID that identifies our group and permits unrelated PID reuse.
func waitOwnedProcess(command *exec.Cmd) error {
	_ = killNativeGroup(command.Process.Pid)
	return command.Wait()
}
