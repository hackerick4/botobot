package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Item 1 (v0.5.3): the handle comes from the bot's own name first.
func TestHandleFromBotName(t *testing.T) {
	never := func(string) bool { return false }
	for _, c := range []struct{ bot, owner, prefix string }{
		{"Sample Desk", "Rick Chiu", "sample-desk-"},
		{"Grok Bot A", "Rick Chiu", "grok-bot-a-"},
		{"小幫手", "Rick Chiu", "xiaobangshou-"}, // v0.5.4: pinyin
		{"ハナコ", "Rick Chiu", "rick-chiu-bot-"},
		{"", "Rick Chiu", "rick-chiu-bot-"},
		{"", "", "bot-"},
	} {
		h := newHandle(c.bot, c.owner, never)
		if !strings.HasPrefix(h, c.prefix) || len(h) != len(c.prefix)+4 || !handleDirSafe.MatchString(h) {
			t.Errorf("newHandle(%q, %q) = %q, want %s????", c.bot, c.owner, h, c.prefix)
		}
	}
	url := startTestServer(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_HOME", "")
	out, e, rc := run(t, "create", "--server", url, "--name", "B", "--display-name", "Sample Desk", "--owner", "Rick Chiu")
	if rc != exitOK || !strings.Contains(out, `"handle":"sample-desk-`) {
		t.Fatalf("create with --display-name: %d %s %s", rc, out, e)
	}
	if _, e, rc := run(t, "create", "--server", url, "--name", "B", "--display-name", "BOT NAME", "--owner", "Rick Chiu"); rc != exitUsage || !strings.Contains(e, "placeholder_name") {
		t.Fatalf("copied BOT NAME: %d %s", rc, e)
	}
}

// Item 8 (RT-78): without BB_HOME the CLI never picks or names a per-bot
// folder, not even when only one other bot's folder is left.
func TestAutoHomeNeverNamesAnotherBot(t *testing.T) {
	url := startTestServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BB_HOME", "")
	if _, e, c := run(t, "create", "--server", url, "--name", "B", "--handle", "other-bot-ab12"); c != exitOK {
		t.Fatalf("create: %d %s", c, e)
	}
	for _, args := range [][]string{{"tick"}, {"bridges", "--offline"}} {
		o, e, c := run(t, args...)
		if c != exitUsage || !strings.Contains(e, "home_required") || strings.Contains(o+e, "other-bot-ab12") || !strings.Contains(e, "BB_HOME") || strings.Contains(e, "routine (not pause)") {
			t.Fatalf("%v: %d %s %s", args, c, o, e)
		}
	}
	// A gone folder (this bot's last bridge was cleaned up) says so.
	gone := filepath.Join(home, ".bot-bridge", "bots", "gone-bot-zz99")
	t.Setenv("BB_HOME", gone)
	o, _, c := run(t, "tick")
	if c != exitOK || o != noBridgesLine+"\n"+noBridgesNext+"\n" || strings.Contains(o, "other-bot") {
		t.Fatalf("tick of a gone folder: %d %q", c, o)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("tick recreated the folder: %v", err)
	}
}
