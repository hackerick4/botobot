package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// app holds process-level dependencies, injectable for tests (DEV_SPEC §17.3).
type app struct {
	version string
	stdout  io.Writer
	stderr  io.Writer
	stdin   io.Reader
	now     func() time.Time
	// sleep is time.Sleep; tests replace it so release's wait is instant.
	sleep func(time.Duration)
	// releasing is set while `release` re-runs a held item, so the item is
	// executed instead of being held again (hold.go).
	releasing bool
	// releasingItem is the held item being released (v0.6.1), so a command
	// can fold its confirmation into its own bot-facing output.
	releasingItem *heldItem
	// holdSink, when set, collects held items instead of printing each one
	// (v0.6.2: delete-menu --all holds every removal, then prints one
	// release line).
	holdSink *[]*heldItem
	// batchWaited lists held ids whose delay releaseBatch already waited
	// out (v0.6.2), so their own release does not wait again.
	batchWaited map[string]bool
	// env overrides os.Getenv for the secret variables (tests only).
	env map[string]string
	// exe is os.Executable; tests leave it nil. It lets the routine line use an
	// absolute CLI path when bot-bridge-cli is not on PATH.
	exe func() (string, error)
}

// getenv reads an environment variable, honouring test overrides.
func (a *app) getenv(k string) string {
	if v, ok := a.env[k]; ok {
		return v
	}
	return os.Getenv(k)
}

// Run is the CLI entry point. It dispatches the subcommand, writes any error as
// the single-line stderr object (DEV_SPEC §12.3), and returns the process exit
// code. cmd/bot-bridge-cli/main.go passes os.Args[1:] and the ldflags version.
func Run(args []string, version string) int {
	a := &app{version: version, stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin, now: time.Now, sleep: time.Sleep, exe: os.Executable}
	return a.run(args)
}

