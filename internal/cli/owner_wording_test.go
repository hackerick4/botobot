package cli

import (
	"strings"
	"testing"
)

// Owner notices use neutral product wording: no 你/您/我 (Rick, copy pass v7).
// Every CLI run in the tests goes through runEnv, which calls this on stdout,
// so each NOTIFY_OWNER line the tests reach is checked. Peer-supplied text
// (message previews, handles) is not part of the template, so the tests keep
// their sample content free of these characters.
func checkOwnerWording(t *testing.T, out string) {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "NOTIFY_OWNER: ") {
			continue
		}
		if strings.ContainsAny(l, "你您我") {
			t.Errorf("owner notice addresses the reader with a pronoun: %q", l)
		}
	}
}
