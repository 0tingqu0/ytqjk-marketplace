package handoff

import (
	"fmt"
	"strings"
)

func disjointPathDomains(tracked, untracked []string) error {
	for _, trackedPath := range tracked {
		for _, untrackedPath := range untracked {
			if pathContains(trackedPath, untrackedPath) || pathContains(untrackedPath, trackedPath) {
				return fmt.Errorf(
					"tracked and untracked paths overlap: %s, %s",
					trackedPath,
					untrackedPath,
				)
			}
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	parentParts := strings.Split(parent, "/")
	childParts := strings.Split(child, "/")
	if len(parentParts) > len(childParts) {
		return false
	}
	for index := range parentParts {
		if !strings.EqualFold(parentParts[index], childParts[index]) {
			return false
		}
	}
	return true
}
