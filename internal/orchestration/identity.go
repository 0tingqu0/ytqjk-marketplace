package orchestration

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (l *Ledger) sign(token Attestation) string {
	claims, _ := json.Marshal(tokenClaims(token))
	runBinding, _ := json.Marshal(map[string]any{
		"database_id": token.DatabaseID, "objective_hash": token.ObjectiveHash,
		"project_id": token.ProjectID, "run_id": token.RunID, "session_key": token.SessionKey,
	})
	runDigest := hmac.New(sha256.New, l.key)
	_, _ = runDigest.Write(runBinding)
	leaseDigest := hmac.New(sha256.New, runDigest.Sum(nil))
	_, _ = leaseDigest.Write([]byte(token.LeaseID))
	signature := hmac.New(sha256.New, leaseDigest.Sum(nil))
	_, _ = signature.Write(claims)
	return hex.EncodeToString(signature.Sum(nil))
}

func tokenClaims(token Attestation) map[string]any {
	claims := map[string]any{
		"database_id": token.DatabaseID, "expires_at": token.ExpiresAt, "lease_id": token.LeaseID,
		"mutation": token.Mutation, "objective_hash": token.ObjectiveHash, "project_id": token.ProjectID,
		"read_scope": token.ReadScope, "role": token.Role, "run_id": token.RunID, "session_key": token.SessionKey,
		"staged_hash": token.StagedHash, "write_scope": token.WriteScope,
	}
	if token.Capability != "" {
		claims["capability"] = token.Capability
	}
	if token.Operation != "" {
		claims["operation"] = token.Operation
	}
	return claims
}

func loadOrCreateKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		data := make([]byte, 32)
		if _, err := rand.Read(data); err != nil {
			return nil, err
		}
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(createErr, os.ErrExist) {
			return loadOrCreateKey(path)
		}
		if createErr != nil {
			return nil, createErr
		}
		ok := false
		defer func() {
			_ = file.Close()
			if !ok {
				_ = os.Remove(path)
			}
		}()
		if _, err := file.Write(data); err != nil {
			return nil, err
		}
		if err := file.Sync(); err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		ok = true
		created = true
		info, err = os.Lstat(path)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("identity key is not a regular file")
	}
	if err := secureKeyPermissions(path, created); err != nil {
		if created {
			_ = os.Remove(path)
		}
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != 32 {
		return nil, errors.New("identity key is invalid")
	}
	return data, nil
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
