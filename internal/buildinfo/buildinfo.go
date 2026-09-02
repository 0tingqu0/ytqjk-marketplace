package buildinfo

const (
	Version = "0.7.2"
	Name    = "YTQJK"
)

// ReleaseEd25519PublicKeyDERBase64 and ReleaseEd25519PublicKeySHA256 are
// injected by the approved release build. Empty defaults intentionally make
// the updater fail closed instead of trusting release metadata alone.
var (
	ReleaseEd25519PublicKeyDERBase64 string
	ReleaseEd25519PublicKeySHA256    string
)
