//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package dashboard

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

func candidateSingleLink(_ *os.File) bool { return true }
