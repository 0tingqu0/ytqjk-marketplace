package upgrade

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	keyIDPattern  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

type releaseManifestAsset struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type releaseManifest struct {
	Schema        string                 `json:"schema"`
	Version       string                 `json:"version"`
	Tag           string                 `json:"tag"`
	Commit        string                 `json:"commit"`
	PayloadAssets []releaseManifestAsset `json:"payload_assets"`
	Rollback      struct {
		Asset  string `json:"asset"`
		Tag    string `json:"tag"`
		Commit string `json:"commit"`
	} `json:"rollback"`
	SBOM struct {
		Asset       string `json:"asset"`
		Format      string `json:"format"`
		SpecVersion string `json:"spec_version"`
	} `json:"sbom"`
	Signature struct {
		Asset           string `json:"asset"`
		Algorithm       string `json:"algorithm"`
		KeyID           string `json:"key_id"`
		PublicKeySHA256 string `json:"public_key_sha256"`
		SignedAsset     string `json:"signed_asset"`
	} `json:"signature"`
	StablePublication struct {
		Status         string   `json:"status"`
		RequiredChecks []string `json:"required_checks"`
	} `json:"stable_publication"`
}

type releaseSignatures struct {
	Schema          string `json:"schema"`
	Algorithm       string `json:"algorithm"`
	KeyID           string `json:"key_id"`
	PublicKeySHA256 string `json:"public_key_sha256"`
	Payload         string `json:"payload"`
	PayloadSHA256   string `json:"payload_sha256"`
	SignatureBase64 string `json:"signature_base64"`
}

func verifyReleaseEnvelope(
	release Release,
	manifestData []byte,
	signaturesData []byte,
) (releaseManifest, string, error) {
	publicKey, publicDigest, err := releaseTrustRoot()
	if err != nil {
		return releaseManifest{}, "", err
	}
	payloadDigest := fmt.Sprintf("%x", sha256.Sum256(manifestData))
	var signatures releaseSignatures
	if err := decodeStrictJSON(signaturesData, &signatures); err != nil {
		return releaseManifest{}, "", failure("RELEASE_SIGNATURE_INVALID", err)
	}
	if signatures.Schema != "ytqjk-release-signatures/v1" ||
		signatures.Algorithm != "Ed25519" || signatures.Payload != "release-manifest.json" ||
		!keyIDPattern.MatchString(signatures.KeyID) || signatures.PublicKeySHA256 != publicDigest ||
		subtle.ConstantTimeCompare([]byte(signatures.PayloadSHA256), []byte(payloadDigest)) != 1 {
		return releaseManifest{}, "", failure("RELEASE_SIGNATURE_INVALID", nil)
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(signatures.SignatureBase64)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(publicKey, manifestData, signature) {
		return releaseManifest{}, "", failure("RELEASE_SIGNATURE_INVALID", err)
	}
	var manifest releaseManifest
	if err := decodeStrictJSON(manifestData, &manifest); err != nil {
		return releaseManifest{}, "", failure("RELEASE_MANIFEST_INVALID", err)
	}
	if err := validateReleaseManifest(manifest, release, signatures, publicDigest); err != nil {
		return releaseManifest{}, "", err
	}
	return manifest, payloadDigest, nil
}

func releaseTrustRoot() (ed25519.PublicKey, string, error) {
	encoded := buildinfo.ReleaseEd25519PublicKeyDERBase64
	expectedDigest := buildinfo.ReleaseEd25519PublicKeySHA256
	if encoded == "" || expectedDigest == "" {
		return nil, "", failure("RELEASE_TRUST_ROOT_MISSING", nil)
	}
	if strings.TrimSpace(encoded) != encoded || !hexDigestPattern.MatchString(expectedDigest) {
		return nil, "", failure("RELEASE_TRUST_ROOT_INVALID", nil)
	}
	der, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, "", failure("RELEASE_TRUST_ROOT_INVALID", err)
	}
	actualDigest := fmt.Sprintf("%x", sha256.Sum256(der))
	if subtle.ConstantTimeCompare([]byte(actualDigest), []byte(expectedDigest)) != 1 {
		return nil, "", failure("RELEASE_TRUST_ROOT_INVALID", nil)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, "", failure("RELEASE_TRUST_ROOT_INVALID", err)
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, "", failure("RELEASE_TRUST_ROOT_INVALID", nil)
	}
	return append(ed25519.PublicKey(nil), publicKey...), actualDigest, nil
}

