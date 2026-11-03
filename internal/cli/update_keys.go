package cli

// releasePubKeys are the pinned Ed25519 public keys of the CLI release signing
// key. The private key is kept offline by the maintainer;
// it is not on the server, in cloud storage or
// in CI. To rotate: announce the next key with `--next-pub` in a manifest
// signed by the current key, then add it here for the following release.
var releasePubKeys = []string{
	"iNGy6U+ZD9Se4uDfiDY8U1AUYz6bgB3CdjBDQJZmpks=", // key id 117a95e2967bf881 (2026-10-30)
}

// releaseSeq is this build's position in the signed release sequence. Set by
// scripts/release-cli.sh via -ldflags "-X .../internal/cli.releaseSeq=N".
// "0" means a development build: no automatic update checks.
var releaseSeq = "0"
