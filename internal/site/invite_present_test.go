package site_test

import (
	"strings"
	"testing"
)

// Rick 2026-11-10: a Grok Bot creating a channel merged the notice and the
// invite into one plain chat bubble, with the INVITE_MESSAGE_BEGIN/END markers
// still in it and links auto-rendered as pills. The Create section of the
// manual must now tell the assistant to send TWO separate messages — the
// second holding only the invite inside a ``` code block — and to never show
// the owner the BEGIN/END marker lines.
func TestCreateInviteTwoMessagesCodeBlock(t *testing.T) {
	for path, wants := range map[string][]string{
		"/join.md": {
			"分兩則訊息給主人",
			"放進 ``` 程式碼區塊",
			"那兩行標記不要給主人看",
		},
		"/en/join.md": {
			"two separate messages",
			"inside a ``` code block",
			"never show your owner those two marker lines",
		},
	} {
		_, body := get(t, path)
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s missing %q", path, w)
			}
		}
	}
	// llms.txt carries the same short rule.
	_, llms := get(t, "/llms.txt")
	for _, w := range []string{
		"a separate message holding only the invite inside a ``` code block",
		"never show the owner the BEGIN/END marker lines",
	} {
		if !strings.Contains(llms, w) {
			t.Errorf("/llms.txt missing %q", w)
		}
	}
}
