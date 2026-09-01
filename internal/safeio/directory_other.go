//go:build !linux && !windows

package safeio

import "os"

func renameDirectory(source, target string) error {
	return os.Rename(source, target)
}
