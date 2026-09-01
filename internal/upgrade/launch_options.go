package upgrade

import (
	"errors"
	"os"
	"os/exec"
	"strings"
)

// LaunchOptions binds out-of-band maintenance state to one helper process.
// Transfer runs only after the upgrade operation lock is owned by that child.
type LaunchOptions struct {
	Environment []string
	Transfer    func(childPID int) error
}

func resolveLaunchOptions(options []LaunchOptions) (LaunchOptions, error) {
	if len(options) > 1 {
		return LaunchOptions{}, failure("UPGRADE_HELPER_START_FAILED", errors.New("multiple launch options are invalid"))
	}
	if len(options) == 0 {
		return LaunchOptions{}, nil
	}
	resolved := options[0]
	environment, err := mergeHelperEnvironment(resolved.Environment)
	if err != nil {
		return LaunchOptions{}, failure("UPGRADE_HELPER_START_FAILED", err)
	}
	resolved.Environment = environment
	return resolved, nil
}

func applyLaunchOptions(command *exec.Cmd, options LaunchOptions) {
	if options.Environment != nil {
		command.Env = options.Environment
	}
}

func mergeHelperEnvironment(overrides []string) ([]string, error) {
	if len(overrides) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(overrides))
	for _, entry := range overrides {
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 || strings.ContainsAny(entry, "\x00\r\n") {
			return nil, errors.New("helper environment entry is invalid")
		}
		key := entry[:separator]
		for _, existing := range keys {
			if strings.EqualFold(existing, key) {
				return nil, errors.New("helper environment contains duplicate keys")
			}
		}
		keys = append(keys, key)
	}
	base := os.Environ()
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 || environmentKeyOverridden(entry[:separator], keys) {
			continue
		}
		merged = append(merged, entry)
	}
	return append(merged, overrides...), nil
}

func environmentKeyOverridden(candidate string, keys []string) bool {
	for _, key := range keys {
		if strings.EqualFold(candidate, key) {
			return true
		}
	}
	return false
}
