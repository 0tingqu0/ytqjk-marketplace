package runtimeentry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Dispatch runs the immutable active generation when the current executable is
// the stable runtime launcher. Generation binaries and bundled plugin binaries
// continue into the regular CLI in-process.
func Dispatch(arguments []string) (bool, int) {
	executable, err := os.Executable()
	if err != nil {
		return false, 0
	}
	executable, err = filepath.Abs(executable)
	if err != nil || filepath.Base(executable) != BinaryName() || filepath.Base(filepath.Dir(executable)) != "bin" {
		return false, 0
	}
	runtimeRoot := filepath.Dir(filepath.Dir(executable))
	manifest, target, err := ReadActive(runtimeRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "YTQJK runtime entry rejected:", err)
		return true, 1
	}
	if samePath(executable, target) {
		return false, 0
	}
	code, err := runGeneration(target, arguments, manifest.Generation)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "YTQJK active generation failed:", err)
		return true, 1
	}
	return true, code
}

func samePath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if filepath.Separator == '\\' {
		return filepath.Clean(left) == filepath.Clean(right) ||
			len(left) == len(right) && equalFoldASCII(left, right)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func equalFoldASCII(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		first, second := left[index], right[index]
		if first >= 'A' && first <= 'Z' {
			first += 'a' - 'A'
		}
		if second >= 'A' && second <= 'Z' {
			second += 'a' - 'A'
		}
		if first != second {
			return false
		}
	}
	return true
}