func (a *app) run(args []string) int {
	if len(args) == 0 {
		writeErrorLine(a.stderr, usageErrorf("no command given; run: bot-bridge-cli help"))
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	if isHelpArg(cmd) {
		topic := ""
		if len(rest) > 0 {
			topic = rest[0]
		}
		_, _ = fmt.Fprint(a.stdout, helpText(topic))
		return exitOK
	}
	if len(rest) > 0 && isHelpArg(rest[0]) && rest[0] != "help" {
		_, _ = fmt.Fprint(a.stdout, helpText(cmd))
		return exitOK
	}
	if cmd == "--version" || cmd == "-version" || cmd == "-v" {
		_, _ = fmt.Fprintln(a.stdout, versionLine(a.version))
		return exitOK
	}
	fn, ok := commands[cmd]
	if !ok {
		writeErrorLine(a.stderr, usageErrorf("unknown command %q", cmd))
		return exitUsage
	}
	// Removal commands never fall back to a default bridge: the owner must have
	// picked one by id (the /delete manual). Checked before any hold so a held
	// item can never be "leave the default bridge".
	if ce := requireExplicitBridge(cmd, rest); ce != nil {
		writeErrorLine(a.stderr, ce)
		return ce.Exit
	}
	// Admin actions wait for the owner's confirmation (EXFIL_DEFENSE.md §3.0).
	if pred, gated := adminGated[cmd]; gated {
		fn = gateAdmin(cmd, fn, pred)
	}
	if err := fn(a, rest); err != nil {
		writeErrorLine(a.stderr, err)
		return err.Exit
	}
	return exitOK
}

// commandFunc is one subcommand (DEV_SPEC §12.3).
type commandFunc func(a *app, args []string) *cliError

// commands is the subcommand table (DEV_SPEC §12.3). Each command file registers
// its entries in an init() so the set is additive across the command groups.
var commands = map[string]commandFunc{}

// globalFlags are the flags common to every subcommand (DEV_SPEC §12.2).
type globalFlags struct {
	bridge  string
	server  string
	home    string
	pretty  bool
	timeout time.Duration
}

// registerGlobals installs the global flags on fs. --server defaults from
// $BB_SERVER (DEV_SPEC §12.2); --home from $BB_HOME is resolved later so the
// flag can still override it.
func registerGlobals(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	fs.StringVar(&g.bridge, "bridge", "", "bridge id (required when more than one is joined)")
	fs.StringVar(&g.server, "server", os.Getenv("BB_SERVER"), "server base URL (create/join only)")
	fs.StringVar(&g.home, "home", "", "state directory (overrides $BB_HOME)")
	fs.BoolVar(&g.pretty, "pretty", false, "human-readable output instead of JSON lines")
	fs.DurationVar(&g.timeout, "timeout", 30*time.Second, "per-request timeout")
	return g
}

// parse builds a FlagSet for cmd, registers the global flags plus whatever
// extra registers, parses args, and returns the globals. Parse failures become
// usage errors (exit 2).
func (a *app) parse(cmd string, args []string, register func(fs *flag.FlagSet)) (*globalFlags, *flag.FlagSet, *cliError) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render errors ourselves as JSON lines
	g := registerGlobals(fs)
	if register != nil {
		register(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, usageErrorf("%s: %v", cmd, err)
	}
	return g, fs, nil
}

// session bundles the keystore, printer, and request settings for a command.
type session struct {
	// removal is forget's note about a released leave/close of the bridge.
	removal string
	// memberEntryClosed is set by membership: the 410 said the bridge's public
	// entry was closed (v0.6.1).
	memberEntryClosed bool
	app               *app
	ks                *keystore
	pr                *printer
	g                 *globalFlags
}

// newSession resolves BB_HOME, opens the keystore, and prepares the printer.
func (a *app) newSession(g *globalFlags) (*session, *cliError) {
	home, err := resolveHome(g.home, os.Getenv("BB_HOME"))
	if err != nil {
		return nil, generalErrorf("%v", err)
	}
	if g.home == "" && os.Getenv("BB_HOME") == "" {
		h, ce := autoHome()
		if ce != nil {
			return nil, ce
		}
		home = h
	}
	ks, err := openKeystore(home)
	if err != nil {
		return nil, generalErrorf("%v", err)
	}
	// Route keystore warnings (e.g. a loose-permission secret file that was
	// tightened) to stderr so they never pollute the JSON-lines stdout contract.
	ks.warn = func(format string, args ...any) {
		fmt.Fprintf(a.stderr, "bot-bridge-cli: "+format+"\n", args...)
	}
	return &session{app: a, ks: ks, pr: newPrinter(a.stdout, g.pretty), g: g}, nil
}

// bridgeState is a resolved, authenticated bridge: its on-disk config plus an
// HTTP client bound to its server and API key.
type bridgeState struct {
	bc  *bridgeConfig
	cli *client
}

// resolveBridge selects the active bridge for read-only / admin commands
// (DEV_SPEC §12.2): the --bridge flag, else config.json's default, else the sole
// joined bridge; more than one with no selection is a usage error.
//
// Since v0.4.8 every bridge-scoped command is strict: with more than one bridge
// in this folder, --bridge is always required, so no admin action, status, or
// message ever lands on an implicit default bridge.
func (s *session) resolveBridge() (*bridgeState, *cliError) { return s.resolveBridgeWith(true) }

// resolveBridgeStrict is resolveBridge for the message-moving commands
// (send/poll/tick/reply): when more than one bridge is configured it refuses the
// implicit default and requires an explicit --bridge. This is what stops a reply
// to a message from bridge B being cross-posted into the default bridge A (Batch
// 1 item 3). Routine prompts always hard-code --bridge, so this never bites them.
func (s *session) resolveBridgeStrict() (*bridgeState, *cliError) { return s.resolveBridgeWith(true) }

// resolveBridgeWith loads bridge.json and the API key for the selected bridge and
// builds the client. strict controls multi-bridge handling (see above).
func (s *session) resolveBridgeWith(strict bool) (*bridgeState, *cliError) {
	id := s.g.bridge
	if id == "" {
		bridges, err := s.ks.listBridges()
		if err != nil {
			return nil, generalErrorf("list bridges: %v", err)
		}
		switch len(bridges) {
		case 0:
			return nil, usageErrorf("no bridges joined; run create, join, or claim first")
		case 1:
			id = bridges[0].BridgeID
		default:
			if strict {
				return nil, &cliError{Code: "bridge_required", Message: fmt.Sprintf("this folder has %d bridges; pass --bridge br_… (bot-bridge-cli bridges lists them)", len(bridges)), Exit: exitUsage}
			}
			cfg, err := s.ks.loadConfig()
			if err != nil {
				return nil, generalErrorf("read config: %v", err)
			}
			if cfg.DefaultBridge == "" {
				return nil, usageErrorf("multiple bridges joined; pass --bridge <id>")
			}
			id = cfg.DefaultBridge
		}
	}

	bc, err := s.ks.loadBridge(id)
	if err != nil && os.IsNotExist(err) {
		// v0.6.1: a public entry line that joined after the last tick is
		// listed by `entry status` but has no local folder yet; fetch it with
		// the entry key (the server only answers for this entry's own lines).
		if lb := s.materializeEntryLine(id); lb != nil {
			bc, err = lb, nil
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			return nil, usageErrorf("bridge %q is not in the local state", id)
		}
		return nil, generalErrorf("read bridge state: %v", err)
	}
	apiKey, err := s.ks.loadAPIKey(id)
	if err != nil {
		return nil, generalErrorf("read api key: %v", err)
	}
	hc := &http.Client{Timeout: s.g.timeout}
	return &bridgeState{bc: bc, cli: newClient(hc, bc.Server, apiKey)}, nil
}

// explicitBridgeCmds are the destructive commands that need --bridge <id> even
// when only one bridge is joined. forget also accepts --all (with --yes).
var explicitBridgeCmds = map[string]bool{"leave": true, "close": true, "forget": true}

func requireExplicitBridge(cmd string, args []string) *cliError {
	if !explicitBridgeCmds[cmd] {
		return nil
	}
	if cmd == "forget" && hasBoolFlag(args, "all") {
		return nil
	}
	if bridgeFlagValue(args) != "" {
		return nil
	}
	return &cliError{Code: "bridge_required", Exit: exitUsage, Message: cmd +
		": --bridge <id> is required, even with only one bridge. Run bot-bridge-cli bridges, show the list to your owner and ask which one; never pick for them"}
}

// bridgeFlagValue returns the trimmed value of --bridge (or --bridge=X) in args,
// or "" when it is missing or empty.
func bridgeFlagValue(args []string) string {
	for i, a := range args {
		if a == "--" {
			return ""
		}
		t := strings.TrimLeft(a, "-")
		if len(t) == len(a) {
			continue
		}
		if t == "bridge" {
			if i+1 < len(args) {
				return strings.TrimSpace(args[i+1])
			}
			return ""
		}
		if strings.HasPrefix(t, "bridge=") {
			return strings.TrimSpace(strings.TrimPrefix(t, "bridge="))
		}
	}
	return ""
}
