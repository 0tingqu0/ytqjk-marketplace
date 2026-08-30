package orchestration

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const DefaultLeaseSeconds = 30 * 60

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var sensitiveConfigPattern = regexp.MustCompile(`^(?:auth|credential|credentials|secret|secrets|token|tokens)\.(?:cfg|conf|ini|json|properties|toml|ya?ml)$`)

var sensitiveScopeDirectories = map[string]bool{
	".aws": true, ".azure": true, ".cargo": true, ".docker": true, ".git": true,
	".gnupg": true, ".kube": true, ".m2": true, ".ssh": true, ".terraform": true,
	".venv": true, "__pycache__": true, "build": true, "coverage": true,
	"dist": true, "node_modules": true, "vendor": true,
}

var sensitiveScopeNames = map[string]bool{
	".authinfo": true, ".git-credentials": true, ".my.cnf": true, ".netrc": true,
	".npmrc": true, ".pgpass": true, ".pypirc": true, ".yarnrc": true,
	".yarnrc.yml": true, "_netrc": true, "auth.json": true, "credentials": true,
	"credentials.json": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	"id_rsa": true, "kubeconfig": true, "nuget.config": true, "secret.json": true,
	"secrets.json": true, "service-account.json": true, "service_account.json": true,
	"settings-security.xml": true, "token.json": true, "tokens.json": true,
}

var sensitiveScopeEndings = []string{
	".age", ".asc", ".gpg", ".jks", ".key", ".kdbx", ".keystore", ".p12",
	".pem", ".pfx", ".ovpn", ".tfstate", ".tfstate.backup", ".tfvars", ".tfvars.json",
}

type Ledger struct {
	database *sql.DB
	key      []byte
	keyPath  string
}

type Run struct {
	RunID         string `json:"run_id"`
	SessionKey    string `json:"session_key"`
	ProjectID     string `json:"project_id"`
	ObjectiveHash string `json:"objective_hash"`
	DatabaseID    string `json:"database_id"`
	Version       int    `json:"version"`
	State         string `json:"state"`
	CreatedAt     int64  `json:"created_at"`
}

type Grant struct {
	RunID        string   `json:"run_id"`
	SessionKey   string   `json:"session_key"`
	Role         string   `json:"role"`
	ReadScope    []string `json:"read_scope"`
	WriteScope   []string `json:"write_scope"`
	Mutation     bool     `json:"mutation"`
	Capabilities []string `json:"capabilities"`
}

type Attestation struct {
	RunID         string   `json:"run_id"`
	SessionKey    string   `json:"session_key"`
	ProjectID     string   `json:"project_id"`
	ObjectiveHash string   `json:"objective_hash"`
	Role          string   `json:"role"`
	ReadScope     []string `json:"read_scope"`
	WriteScope    []string `json:"write_scope"`
	Mutation      bool     `json:"mutation"`
	StagedHash    string   `json:"staged_hash"`
	DatabaseID    string   `json:"database_id"`
	LeaseID       string   `json:"lease_id"`
	ExpiresAt     int64    `json:"expires_at"`
	Signature     string   `json:"signature"`
}

func Open(databasePath, keyPath string) (*Ledger, string, error) {
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		return nil, "", err
	}
	dsn := "file:" + url.PathEscape(filepath.ToSlash(databasePath)) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, "", err
	}
	database.SetMaxOpenConns(1)
	for _, statement := range orchestrationSchema {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			return nil, "", errors.New("身份账本不可用")
		}
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		database.Close()
		return nil, "", err
	}
	ledger := &Ledger{database: database, key: key, keyPath: keyPath}
	databaseID := ""
	if err := database.QueryRow("SELECT value FROM metadata WHERE key='database_id'").Scan(&databaseID); errors.Is(err, sql.ErrNoRows) {
		value := make([]byte, 16)
		if _, err := rand.Read(value); err != nil {
			database.Close()
			return nil, "", err
		}
		databaseID = hex.EncodeToString(value)
		if _, err := database.Exec("INSERT INTO metadata(key,value) VALUES ('database_id',?)", databaseID); err != nil {
			database.Close()
			return nil, "", err
		}
	} else if err != nil {
		database.Close()
		return nil, "", err
	}
	return ledger, databaseID, nil
}

