//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package dashboard

import (
	"os"
	"syscall"
)

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

func candidateSingleLink(file *os.File) bool {
	information, ok := fileInfoStat(file)
	return ok && information.Nlink == 1
}

func fileInfoStat(file *os.File) (*syscall.Stat_t, bool) {
	information, err := file.Stat()
	if err != nil {
		return nil, false
	}
	stat, ok := information.Sys().(*syscall.Stat_t)
	return stat, ok
}
