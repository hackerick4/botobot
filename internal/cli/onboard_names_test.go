package cli

import (
	"strings"
	"testing"
)

func newBridge(t *testing.T) (homeA, link, code, bridgeID string) {
	t.Helper()
	url := startTestServer(t)
	homeA = t.TempDir()
	out, errOut, rc := run(t, "create", "--home", homeA, "--server", url, "--name", "N", "--handle", "admin-bot", "--owner", "Dana")
	if rc != exitOK {
		t.Fatalf("create: %d %s", rc, errOut)
	}
	c := findLine(t, out, "created")
	if c["poll_interval_sec"] != nil && c["poll_interval_sec"].(float64) != 300 {
		t.Fatalf("create poll_interval_sec = %v, want 300", c["poll_interval_sec"])
	}
	return homeA, c["invite_link"].(string), c["invite_code"].(string), c["bridge_id"].(string)
}

// Secrets on the command line are refused, and the value is never echoed.
func TestSecretInArgvRefused(t *testing.T) {
	_, link, code, _ := newBridge(t)
	home := t.TempDir()
	out, errOut, rc := runEnv(t, "", nil, "join", "--home", home, "--link", link, "--code", code, "--handle", "amy-bot")
	if rc != exitUsage || !strings.Contains(errOut, "secret_in_argv") || strings.Contains(out+errOut, code) {
		t.Fatalf("join --code: %d out=%s err=%s", rc, out, errOut)
	}
	out, errOut, rc = runEnv(t, "", nil, "claim", "--home", home, "--link", link, "--token", "bbc_secretvalue123", "--handle", "amy-bot")
	if rc != exitUsage || !strings.Contains(errOut, "secret_in_argv") || strings.Contains(out+errOut, "bbc_secretvalue123") {
		t.Fatalf("claim --token: %d out=%s err=%s", rc, out, errOut)
	}
	// The env form still works, and the land reports the 5-minute interval.
	out, errOut, rc = runEnv(t, "", map[string]string{"BB_CODE": code}, "join", "--home", home, "--link", link, "--handle", "amy-bot", "--owner", "Amy")
	if rc != exitOK {
		t.Fatalf("join via BB_CODE: %d %s", rc, errOut)
	}
	if v := findLine(t, out, "joined")["poll_interval_sec"]; v != float64(300) {
		t.Fatalf("joined poll_interval_sec = %v, want 300", v)
	}
}

// Copied example names are refused before anything is written or sent.
func TestPlaceholderNamesRefused(t *testing.T) {
	_, link, code, _ := newBridge(t)
	for _, args := range [][]string{
		{"--handle", "my-bot", "--owner", "Amy"},
		{"--handle", "HANDLE", "--owner", "Amy"},
		{"--handle", "<handle>", "--owner", "Amy"},
		{"--handle", "amy-bot", "--owner", "你的主人"},
		{"--handle", "amy-bot", "--owner", "OWNER NAME"},
	} {
		home := t.TempDir()
		a := append([]string{"join", "--home", home, "--link", link}, args...)
		_, errOut, rc := runEnv(t, "", map[string]string{"BB_CODE": code}, a...)
		if rc != exitUsage || !strings.Contains(errOut, "placeholder_name") {
			t.Fatalf("%v: %d %s", args, rc, errOut)
		}
	}
	// The code was not used up by the refusals.
	_, errOut, rc := runEnv(t, "", map[string]string{"BB_CODE": code}, "join", "--home", t.TempDir(), "--link", link, "--owner", "Amy Lin")
	if rc != exitOK {
		t.Fatalf("real join after refusals: %d %s", rc, errOut)
	}
}

// Without --handle the CLI derives one from a latin owner name.
func TestDefaultHandleFromOwner(t *testing.T) {
	for in, want := range map[string]string{"Amy Lin": "amy-lin-bot", "dana": "dana-bot", "瑞克": "ruike-bot", "ハナコ": "", "": ""} {
		if got := defaultHandle("", in); got != want {
			t.Errorf("defaultHandle(%q) = %q, want %q", in, got, want)
		}
	}
	if defaultHandle("x-bot", "Amy") != "x-bot" {
		t.Error("explicit handle must win")
	}
}

// status --bridge ID without MSG_ID shows this bot's state on the bridge.
func TestStatusBridgeWithoutMsgID(t *testing.T) {
	p := setupPair(t)
	run(t, "tick", "--home", p.homeA, "--bridge", p.bridgeID)
	out, errOut, rc := run(t, "status", "--home", p.homeA, "--bridge", p.bridgeID)
	if rc != exitOK {
		t.Fatalf("status: %d %s", rc, errOut)
	}
	st := findLine(t, out, "bridge_status")
	if st["status"] != "open" || st["role"] != "admin" || st["trust"] != "ok" || st["held"] != float64(0) || st["poll_interval_sec"] != float64(300) {
		t.Fatalf("bridge_status: %v", st)
	}
	// Message status still works.
	out, _, rc = runStdin(t, "hello\n", "send", "--home", p.homeA, "--bridge", p.bridgeID, "--stdin")
	id, _ := findLine(t, out, "sent")["msg_id"].(string)
	if rc != exitOK || id == "" {
		t.Fatalf("send: %d %s", rc, out)
	}
	out, errOut, rc = run(t, "status", "--home", p.homeA, "--bridge", p.bridgeID, id)
	if rc != exitOK || findLine(t, out, "status") == nil {
		t.Fatalf("status MSG_ID: %d %s %s", rc, out, errOut)
	}
}

func TestHelpKeepsSecretsInEnv(t *testing.T) {
	out, _, _ := run(t, "help")
	if strings.Contains(out, "--code") || strings.Contains(out, "--token") ||
		!strings.Contains(out, "join --invite-stdin") || !strings.Contains(out, "claim --new") || !strings.Contains(out, "BB_CLAIM_TOKEN='TOKEN' bot-bridge-cli claim") {
		t.Fatalf("help: %s", out)
	}
	out, _, _ = run(t, "status", "--help")
	if !strings.Contains(out, "status --bridge ID [MSG_ID]") {
		t.Fatalf("status help: %s", out)
	}
}

// config with no flags shows the settings instead of failing.
func TestConfigShow(t *testing.T) {
	p := setupPair(t)
	out, errOut, rc := run(t, "config", "--home", p.homeB, "--bridge", p.bridgeID)
	if rc != exitOK || findLine(t, out, "config")["poll_interval_sec"] != float64(300) {
		t.Fatalf("config: %d %s %s", rc, out, errOut)
	}
}