func (l *Ledger) Close() error { return l.database.Close() }

func (l *Ledger) StartRun(projectID, objectiveHash, sessionKey, currentSessionKey string) (Run, error) {
	if strings.TrimSpace(projectID) == "" || len(projectID) > 128 || strings.ContainsAny(projectID, " \t\r\n") {
		return Run{}, errors.New("invalid identity input")
	}
	if !hashPattern.MatchString(objectiveHash) || !hashPattern.MatchString(sessionKey) || !hmac.Equal([]byte(sessionKey), []byte(currentSessionKey)) {
		return Run{}, errors.New("invalid identity input")
	}
	runID, err := randomHex(16)
	if err != nil {
		return Run{}, err
	}
	now := time.Now().Unix()
	var databaseID string
	if err := l.database.QueryRow("SELECT value FROM metadata WHERE key='database_id'").Scan(&databaseID); err != nil {
		return Run{}, err
	}
	tx, err := l.database.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO runs(run_id,session_key,project_id,objective_hash,database_id,created_at) VALUES (?,?,?,?,?,?)", runID, sessionKey, projectID, objectiveHash, databaseID, now); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec("INSERT INTO run_events(run_id,version,state,created_at) VALUES (?,0,'RUNNING',?)", runID, now); err != nil {
		return Run{}, err
	}
	if err := appendAudit(tx, "run_started", runID, sessionKey, "", map[string]any{"project_id": projectID}, now); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return Run{RunID: runID, SessionKey: sessionKey, ProjectID: projectID, ObjectiveHash: objectiveHash, DatabaseID: databaseID, Version: 0, State: "RUNNING", CreatedAt: now}, nil
}

func (l *Ledger) Run(runID string) (Run, error) {
	var run Run
	err := l.database.QueryRow(`SELECT r.run_id,r.session_key,r.project_id,r.objective_hash,r.database_id,e.version,e.state,r.created_at
FROM runs r JOIN run_events e ON e.run_id=r.run_id WHERE r.run_id=? ORDER BY e.version DESC LIMIT 1`, runID).
		Scan(&run.RunID, &run.SessionKey, &run.ProjectID, &run.ObjectiveHash, &run.DatabaseID, &run.Version, &run.State, &run.CreatedAt)
	return run, err
}

