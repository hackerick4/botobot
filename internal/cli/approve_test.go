package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// approveHeld plays the owner: it takes the "held" record from out, ages the
// item past the minimum delay and out of its wake (as a later owner turn
// would be), and runs release. It returns release's output.
func approveHeld(t *testing.T, home, out string) (string, string, int) {
	t.Helper()
	h := findLine(t, out, "held")
	id, _ := h["id"].(string)
	hash, _ := h["hash"].(string)
	ageHold(t, home, id)
	return run(t, "release", id, "--home", home, "--hash", hash, "--owner-approved")
}

// ageHold moves a held item's creation time back past holdMinDelay and clears
// its wake id.
func ageHold(t *testing.T, home, id string) {
	t.Helper()
	p := filepath.Join(home, "held", id+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read held item: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["created_ms"] = m["created_ms"].(float64) - 120_000
	delete(m, "wake_id")
	nb, _ := json.Marshal(m)
	if err := os.WriteFile(p, nb, 0o600); err != nil {
		t.Fatal(err)
	}
}

// homeArg returns the --home value in args.
func homeArg(args []string) string {
	for i, a := range args {
		if a == "--home" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// runApproved runs a command that is held for the owner (admin actions,
// attachments) and immediately approves it.
func runApproved(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runStdinApproved(t, "", args...)
}

func runStdinApproved(t *testing.T, input string, args ...string) (string, string, int) {
	t.Helper()
	out, errOut, code := runStdin(t, input, args...)
	if code != exitOK || !strings.Contains(out, `"type":"held"`) {
		return out, errOut, code
	}
	return approveHeld(t, homeArg(args), out)
}

func regexpFind(s, pattern string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
