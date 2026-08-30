package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestTopLevelHelpDoesNotInvokeInstaller(t *testing.T) {
	for _, argument := range []string{"--help", "-h", "help"} {
		t.Run(argument, func(t *testing.T) {
			var output bytes.Buffer
			if exitCode := Main([]string{argument}, strings.NewReader(""), &output, &output); exitCode != 0 {
				t.Fatalf("Main(%q) exit code = %d, output = %q", argument, exitCode, output.String())
			}
			if !strings.Contains(output.String(), "YTQJK local orchestration and knowledge runtime (Go)") {
				t.Fatalf("help output missing heading: %q", output.String())
			}
			if strings.Contains(output.String(), `"ok":false`) {
				t.Fatalf("help was routed through installer: %q", output.String())
			}
		})
	}
}
