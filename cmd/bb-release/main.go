// Command bb-release is the release-box tool for the signed CLI update channel
// (docs/CLI_UPDATE.md). It never prints or transmits the private key.
//
//	bb-release keygen   --key ~/.config/bot-bridge/release-signing.key
//	bb-release pubkey   --key KEYFILE
//	bb-release manifest --version v0.4.0 --seq 2 --base-url https://botobot.app/dl \
//	                    --bin linux-amd64=dist/bot-bridge-cli-linux-amd64-v0.4.0 \
//	                    [--next-pub BASE64] [--days 30] > manifest.json
//	bb-release sign     --key KEYFILE [--key KEYFILE2] manifest.json > manifest.json.sig
//	bb-release verify   --pub BASE64 [--min-seq N] manifest.json manifest.json.sig
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hackerick4/botobot/internal/update"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: bb-release keygen|pubkey|manifest|sign|verify [flags]"))
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "pubkey":
		err = pubkey(os.Args[2:])
	case "manifest":
		err = manifest(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "bb-release:", err)
	os.Exit(1)
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must be mode 600", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a release signing key", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func pubOf(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	path := fs.String("key", "", "private key file to create (mode 600, must not exist)")
	_ = fs.Parse(args)
	if *path == "" {
		return errors.New("--key is required")
	}
	if err := os.MkdirAll(filepath.Dir(*path), 0o700); err != nil {
		return err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	f, err := os.OpenFile(*path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(seed) + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	fmt.Printf("public key: %s\nkey id:     %s\n", pubOf(priv), update.KeyID(pub))
	return nil
}

func pubkey(args []string) error {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	path := fs.String("key", "", "private key file")
	_ = fs.Parse(args)
	priv, err := loadKey(*path)
	if err != nil {
		return err
	}
	fmt.Println(pubOf(priv))
	return nil
}

func manifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ExitOnError)
	version := fs.String("version", "", "release version (vX.Y.Z)")
	seq := fs.Uint64("seq", 0, "sequence number (must increase every release)")
	base := fs.String("base-url", "https://botobot.app/dl", "download base URL")
	days := fs.Int("days", 30, "days until the manifest expires")
	var bins, nexts multi
	fs.Var(&bins, "bin", "platform=path (repeatable)")
	fs.Var(&nexts, "next-pub", "next release public key, base64 (repeatable)")
	_ = fs.Parse(args)
	now := time.Now().UTC().Truncate(time.Second)
	m := update.Manifest{
		Schema: update.ManifestSchema, Channel: "stable", Version: *version, Seq: *seq,
		IssuedAt: now, ExpiresAt: now.Add(time.Duration(*days) * 24 * time.Hour),
		Platforms: map[string]update.Platform{},
	}
	for _, b := range bins {
		plat, path, ok := strings.Cut(b, "=")
		if !ok {
			return fmt.Errorf("bad --bin %q", b)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return err
		}
		m.Platforms[plat] = update.Platform{
			URL:    strings.TrimRight(*base, "/") + "/" + filepath.Base(path),
			SHA256: hex.EncodeToString(h.Sum(nil)), Size: n,
		}
	}
	for _, p := range nexts {
		pub, err := update.ParsePub(p)
		if err != nil {
			return err
		}
		m.NextKeys = append(m.NextKeys, update.PubKey{KeyID: update.KeyID(pub), Pub: p})
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	// Round-trip through the client's own validation with a throwaway key so a
	// malformed manifest is caught before it is signed.
	_, tmpPriv, _ := ed25519.GenerateKey(rand.Reader)
	sf, _ := json.Marshal(update.SigFile{Schema: update.SigSchema, Sigs: []update.Sig{update.SignManifest(tmpPriv, out)}})
	kr := update.Keyring{update.KeyID(tmpPriv.Public().(ed25519.PublicKey)): tmpPriv.Public().(ed25519.PublicKey)}
	if _, err := update.Verify(out, sf, kr, now, 0); err != nil {
		return fmt.Errorf("manifest would be rejected by clients: %w", err)
	}
	_, err = os.Stdout.Write(out)
	return err
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	var keys multi
	fs.Var(&keys, "key", "private key file (repeatable, e.g. current and next during rotation)")
	_ = fs.Parse(args)
	if len(keys) == 0 || fs.NArg() != 1 {
		return errors.New("usage: sign --key KEY manifest.json")
	}
	body, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	sf := update.SigFile{Schema: update.SigSchema}
	for _, k := range keys {
		priv, err := loadKey(k)
		if err != nil {
			return err
		}
		sf.Sigs = append(sf.Sigs, update.SignManifest(priv, body))
	}
	out, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(out, '\n'))
	return err
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	var pubs multi
	fs.Var(&pubs, "pub", "trusted public key, base64 (repeatable)")
	minSeq := fs.Uint64("min-seq", 0, "reject unless seq is greater than this")
	_ = fs.Parse(args)
	if fs.NArg() != 2 {
		return errors.New("usage: verify --pub KEY manifest.json manifest.json.sig")
	}
	body, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(fs.Arg(1))
	if err != nil {
		return err
	}
	m, err := update.Verify(body, sig, update.NewKeyring(pubs...), time.Now(), *minSeq)
	if err != nil {
		return err
	}
	fmt.Printf("OK %s seq=%d expires=%s\n", m.Version, m.Seq, m.ExpiresAt.Format(time.RFC3339))
	return nil
}
