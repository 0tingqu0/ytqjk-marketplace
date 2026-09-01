//go:build windows

package runtimeentry

import (
	"os"
	"os/exec"
)

func runGeneration(target string, arguments []string, generation string) (int, error) {
	command := exec.Command(target, arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = append(os.Environ(), "YTQJK_ACTIVE_GENERATION="+generation)
	if err := command.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}
