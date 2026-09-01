//go:build !linux

package handoff

import (
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func writeUntrackedFile(source, root, relative string, mode fs.FileMode) error {
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := safeParents(root, target); err != nil {
		return fmt.Errorf("unsafe parent: %w", err)
	}
	return safeio.CopyFile(source, target, mode)
}
