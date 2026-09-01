package cli

import (
	"fmt"

	"github.com/0tingqu0/ytqjk-marketplace/internal/install"
)

func (context commandContext) emitInstall(receipt map[string]any, asJSON bool, exitCode int) int {
	if asJSON {
		_ = context.write(receipt)
	} else {
		fmt.Fprintln(context.out, install.SummaryText(receipt))
	}
	return exitCode
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
