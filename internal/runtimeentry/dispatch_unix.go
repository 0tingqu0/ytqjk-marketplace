//go:build !windows

package runtimeentry

import (
	"os"
	"syscall"
)

func runGeneration(target string, arguments []string, generation string) (int, error) {
	environment := append(os.Environ(), "YTQJK_ACTIVE_GENERATION="+generation)
	return 1, syscall.Exec(target, append([]string{target}, arguments...), environment)
}
