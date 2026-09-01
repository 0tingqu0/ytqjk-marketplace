//go:build !windows

package safeio

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
