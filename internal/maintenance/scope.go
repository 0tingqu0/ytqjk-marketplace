package maintenance

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func normalizeScope(scope Scope) (controlPlane, error) {
	if strings.TrimSpace(scope.ControlRoot) == "" {
		return controlPlane{}, fail(CodeInvalid, errors.New("control root is required"))
	}
	controlRoot, err := canonicalDirectory(scope.ControlRoot)
	if err != nil {
		return controlPlane{}, fail(CodeStateCorrupt, errors.Join(errors.New("control root is unavailable"), err))
	}
	marker, err := readBootstrapRecord(controlRoot)
	if err != nil || marker.Status != bootstrapComplete {
		return controlPlane{}, fail(CodeStateCorrupt, errors.Join(errors.New("maintenance bootstrap proof is invalid"), err))
	}
	directory, err := canonicalDirectory(filepath.Join(controlRoot, "maintenance"))
	if err != nil || canonicalKey(directory) != canonicalKey(filepath.Join(controlRoot, "maintenance")) {
		return controlPlane{}, fail(
			CodeStateCorrupt,
			errors.Join(errors.New("maintenance control directory is unavailable or unsafe"), err),
		)
	}
	directoryID, err := directoryIdentity(directory)
	if err != nil || directoryID != marker.DirectoryIdentity {
		return controlPlane{}, fail(
			CodeStateCorrupt,
			errors.Join(errors.New("maintenance control directory identity changed"), err),
		)
	}
	control := controlPlane{
		root: controlRoot, directory: directory, directoryID: directoryID,
		guardPath: filepath.Join(directory, "guard.lock"), guardID: marker.GuardIdentity,
		writersPath: filepath.Join(directory, "writers.lock"), writersID: marker.WritersIdentity,
		recordPath: filepath.Join(directory, "record.json"),
	}
	if err := validateControlLockIdentities(control); err != nil {
		return controlPlane{}, err
	}
	resources, bindings, err := normalizeResources(scope)
	if err != nil {
		return controlPlane{}, err
	}
	control.resources = resources
	control.bindings = bindings
	return control, nil
}

func validateControlLockIdentities(control controlPlane) error {
	for _, lock := range []struct {
		name     string
		expected string
	}{
		{name: "guard.lock", expected: control.guardID},
		{name: "writers.lock", expected: control.writersID},
	} {
		identity, err := boundEntryIdentity(control, lock.name)
		if err != nil || identity != lock.expected {
			return fail(
				CodeStateCorrupt,
				errors.Join(errors.New("maintenance lock identity changed"), err),
			)
		}
	}
	return nil
}

func normalizeResources(scope Scope) ([]string, []*resourceBinding, error) {
	specifications := make([]resourceSpecification, 0,
		3+len(scope.ExtraRoots)+len(scope.ProspectiveRoots)+len(scope.FilePaths))
	for _, value := range append(
		[]string{scope.RuntimeRoot, scope.CodexRoot, scope.KnowledgeRoot}, scope.ExtraRoots...,
	) {
		specifications = append(specifications, resourceSpecification{
			path: value, kind: resourceDirectory, requireExisting: true,
		})
	}
	for _, value := range scope.ProspectiveRoots {
		specifications = append(specifications, resourceSpecification{path: value, kind: resourceDirectory})
	}
	for _, value := range scope.FilePaths {
		specifications = append(specifications, resourceSpecification{path: value, kind: resourceFile})
	}
	byPath := make(map[string]*resourceBinding, len(specifications))
	for _, specification := range specifications {
		if strings.TrimSpace(specification.path) == "" {
			continue
		}
		binding, err := newResourceBinding(specification)
		if err != nil {
			return nil, nil, fail(CodeInvalid, errors.Join(errors.New("business resource is unsafe"), err))
		}
		key := binding.key
		if existing, ok := byPath[key]; ok {
			if existing.kind != binding.kind {
				return nil, nil, fail(CodeInvalid, errors.New("business resource has conflicting kinds"))
			}
			if specification.requireExisting && !existing.targetExists() {
				return nil, nil, fail(CodeInvalid, errors.New("business resource root does not exist"))
			}
			continue
		}
		byPath[key] = binding
	}
	if len(byPath) == 0 {
		return nil, nil, fail(CodeInvalid, errors.New("at least one business resource is required"))
	}
	resources := make([]string, 0, len(byPath))
	for key := range byPath {
		resources = append(resources, key)
	}
	sort.Strings(resources)
	bindings := make([]*resourceBinding, 0, len(resources))
	for _, resource := range resources {
		bindings = append(bindings, byPath[resource])
	}
	return resources, bindings, nil
}

func canonicalDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("directory is empty")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(errors.New("directory is unsafe"), err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if canonicalKey(resolved) != canonicalKey(absolute) {
		return "", errors.New("directory traverses a symbolic link")
	}
	return absolute, nil
}

func canonicalKey(value string) string {
	key := filepath.ToSlash(filepath.Clean(value))
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}