func validateReleaseManifest(
	manifest releaseManifest,
	release Release,
	signatures releaseSignatures,
	publicDigest string,
) error {
	if manifest.Schema != "ytqjk-release/v1" || manifest.Version != release.Version ||
		manifest.Tag != release.Tag || !commitPattern.MatchString(manifest.Commit) {
		return failure("RELEASE_MANIFEST_INVALID", nil)
	}
	if manifest.Rollback.Asset != "ytqjk-python-rollback.zip" ||
		!strings.HasPrefix(manifest.Rollback.Tag, "v") ||
		!commitPattern.MatchString(manifest.Rollback.Commit) {
		return failure("RELEASE_MANIFEST_INVALID", nil)
	}
	if _, err := parseVersion(strings.TrimPrefix(manifest.Rollback.Tag, "v")); err != nil {
		return failure("RELEASE_MANIFEST_INVALID", err)
	}
	if manifest.SBOM.Asset != "SBOM.cdx.json" || manifest.SBOM.Format != "CycloneDX" ||
		manifest.SBOM.SpecVersion != "1.5" {
		return failure("RELEASE_MANIFEST_INVALID", nil)
	}
	if manifest.Signature.Asset != "signatures.json" || manifest.Signature.Algorithm != "Ed25519" ||
		manifest.Signature.KeyID != signatures.KeyID || manifest.Signature.PublicKeySHA256 != publicDigest ||
		manifest.Signature.SignedAsset != "release-manifest.json" {
		return failure("RELEASE_MANIFEST_INVALID", nil)
	}
	expectedChecks := []string{
		"clean_install", "old_to_go", "go_to_python", "python_to_same_go", "zero_data_loss", "jit_authorization",
	}
	if manifest.StablePublication.Status != "REAL_ACCEPTANCE_REQUIRED" ||
		!equalStrings(manifest.StablePublication.RequiredChecks, expectedChecks) {
		return failure("RELEASE_MANIFEST_INVALID", nil)
	}
	expectedAssets := map[string]bool{
		"SBOM.cdx.json": false, "ytqjk-linux-amd64": false,
		"ytqjk-linux-amd64.tar.gz": false, "ytqjk-python-rollback.zip": false,
		"ytqjk-windows-amd64.exe": false, "ytqjk-windows-amd64.zip": false,
	}
	if len(manifest.PayloadAssets) != len(expectedAssets) {
		return failure("RELEASE_MANIFEST_INVALID", nil)
	}
	for _, asset := range manifest.PayloadAssets {
		seen, known := expectedAssets[asset.Name]
		if !known || seen || !hexDigestPattern.MatchString(asset.SHA256) || asset.Size < 1 || asset.Size > maxBinaryBytes {
			return failure("RELEASE_MANIFEST_INVALID", nil)
		}
		expectedAssets[asset.Name] = true
	}
	return nil
}

func (manifest releaseManifest) asset(name string) (releaseManifestAsset, bool) {
	for _, candidate := range manifest.PayloadAssets {
		if candidate.Name == name {
			return candidate, true
		}
	}
	return releaseManifestAsset{}, false
}

func decodeStrictJSON(data []byte, target any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is invalid")
			}
			if _, duplicate := keys[key]; duplicate {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			keys[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(map[json.Delim]json.Delim{'{': '}', '[': ']'}[delimiter]) {
		return errors.New("JSON delimiter is not closed")
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
