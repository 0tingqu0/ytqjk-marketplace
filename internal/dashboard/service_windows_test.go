//go:build windows

package dashboard

import (
	"strings"
	"syscall"
	"testing"
)

func TestWindowsCommandLineEscapesEveryPath(t *testing.T) {
	spec := serviceSpec{
		Binary:        `C:\Program Files\YTQJK\ytqjk.exe`,
		KnowledgeRoot: `C:\Data Root\knowledge`,
		Assets:        `C:\Plugin Root\dashboard`,
		Port:          8765,
	}
	line := windowsCommandLine(spec)
	for _, expected := range []string{
		syscall.EscapeArg(spec.Binary),
		syscall.EscapeArg(spec.KnowledgeRoot),
		syscall.EscapeArg(spec.Assets),
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("command line %q does not contain %q", line, expected)
		}
	}
	if strings.ContainsAny(line, "\r\n") {
		t.Fatalf("command line contains a control character: %q", line)
	}
}
