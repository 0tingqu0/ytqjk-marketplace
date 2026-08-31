package peer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

const (
	settingsSchemaVersion = 1
	maxPeers              = 256
	maxReplayEntries      = 4096
)

var (
	ErrNotConfigured    = errors.New("PEER_CONFIG_NOT_CONFIGURED")
	ErrRevisionConflict = errors.New("PEER_REVISION_CONFLICT")
)

type Settings struct {
	SchemaVersion    int      `json:"schema_version"`
	Revision         int64    `json:"revision"`
	LocalPeerID      string   `json:"local_peer_id"`
	Enabled          bool     `json:"enabled"`
	BindHost         string   `json:"bind_host"`
	Port             int      `json:"port"`
	AllowInsecureLAN bool     `json:"allow_insecure_lan"`
	Peers            []Record `json:"peers"`
}

type PublicSettings struct {
	SchemaVersion    int            `json:"schema_version"`
	Revision         int64          `json:"revision"`
	LocalPeerID      string         `json:"local_peer_id"`
	Enabled          bool           `json:"enabled"`
	BindHost         string         `json:"bind_host"`
	Port             int            `json:"port"`
	AllowInsecureLAN bool           `json:"allow_insecure_lan"`
	Peers            []PublicRecord `json:"peers"`
}

type Store struct {
	database *sql.DB
	clock    func() time.Time
}

func OpenStore(path string) (*Store, error) {
	return openStore(path, time.Now)
}

func openStore(path string, clock func() time.Time) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + url.PathEscape(filepath.ToSlash(absolute)) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(2)
	store := &Store{database: database, clock: clock}
	if _, err := database.Exec(peerSchema); err != nil {
		database.Close()
		return nil, err
	}
	_ = os.Chmod(absolute, 0o600)
	return store, nil
}

func (s *Store) Close() error { return s.database.Close() }

