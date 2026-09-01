package upgrade

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
)

func TestReleaseTrustRootFailsClosedWhenBuildValuesAreMissing(t *testing.T) {
	restoreBuildTrust(t, "", "")
	if _, _, err := releaseTrustRoot(); errorCode(err) != "RELEASE_TRUST_ROOT_MISSING" {
		t.Fatalf("trust error = %v", err)
	}
}

func TestVerifyReleaseEnvelopeAuthenticatesManifestAndRejectsTampering(t *testing.T) {
	release, manifestData, signaturesData := signedReleaseFixture(t)
	manifest, digest, err := verifyReleaseEnvelope(release, manifestData, signaturesData)
	if err != nil || manifest.Version != release.Version || digest != fmt.Sprintf("%x", sha256.Sum256(manifestData)) {
		t.Fatalf("verified manifest = %#v, %q, %v", manifest, digest, err)
	}

	tampered := append([]byte(nil), manifestData...)
	tampered[len(tampered)-1] ^= 1
	if _, _, err := verifyReleaseEnvelope(release, tampered, signaturesData); errorCode(err) != "RELEASE_SIGNATURE_INVALID" {
		t.Fatalf("tampered error = %v", err)
	}
}

func TestVerifyReleaseEnvelopeRejectsUntrustedAndDuplicateMetadata(t *testing.T) {
	release, manifestData, signaturesData := signedReleaseFixture(t)
	buildinfo.ReleaseEd25519PublicKeySHA256 = strings.Repeat("0", 64)
	if _, _, err := verifyReleaseEnvelope(release, manifestData, signaturesData); errorCode(err) != "RELEASE_TRUST_ROOT_INVALID" {
		t.Fatalf("trust mismatch error = %v", err)
	}

	_, _, signaturesData = signedReleaseFixture(t)
	duplicate := []byte(strings.Replace(string(signaturesData), `"schema":`, `"schema":"duplicate","schema":`, 1))
	if _, _, err := verifyReleaseEnvelope(release, manifestData, duplicate); errorCode(err) != "RELEASE_SIGNATURE_INVALID" {
		t.Fatalf("duplicate metadata error = %v", err)
	}
}

func signedReleaseFixture(t *testing.T) (Release, []byte, []byte) {
	return signedReleaseFixtureWithArchive(t, "", "", 0)
}

func signedReleaseFixtureWithArchive(
	t *testing.T,
	archiveName string,
	archiveSHA256 string,
	archiveSize int64,
) (Release, []byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicDigest := fmt.Sprintf("%x", sha256.Sum256(der))
	restoreBuildTrust(t, base64.StdEncoding.EncodeToString(der), publicDigest)

	release := Release{Version: "0.7.0", Tag: "v0.7.0"}
	manifest := releaseManifest{
		Schema: "ytqjk-release/v1", Version: release.Version, Tag: release.Tag,
		Commit: strings.Repeat("a", 40),
		PayloadAssets: []releaseManifestAsset{
			{Name: "SBOM.cdx.json", SHA256: strings.Repeat("1", 64), Size: 1},
			{Name: "ytqjk-linux-amd64", SHA256: strings.Repeat("2", 64), Size: 2},
			{Name: "ytqjk-linux-amd64.tar.gz", SHA256: strings.Repeat("3", 64), Size: 3},
			{Name: "ytqjk-python-rollback.zip", SHA256: strings.Repeat("4", 64), Size: 4},
			{Name: "ytqjk-windows-amd64.exe", SHA256: strings.Repeat("5", 64), Size: 5},
			{Name: "ytqjk-windows-amd64.zip", SHA256: strings.Repeat("6", 64), Size: 6},
		},
	}
	if archiveName != "" {
		found := false
		for index := range manifest.PayloadAssets {
			if manifest.PayloadAssets[index].Name != archiveName {
				continue
			}
			manifest.PayloadAssets[index].SHA256 = archiveSHA256
			manifest.PayloadAssets[index].Size = archiveSize
			found = true
		}
		if !found {
			t.Fatalf("archive fixture %s is unsupported", archiveName)
		}
	}
	manifest.Rollback.Asset = "ytqjk-python-rollback.zip"
	manifest.Rollback.Tag = "v0.6.10"
	manifest.Rollback.Commit = strings.Repeat("b", 40)
	manifest.SBOM.Asset = "SBOM.cdx.json"
	manifest.SBOM.Format = "CycloneDX"
	manifest.SBOM.SpecVersion = "1.5"
	manifest.Signature.Asset = "signatures.json"
	manifest.Signature.Algorithm = "Ed25519"
	manifest.Signature.KeyID = "test:key"
	manifest.Signature.PublicKeySHA256 = publicDigest
	manifest.Signature.SignedAsset = "release-manifest.json"
	manifest.StablePublication.Status = "REAL_ACCEPTANCE_REQUIRED"
	manifest.StablePublication.RequiredChecks = []string{
		"clean_install", "old_to_go", "go_to_python", "python_to_same_go", "zero_data_loss", "jit_authorization",
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifestData)
	signatures := releaseSignatures{
		Schema: "ytqjk-release-signatures/v1", Algorithm: "Ed25519", KeyID: "test:key",
		PublicKeySHA256: publicDigest, Payload: "release-manifest.json",
		PayloadSHA256:   fmt.Sprintf("%x", sha256.Sum256(manifestData)),
		SignatureBase64: base64.StdEncoding.EncodeToString(signature),
	}
	signaturesData, err := json.Marshal(signatures)
	if err != nil {
		t.Fatal(err)
	}
	return release, manifestData, signaturesData
}

func restoreBuildTrust(t *testing.T, encoded, digest string) {
	t.Helper()
	previousEncoded := buildinfo.ReleaseEd25519PublicKeyDERBase64
	previousDigest := buildinfo.ReleaseEd25519PublicKeySHA256
	buildinfo.ReleaseEd25519PublicKeyDERBase64 = encoded
	buildinfo.ReleaseEd25519PublicKeySHA256 = digest
	t.Cleanup(func() {
		buildinfo.ReleaseEd25519PublicKeyDERBase64 = previousEncoded
		buildinfo.ReleaseEd25519PublicKeySHA256 = previousDigest
	})
}
