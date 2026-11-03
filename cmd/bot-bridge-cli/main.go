// Command bot-bridge-cli is the land-side client (DEV_SPEC §12). It owns the
// local keystore, talks to the bot-bridge server over HTTPS, and performs the
// end-to-end encryption via internal/protocol. All behaviour lives in
// internal/cli; this file is just the entry point and the ldflags version stamp.
package main

import (
	"os"

	"github.com/hackerick4/botobot/internal/cli"
)

// version is the CLI version, overridable at build time via
// -ldflags "-X main.version=<v>" (DEV_SPEC §12.3 version, §12.4).
var version = "v0.6.0"

func main() {
	os.Exit(cli.Run(os.Args[1:], version))
}
