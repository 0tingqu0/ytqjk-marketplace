package maintenance

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type resourceKind uint8

const (
	resourceDirectory resourceKind = iota + 1
	resourceFile
)

type resourceSpecification struct {
	path            string
	kind            resourceKind
	requireExisting bool
}

type resourceScan struct {
	identities   map[string]string
	targetExists bool
}

type resourceBinding struct {
	mu                sync.Mutex
	path              string
	key               string
	kind              resourceKind
	identities        map[string]string
	targetExistsValue bool
}

func newResourceBinding(specification resourceSpecification) (*resourceBinding, error) {
	path, err := canonicalResourcePath(specification.path)
	if err != nil {
		return nil, err
	}
	scan, err := scanStableResource(path, specification.kind)
	if err != nil {
		return nil, err
	}
	if specification.requireExisting && !scan.targetExists {
		return nil, errors.New("required resource directory does not exist")
	}
	return &resourceBinding{
		path: path, key: canonicalKey(path), kind: specification.kind,
		identities: scan.identities, targetExistsValue: scan.targetExists,
	}, nil
}

func (binding *resourceBinding) targetExists() bool {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	return binding.targetExistsValue
}

func (binding *resourceBinding) verify() error {
	if binding == nil {
		return errors.New("business resource binding is missing")
	}
	binding.mu.Lock()
	defer binding.mu.Unlock()
	scan, err := scanStableResource(binding.path, binding.kind)
	if err != nil {
		return err
	}
	for path, expected := range binding.identities {
		if current, ok := scan.identities[path]; !ok || current != expected {
			return errors.New("business resource ancestor identity changed")
		}
	}
	if binding.targetExistsValue && !scan.targetExists {
		return errors.New("business resource target disappeared")
	}
	binding.identities = scan.identities
	binding.targetExistsValue = scan.targetExists
	return nil
}

func verifyResourceBindings(control controlPlane) error {
	for _, binding := range control.bindings {
		if err := binding.verify(); err != nil {
			return fail(CodeRecoveryRequired, errors.Join(
				errors.New("maintenance business resource identity changed"), err,
			))
		}
	}
	return nil
}

func canonicalResourcePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("resource path is empty or malformed")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func scanStableResource(path string, kind resourceKind) (resourceScan, error) {
	first, err := scanResource(path, kind)
	if err != nil {
		return resourceScan{}, err
	}
	second, err := scanResource(path, kind)
	if err != nil {
		return resourceScan{}, err
	}
	if first.targetExists != second.targetExists || !sameIdentityMaps(first.identities, second.identities) {
		return resourceScan{}, errors.New("business resource changed while it was being bound")
	}
	return second, nil
}

func scanResource(path string, kind resourceKind) (result resourceScan, returnedErr error) {
	root, parts, err := splitResourcePath(path)
	if err != nil {
		return result, err
	}
	if kind == resourceFile && len(parts) == 0 {
		return result, errors.New("filesystem root cannot be bound as a file")
	}
	current, err := openResourceRootNoFollow(root)
	if err != nil {
		return result, err
	}
	defer func() { returnedErr = errors.Join(returnedErr, current.Close()) }()
	result.identities = make(map[string]string, len(parts)+1)
	identity, err := fileHandleIdentity(current)
	if err != nil {
		return result, err
	}
	result.identities[canonicalKey(root)] = identity
	currentPath := root
	for index, part := range parts {
		isTarget := index == len(parts)-1
		if isTarget && kind == resourceFile {
			file, openErr := openRootRegularFileNoFollow(current, part, false)
			if errors.Is(openErr, os.ErrNotExist) {
				return result, nil
			}
			if openErr != nil {
				return result, openErr
			}
			fileIdentity, identityErr := fileHandleIdentity(file)
			closeErr := file.Close()
			if identityErr != nil || closeErr != nil {
				return result, errors.Join(identityErr, closeErr)
			}
			result.identities[canonicalKey(path)] = fileIdentity
			result.targetExists = true
			return result, nil
		}
		next, openErr := openResourceDirectoryNoFollow(current, part)
		if errors.Is(openErr, os.ErrNotExist) {
			return result, nil
		}
		if openErr != nil {
			return result, openErr
		}
		if closeErr := current.Close(); closeErr != nil {
			_ = next.Close()
			return result, closeErr
		}
		current = next
		currentPath = filepath.Join(currentPath, part)
		directoryIdentity, identityErr := fileHandleIdentity(current)
		if identityErr != nil {
			return result, identityErr
		}
		result.identities[canonicalKey(currentPath)] = directoryIdentity
	}
	result.targetExists = true
	return result, nil
}

func splitResourcePath(path string) (string, []string, error) {
	volume := filepath.VolumeName(path)
	root := string(filepath.Separator)
	if volume != "" {
		root = volume + string(filepath.Separator)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, errors.Join(errors.New("resource path is outside its filesystem root"), err)
	}
	if relative == "." {
		return root, nil, nil
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part {
			return "", nil, errors.New("resource path contains an invalid component")
		}
	}
	return root, parts, nil
}

func sameIdentityMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	keys := make([]string, 0, len(left))
	for key := range left {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if right[key] != left[key] {
			return false
		}
	}
	return true
}
