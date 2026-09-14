package main

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func openDelegateTaskFile(path string) (*os.File, error) { return os.Open(path) }

func executeDelegate(_ *exec.Cmd, _ time.Duration) error {
	return errors.New("delegate requires Unix process-group cancellation; Windows is not supported")
}
func signalExitCode(_ *exec.ExitError) int { return 1 }