func (s *Store) Bootstrap(ctx context.Context) (Settings, error) {
	identifier, err := randomLocalID()
	if err != nil {
		return Settings{}, err
	}
	settings := Settings{
		SchemaVersion: settingsSchemaVersion, LocalPeerID: identifier,
		BindHost: "127.0.0.1", Port: 8766, Peers: []Record{},
	}
	document, digest, err := encodeSettings(settings)
	if err != nil {
		return Settings{}, err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	now := s.clock().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO peer_config_state(singleton,revision,document,digest,updated_at) VALUES(1,0,?,?,?)`, document, digest, now)
	if err != nil {
		return Settings{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO peer_config_events(revision,digest,public_document,created_at) VALUES(0,?,?,?)`, digest, publicJSON(settings), now); err != nil {
			return Settings{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, err
	}
	return s.Load(ctx)
}

func (s *Store) Load(ctx context.Context) (Settings, error) {
	var revision int64
	var document, digest string
	err := s.database.QueryRowContext(ctx, `SELECT revision,document,digest FROM peer_config_state WHERE singleton=1`).Scan(&revision, &document, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, ErrNotConfigured
	}
	if err != nil {
		return Settings{}, err
	}
	return decodeSettings(revision, document, digest)
}

func (s *Store) Configure(ctx context.Context, expectedRevision int64, enabled bool, bindHost string, port int, allowInsecure bool) (Settings, error) {
	if err := ValidateLocal(enabled, bindHost, port, allowInsecure); err != nil {
		return Settings{}, err
	}
	return s.mutate(ctx, expectedRevision, func(value *Settings) error {
		value.Enabled = enabled
		value.BindHost = bindHost
		value.Port = port
		value.AllowInsecureLAN = allowInsecure
		return nil
	})
}

func (s *Store) Upsert(ctx context.Context, expectedRevision int64, record Record) (Settings, error) {
	normalized, err := ValidateRecord(record)
	if err != nil {
		return Settings{}, err
	}
	return s.mutate(ctx, expectedRevision, func(value *Settings) error {
		if normalized.PeerID == value.LocalPeerID {
			return errors.New("SELF_PEER_FORBIDDEN")
		}
		byID := make(map[string]Record, len(value.Peers)+1)
		for _, current := range value.Peers {
			byID[current.PeerID] = current
		}
		byID[normalized.PeerID] = normalized
		value.Peers = value.Peers[:0]
		for _, current := range byID {
			value.Peers = append(value.Peers, current)
		}
		sort.Slice(value.Peers, func(i, j int) bool { return value.Peers[i].PeerID < value.Peers[j].PeerID })
		return nil
	})
}

func (s *Store) Remove(ctx context.Context, expectedRevision int64, peerID string) (Settings, error) {
	if !validIdentifier(peerID) {
		return Settings{}, errors.New("INVALID_PEER_ID")
	}
	return s.mutate(ctx, expectedRevision, func(value *Settings) error {
		found := false
		peers := make([]Record, 0, len(value.Peers))
		for _, record := range value.Peers {
			if record.PeerID == peerID {
				found = true
				continue
			}
			peers = append(peers, record)
		}
		if !found {
			return errors.New("UNKNOWN_PEER")
		}
		value.Peers = peers
		return nil
	})
}

func (s *Store) AcceptReplay(ctx context.Context, peerID, nonce string, timestamp int64) (bool, error) {
	now := s.clock().Unix()
	window := int64(ReplayWindow / time.Second)
	if !validIdentifier(peerID) || !noncePattern.MatchString(nonce) || timestamp < now-window || timestamp > now+window {
		return false, errors.New("INVALID_PEER_REPLAY_INPUT")
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var future int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_replay_nonces WHERE timestamp>?`, now+window).Scan(&future); err != nil {
		return false, err
	}
	if future != 0 {
		return false, errors.New("PEER_REPLAY_STATE_INVALID")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM peer_replay_nonces WHERE timestamp<?`, now-window); err != nil {
		return false, err
	}
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_replay_nonces WHERE peer_id=? AND nonce=?`, peerID, nonce).Scan(&duplicate); err != nil {
		return false, err
	}
	if duplicate != 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer_replay_nonces`).Scan(&count); err != nil {
		return false, err
	}
	if count >= maxReplayEntries {
		return false, errors.New("PEER_REPLAY_CAPACITY_EXHAUSTED")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_replay_nonces(peer_id,nonce,timestamp,accepted_at) VALUES(?,?,?,?)`, peerID, nonce, timestamp, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (settings Settings) Peer(peerID string) (Record, bool) {
	index := sort.Search(len(settings.Peers), func(index int) bool { return settings.Peers[index].PeerID >= peerID })
	if index < len(settings.Peers) && settings.Peers[index].PeerID == peerID {
		return settings.Peers[index], true
	}
	return Record{}, false
}

func (settings Settings) Public() PublicSettings {
	peers := make([]PublicRecord, 0, len(settings.Peers))
	for _, record := range settings.Peers {
		peers = append(peers, record.Public())
	}
	return PublicSettings{
		SchemaVersion: settings.SchemaVersion, Revision: settings.Revision,
		LocalPeerID: settings.LocalPeerID, Enabled: settings.Enabled,
		BindHost: settings.BindHost, Port: settings.Port,
		AllowInsecureLAN: settings.AllowInsecureLAN, Peers: peers,
	}
}

func (s *Store) mutate(ctx context.Context, expectedRevision int64, change func(*Settings) error) (Settings, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	var revision int64
	var document, digest string
	err = tx.QueryRowContext(ctx, `SELECT revision,document,digest FROM peer_config_state WHERE singleton=1`).Scan(&revision, &document, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, ErrNotConfigured
	}
	if err != nil {
		return Settings{}, err
	}
	if revision != expectedRevision {
		return Settings{}, ErrRevisionConflict
	}
	value, err := decodeSettings(revision, document, digest)
	if err != nil {
		return Settings{}, err
	}
	if revision == int64(^uint64(0)>>1) {
		return Settings{}, errors.New("PEER_REVISION_EXHAUSTED")
	}
	if err := change(&value); err != nil {
		return Settings{}, err
	}
	value.Revision++
	document, digest, err = encodeSettings(value)
	if err != nil {
		return Settings{}, err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE peer_config_state SET revision=?,document=?,digest=?,updated_at=? WHERE singleton=1 AND revision=?`, value.Revision, document, digest, now, expectedRevision)
	if err != nil {
		return Settings{}, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return Settings{}, ErrRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO peer_config_events(revision,digest,public_document,created_at) VALUES(?,?,?,?)`, value.Revision, digest, publicJSON(value), now); err != nil {
		return Settings{}, err
	}
	var readback, readbackDigest string
	if err := tx.QueryRowContext(ctx, `SELECT document,digest FROM peer_config_state WHERE singleton=1`).Scan(&readback, &readbackDigest); err != nil || readback != document || readbackDigest != digest {
		return Settings{}, errors.New("PEER_CONFIG_READBACK_FAILED")
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, err
	}
	return value, nil
}

func encodeSettings(value Settings) (string, string, error) {
	if err := validateSettings(value); err != nil {
		return "", "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(digest[:]), nil
}

func decodeSettings(revision int64, document, suppliedDigest string) (Settings, error) {
	digest := sha256.Sum256([]byte(document))
	if !signaturePattern.MatchString(suppliedDigest) || !hmac.Equal([]byte(suppliedDigest), []byte(hex.EncodeToString(digest[:]))) {
		return Settings{}, errors.New("PEER_CONFIG_DIGEST_MISMATCH")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(document))
	decoder.DisallowUnknownFields()
	var value Settings
	if err := decoder.Decode(&value); err != nil {
		return Settings{}, errors.New("PEER_CONFIG_INVALID")
	}
	if decoder.Decode(&struct{}{}) != io.EOF || value.Revision != revision {
		return Settings{}, errors.New("PEER_CONFIG_INVALID")
	}
	if err := validateSettings(value); err != nil {
		return Settings{}, errors.New("PEER_CONFIG_INVALID")
	}
	return value, nil
}

func validateSettings(value Settings) error {
	if value.SchemaVersion != settingsSchemaVersion || value.Revision < 0 || !validIdentifier(value.LocalPeerID) || len(value.Peers) > maxPeers {
		return errors.New("PEER_CONFIG_INVALID")
	}
	if err := ValidateLocal(value.Enabled, value.BindHost, value.Port, value.AllowInsecureLAN); err != nil {
		return err
	}
	prior := ""
	for _, record := range value.Peers {
		normalized, err := ValidateRecord(record)
		if err != nil || normalized.PeerID == value.LocalPeerID || normalized.PeerID <= prior {
			return errors.New("PEER_CONFIG_INVALID")
		}
		prior = normalized.PeerID
	}
	return nil
}

func randomLocalID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "peer-" + hex.EncodeToString(value), nil
}

func publicJSON(value Settings) string {
	encoded, _ := json.Marshal(value.Public())
	return string(encoded)
}

const peerSchema = `
CREATE TABLE IF NOT EXISTS peer_config_state (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), revision INTEGER NOT NULL CHECK(revision>=0),
 document TEXT NOT NULL, digest TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS peer_config_events (
 revision INTEGER PRIMARY KEY, digest TEXT NOT NULL, public_document TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS peer_replay_nonces (
 peer_id TEXT NOT NULL, nonce TEXT NOT NULL, timestamp INTEGER NOT NULL,
 accepted_at INTEGER NOT NULL, PRIMARY KEY(peer_id,nonce)
);
CREATE TRIGGER IF NOT EXISTS peer_config_events_no_update BEFORE UPDATE ON peer_config_events
BEGIN SELECT RAISE(ABORT,'peer config events are append-only'); END;
CREATE TRIGGER IF NOT EXISTS peer_config_events_no_delete BEFORE DELETE ON peer_config_events
BEGIN SELECT RAISE(ABORT,'peer config events are append-only'); END;
`
