//go:build !windows

package orchestration

import (
	"errors"
	"os"
)

func secureKeyPermissions(path string, _ bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("identity key permissions are too broad")
	}
	return nil
}