func (l *Ledger) Transition(runID, currentSessionKey, target string, expectedVersion int) (Run, error) {
	run, err := l.Run(runID)
	if err != nil {
		return Run{}, err
	}
	if !hmac.Equal([]byte(run.SessionKey), []byte(currentSessionKey)) || run.Version != expectedVersion {
		return Run{}, errors.New("lifecycle compare-and-swap conflict")
	}
	var lifecycleGrants int
	if err := l.database.QueryRow(`SELECT COUNT(*) FROM role_ledger WHERE run_id=? AND session_key=?
AND role IN ('director','controller') AND read_scope='[]' AND write_scope='[]'
AND mutation=0 AND capabilities='["run:lifecycle"]'`, runID, currentSessionKey).Scan(&lifecycleGrants); err != nil || lifecycleGrants == 0 {
		return Run{}, errors.New("lifecycle capability is unavailable")
	}
	allowed := map[string]map[string]bool{
		"RUNNING": {"PAUSED": true, "STOPPED": true, "DONE": true, "BLOCKED": true},
		"PAUSED":  {"RUNNING": true, "STOPPED": true, "DONE": true, "BLOCKED": true},
	}
	if !allowed[run.State][target] {
		return Run{}, errors.New("illegal run state transition")
	}
	now := time.Now().Unix()
	tx, err := l.database.Begin()
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	if target != "RUNNING" {
		if _, err := tx.Exec(`INSERT INTO lease_events(lease_id,run_id,session_key,role,version,state,expires_at,binding_hash,created_at)
SELECT e.lease_id,e.run_id,e.session_key,e.role,e.version+1,'REVOKED',e.expires_at,e.binding_hash,?
FROM lease_events e WHERE e.run_id=? AND e.state='ACTIVE'
AND e.version=(SELECT MAX(latest.version) FROM lease_events latest WHERE latest.lease_id=e.lease_id)`, now, runID); err != nil {
			return Run{}, err
		}
	}
	result, err := tx.Exec(`INSERT INTO run_events(run_id,version,state,created_at)
SELECT ?,?,?,? WHERE (SELECT MAX(version) FROM run_events WHERE run_id=?)=?`, runID, run.Version+1, target, now, runID, expectedVersion)
	if err != nil {
		return Run{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return Run{}, errors.New("lifecycle compare-and-swap conflict")
	}
	if err := appendAudit(tx, "run_transition", runID, run.SessionKey, "", map[string]any{"state": target, "version": run.Version + 1}, now); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return l.Run(runID)
}

func (l *Ledger) Grant(grant Grant, currentSessionKey string) error {
	run, err := l.Run(grant.RunID)
	if err != nil {
		return err
	}
	if run.State != "RUNNING" || !hmac.Equal([]byte(run.SessionKey), []byte(currentSessionKey)) || grant.SessionKey != run.SessionKey {
		return errors.New("run identity mismatch")
	}
	if !validRole(grant.Role) {
		return errors.New("invalid role")
	}
	reads, err := canonicalScope(grant.ReadScope)
	if err != nil {
		return err
	}
	writes, err := canonicalScope(grant.WriteScope)
	if err != nil {
		return err
	}
	capabilities, err := canonicalCapabilities(grant.Capabilities)
	if err != nil {
		return err
	}
	if directorGrantInvalid(grant.Role, reads, writes, grant.Mutation, capabilities) {
		return errors.New("director scope must be empty")
	}
	if grant.Role != "director" && grant.Role != "controller" {
		for _, capability := range capabilities {
			if capability == "run:lifecycle" {
				return errors.New("lifecycle capability is coordination-only")
			}
		}
	}
	readJSON, _ := json.Marshal(reads)
	writeJSON, _ := json.Marshal(writes)
	capJSON, _ := json.Marshal(capabilities)
	tx, err := l.database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existingSession, existingReads, existingWrites, existingCapabilities string
	var existingMutation bool
	err = tx.QueryRow(`SELECT session_key,read_scope,write_scope,mutation,capabilities
FROM role_ledger WHERE run_id=? AND role=?`, grant.RunID, grant.Role).
		Scan(&existingSession, &existingReads, &existingWrites, &existingMutation, &existingCapabilities)
	if err == nil {
		if existingSession == grant.SessionKey && existingReads == string(readJSON) && existingWrites == string(writeJSON) && existingMutation == grant.Mutation && existingCapabilities == string(capJSON) {
			return nil
		}
		return errors.New("role grants are immutable")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := time.Now().Unix()
	if _, err = tx.Exec(`INSERT INTO role_ledger(run_id,session_key,role,read_scope,write_scope,mutation,capabilities,created_at)
VALUES (?,?,?,?,?,?,?,?)`, grant.RunID, grant.SessionKey, grant.Role, string(readJSON), string(writeJSON), grant.Mutation, string(capJSON), now); err != nil {
		return err
	}
	if err := appendAudit(tx, "role_registered", grant.RunID, grant.SessionKey, "", map[string]any{"role": grant.Role}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *Ledger) Attest(runID, currentSessionKey, role string, reads, writes []string, mutation bool, stagedHash string, leaseSeconds int) (Attestation, error) {
	run, err := l.Run(runID)
	if err != nil {
		return Attestation{}, err
	}
	if run.State != "RUNNING" || !hmac.Equal([]byte(run.SessionKey), []byte(currentSessionKey)) {
		return Attestation{}, errors.New("run identity mismatch")
	}
	if leaseSeconds < 1 || leaseSeconds > DefaultLeaseSeconds {
		return Attestation{}, errors.New("invalid lease")
	}
	if mutation && !hashPattern.MatchString(stagedHash) {
		return Attestation{}, errors.New("mutation attestation requires a staged hash")
	}
	if !mutation && stagedHash != "" {
		return Attestation{}, errors.New("read-only attestation cannot bind a staged hash")
	}
	reads, err = canonicalScope(reads)
	if err != nil {
		return Attestation{}, err
	}
	writes, err = canonicalScope(writes)
	if err != nil {
		return Attestation{}, err
	}
	var rawReads, rawWrites, rawCapabilities string
	var grantedMutation bool
	if err := l.database.QueryRow("SELECT read_scope,write_scope,mutation,capabilities FROM role_ledger WHERE run_id=? AND role=?", runID, role).Scan(&rawReads, &rawWrites, &grantedMutation, &rawCapabilities); err != nil {
		return Attestation{}, errors.New("role ledger does not allow attestation")
	}
	var grantReads, grantWrites, grantCapabilities []string
	if json.Unmarshal([]byte(rawReads), &grantReads) != nil || json.Unmarshal([]byte(rawWrites), &grantWrites) != nil ||
		json.Unmarshal([]byte(rawCapabilities), &grantCapabilities) != nil ||
		directorGrantInvalid(role, grantReads, grantWrites, grantedMutation, grantCapabilities) ||
		grantedMutation != mutation || !subset(reads, grantReads) || !subset(writes, grantWrites) {
		return Attestation{}, errors.New("scope exceeds role ledger")
	}
	leaseID, err := randomHex(16)
	if err != nil {
		return Attestation{}, err
	}
	token := Attestation{
		RunID: runID, SessionKey: run.SessionKey, ProjectID: run.ProjectID, ObjectiveHash: run.ObjectiveHash,
		Role: role, ReadScope: reads, WriteScope: writes, Mutation: mutation, StagedHash: stagedHash,
		DatabaseID: run.DatabaseID, LeaseID: leaseID, ExpiresAt: time.Now().Unix() + int64(leaseSeconds),
	}
	token.Signature = l.sign(token)
	claims, _ := json.Marshal(tokenClaims(token))
	binding := sha256.Sum256(claims)
	tx, err := l.database.Begin()
	if err != nil {
		return Attestation{}, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err = tx.Exec(`INSERT INTO lease_events(lease_id,run_id,session_key,role,version,state,expires_at,binding_hash,created_at)
VALUES (?,?,?,?,0,'ACTIVE',?,?,?)`, leaseID, runID, run.SessionKey, role, token.ExpiresAt, hex.EncodeToString(binding[:]), now); err != nil {
		return Attestation{}, err
	}
	if err := appendAudit(tx, "lease_issued", runID, run.SessionKey, leaseID, map[string]any{"role": role, "mutation": mutation}, now); err != nil {
		return Attestation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Attestation{}, err
	}
	return token, nil
}

func (l *Ledger) Verify(token Attestation, currentSessionKey string) error {
	if token.ExpiresAt <= time.Now().Unix() || !hmac.Equal([]byte(token.SessionKey), []byte(currentSessionKey)) {
		return errors.New("attestation is expired or session-bound")
	}
	expected := l.sign(token)
	if !hmac.Equal([]byte(expected), []byte(token.Signature)) {
		return errors.New("attestation signature is invalid")
	}
	var databaseID string
	if err := l.database.QueryRow("SELECT value FROM metadata WHERE key='database_id'").Scan(&databaseID); err != nil || databaseID != token.DatabaseID {
		return errors.New("attestation database binding is invalid")
	}
	run, err := l.Run(token.RunID)
	if err != nil || run.State != "RUNNING" || run.ProjectID != token.ProjectID || run.ObjectiveHash != token.ObjectiveHash || run.SessionKey != token.SessionKey || run.DatabaseID != token.DatabaseID {
		return errors.New("attestation run binding is invalid")
	}
	claims, _ := json.Marshal(tokenClaims(token))
	binding := sha256.Sum256(claims)
	var state, runID, sessionKey, role, bindingHash string
	var expiresAt int64
	err = l.database.QueryRow(`SELECT state,run_id,session_key,role,expires_at,binding_hash FROM lease_events
WHERE lease_id=? ORDER BY version DESC LIMIT 1`, token.LeaseID).Scan(&state, &runID, &sessionKey, &role, &expiresAt, &bindingHash)
	if err != nil || state != "ACTIVE" || runID != token.RunID || sessionKey != token.SessionKey || role != token.Role || expiresAt != token.ExpiresAt || bindingHash != hex.EncodeToString(binding[:]) {
		return errors.New("attestation lease is not active")
	}
	return nil
}

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
	return map[string]any{
		"database_id": token.DatabaseID, "expires_at": token.ExpiresAt, "lease_id": token.LeaseID,
		"mutation": token.Mutation, "objective_hash": token.ObjectiveHash, "project_id": token.ProjectID,
		"read_scope": token.ReadScope, "role": token.Role, "run_id": token.RunID, "session_key": token.SessionKey,
		"staged_hash": token.StagedHash, "write_scope": token.WriteScope,
	}
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

func canonicalScope(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.ToSlash(value)
		cleaned := filepath.ToSlash(filepath.Clean(value))
		lower := strings.ToLower(cleaned)
		if value == "" || value != cleaned || filepath.IsAbs(value) || strings.Contains(value, "\\") || strings.Contains(value, ":") || value == ".." || strings.HasPrefix(value, "../") || sensitiveScope(lower) {
			return nil, errors.New("scope contains sensitive or unsafe path")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func canonicalCapabilities(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "run:lifecycle" {
			return nil, errors.New("capability is invalid")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func directorGrantInvalid(role string, reads, writes []string, mutation bool, capabilities []string) bool {
	if role != "director" && role != "controller" {
		return false
	}
	if len(reads) != 0 || len(writes) != 0 || mutation {
		return true
	}
	for _, capability := range capabilities {
		if capability != "run:lifecycle" {
			return true
		}
	}
	return false
}

func sensitiveScope(value string) bool {
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if sensitiveScopeDirectories[part] {
			return true
		}
	}
	base := parts[len(parts)-1]
	if sensitiveScopeNames[base] || sensitiveConfigPattern.MatchString(base) || strings.HasPrefix(base, ".env") {
		return true
	}
	for _, ending := range sensitiveScopeEndings {
		if strings.HasSuffix(base, ending) {
			return true
		}
	}
	return false
}

func validRole(value string) bool {
	for _, role := range []string{"director", "controller", "worker", "reviewer", "git"} {
		if value == role {
			return true
		}
	}
	return false
}

func subset(values, allowed []string) bool {
	set := map[string]bool{}
	for _, value := range allowed {
		set[value] = true
	}
	for _, value := range values {
		if !set[value] {
			return false
		}
	}
	return true
}

func appendAudit(tx *sql.Tx, kind, runID, sessionKey, leaseID string, detail map[string]any, timestamp int64) error {
	encoded, _ := json.Marshal(detail)
	_, err := tx.Exec("INSERT INTO audit_events(created_at,kind,run_id,session_key,lease_id,detail) VALUES (?,?,?,?,?,?)", timestamp, kind, runID, sessionKey, nullable(leaseID), string(encoded))
	return err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

var orchestrationSchema = []string{
	`CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS runs(run_id TEXT PRIMARY KEY,session_key TEXT NOT NULL,project_id TEXT NOT NULL,objective_hash TEXT NOT NULL,database_id TEXT NOT NULL,created_at INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS run_events(run_id TEXT NOT NULL REFERENCES runs(run_id),version INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN ('RUNNING','PAUSED','STOPPED','DONE','BLOCKED')),created_at INTEGER NOT NULL,PRIMARY KEY(run_id,version))`,
	`CREATE TABLE IF NOT EXISTS role_ledger(run_id TEXT NOT NULL REFERENCES runs(run_id),session_key TEXT NOT NULL,role TEXT NOT NULL,read_scope TEXT NOT NULL,write_scope TEXT NOT NULL,mutation INTEGER NOT NULL CHECK(mutation IN (0,1)),capabilities TEXT NOT NULL,created_at INTEGER NOT NULL,CHECK(capabilities='[]' OR (role IN ('director','controller') AND capabilities='["run:lifecycle"]')),CHECK(role NOT IN ('director','controller') OR (read_scope='[]' AND write_scope='[]' AND mutation=0)),PRIMARY KEY(run_id,session_key,role))`,
	`CREATE TABLE IF NOT EXISTS lease_events(lease_id TEXT NOT NULL,run_id TEXT NOT NULL REFERENCES runs(run_id),session_key TEXT NOT NULL,role TEXT NOT NULL,version INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN ('ACTIVE','CONSUMED','REVOKED','EXPIRED')),expires_at INTEGER NOT NULL,binding_hash TEXT NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(lease_id,version))`,
	`CREATE TABLE IF NOT EXISTS audit_events(event_id INTEGER PRIMARY KEY AUTOINCREMENT,created_at INTEGER NOT NULL,kind TEXT NOT NULL,run_id TEXT NOT NULL,session_key TEXT NOT NULL,lease_id TEXT,detail TEXT NOT NULL)`,
	`CREATE TRIGGER IF NOT EXISTS no_metadata_update BEFORE UPDATE ON metadata BEGIN SELECT RAISE(ABORT,'immutable ledger'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_metadata_delete BEFORE DELETE ON metadata BEGIN SELECT RAISE(ABORT,'immutable ledger'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_runs_update BEFORE UPDATE ON runs BEGIN SELECT RAISE(ABORT,'immutable ledger'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_runs_delete BEFORE DELETE ON runs BEGIN SELECT RAISE(ABORT,'immutable ledger'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_run_events_update BEFORE UPDATE ON run_events BEGIN SELECT RAISE(ABORT,'append only'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_run_events_delete BEFORE DELETE ON run_events BEGIN SELECT RAISE(ABORT,'append only'); END`,
	`CREATE TRIGGER IF NOT EXISTS valid_run_transition BEFORE INSERT ON run_events BEGIN SELECT CASE WHEN
(NEW.version=0 AND NEW.state='RUNNING') OR (NEW.version>0 AND EXISTS (
SELECT 1 FROM run_events old WHERE old.run_id=NEW.run_id AND old.version=NEW.version-1 AND
((old.state='RUNNING' AND NEW.state IN ('PAUSED','STOPPED','DONE','BLOCKED')) OR
(old.state='PAUSED' AND NEW.state IN ('RUNNING','STOPPED','DONE','BLOCKED')))))
THEN 1 ELSE RAISE(ABORT,'invalid run transition') END; END`,
	`CREATE TRIGGER IF NOT EXISTS run_close_requires_revocation BEFORE INSERT ON run_events WHEN NEW.state!='RUNNING' BEGIN
SELECT CASE WHEN EXISTS (SELECT 1 FROM lease_events current JOIN
(SELECT lease_id,MAX(version) version FROM lease_events GROUP BY lease_id) latest
ON current.lease_id=latest.lease_id AND current.version=latest.version
WHERE current.run_id=NEW.run_id AND current.state='ACTIVE')
THEN RAISE(ABORT,'active lease must be revoked') END; END`,
	`CREATE TRIGGER IF NOT EXISTS no_roles_update BEFORE UPDATE ON role_ledger BEGIN SELECT RAISE(ABORT,'immutable ledger'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_roles_delete BEFORE DELETE ON role_ledger BEGIN SELECT RAISE(ABORT,'immutable ledger'); END`,
	`CREATE TRIGGER IF NOT EXISTS role_matches_run BEFORE INSERT ON role_ledger
WHEN NOT EXISTS (SELECT 1 FROM runs WHERE run_id=NEW.run_id AND session_key=NEW.session_key)
BEGIN SELECT RAISE(ABORT,'role session mismatch'); END`,
	`CREATE TRIGGER IF NOT EXISTS role_requires_running BEFORE INSERT ON role_ledger
WHEN NOT EXISTS (SELECT 1 FROM run_events WHERE run_id=NEW.run_id AND state='RUNNING'
AND version=(SELECT MAX(latest.version) FROM run_events latest WHERE latest.run_id=NEW.run_id))
BEGIN SELECT RAISE(ABORT,'role requires running run'); END`,
	`CREATE TRIGGER IF NOT EXISTS role_ledger_guard BEFORE INSERT ON role_ledger WHEN
NEW.role NOT IN ('director','controller','worker','reviewer','git') OR NEW.mutation NOT IN (0,1)
OR json_valid(NEW.read_scope)=0 OR json_type(NEW.read_scope)!='array'
OR json_valid(NEW.write_scope)=0 OR json_type(NEW.write_scope)!='array'
OR json_valid(NEW.capabilities)=0 OR json_type(NEW.capabilities)!='array'
BEGIN SELECT RAISE(ABORT,'invalid role grant'); END`,
	`CREATE TRIGGER IF NOT EXISTS director_zero_task_power BEFORE INSERT ON role_ledger
WHEN NEW.role IN ('director','controller') AND NOT (NEW.read_scope='[]' AND NEW.write_scope='[]'
AND NEW.mutation=0 AND NEW.capabilities IN ('[]','["run:lifecycle"]'))
BEGIN SELECT RAISE(ABORT,'director coordination only'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_lease_events_update BEFORE UPDATE ON lease_events BEGIN SELECT RAISE(ABORT,'append only'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_lease_events_delete BEFORE DELETE ON lease_events BEGIN SELECT RAISE(ABORT,'append only'); END`,
	`CREATE TRIGGER IF NOT EXISTS valid_lease_transition BEFORE INSERT ON lease_events BEGIN SELECT CASE WHEN
(NEW.version=0 AND NEW.state='ACTIVE') OR (NEW.version>0 AND EXISTS (
SELECT 1 FROM lease_events old WHERE old.lease_id=NEW.lease_id AND old.version=NEW.version-1
AND old.run_id=NEW.run_id AND old.session_key=NEW.session_key AND old.role=NEW.role
AND old.state='ACTIVE' AND NEW.state IN ('CONSUMED','REVOKED','EXPIRED')))
THEN 1 ELSE RAISE(ABORT,'invalid lease transition') END; END`,
	`CREATE TRIGGER IF NOT EXISTS lease_matches_role BEFORE INSERT ON lease_events WHEN NEW.version=0
AND NOT EXISTS (SELECT 1 FROM role_ledger WHERE run_id=NEW.run_id AND session_key=NEW.session_key AND role=NEW.role)
BEGIN SELECT RAISE(ABORT,'lease role mismatch'); END`,
	`CREATE TRIGGER IF NOT EXISTS lease_requires_running BEFORE INSERT ON lease_events WHEN NEW.version=0
AND NOT EXISTS (SELECT 1 FROM run_events WHERE run_id=NEW.run_id AND state='RUNNING'
AND version=(SELECT MAX(latest.version) FROM run_events latest WHERE latest.run_id=NEW.run_id))
BEGIN SELECT RAISE(ABORT,'lease requires running run'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_audit_update BEFORE UPDATE ON audit_events BEGIN SELECT RAISE(ABORT,'append only'); END`,
	`CREATE TRIGGER IF NOT EXISTS no_audit_delete BEFORE DELETE ON audit_events BEGIN SELECT RAISE(ABORT,'append only'); END`,
}
