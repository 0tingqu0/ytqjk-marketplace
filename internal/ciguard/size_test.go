package ciguard

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const productionGoLineLimit = 400

func TestProductionGoFilesStayWithinLineLimit(t *testing.T) {
	root := repositoryRoot(t)
	violations := make([]string, 0)
	for _, directory := range []string{"cmd", "internal"} {
		path := filepath.Join(root, directory)
		err := filepath.WalkDir(path, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			count, err := effectiveGoLineCount(filePath)
			if err != nil {
				return err
			}
			if count > productionGoLineLimit {
				relative, err := filepath.Rel(root, filePath)
				if err != nil {
					return err
				}
				violations = append(violations, filepath.ToSlash(relative)+": "+strconv.Itoa(count))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan production Go files: %v", err)
		}
	}
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	t.Fatalf("production Go files exceed %d effective lines:\n%s", productionGoLineLimit, strings.Join(violations, "\n"))
}

func effectiveGoLineCount(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fileSet := token.NewFileSet()
	file := fileSet.AddFile(path, fileSet.Base(), len(content))
	var source scanner.Scanner
	source.Init(file, content, nil, 0)
	lines := make(map[int]struct{})
	for {
		position, item, literal := source.Scan()
		if item == token.EOF {
			break
		}
		if item == token.SEMICOLON && literal == "\n" {
			continue
		}
		lines[fileSet.Position(position).Line] = struct{}{}
	}
	return len(lines), nil
}
