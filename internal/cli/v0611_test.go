package cli

import (
	"strings"
	"testing"
)

// v0.6.11 (Rick t326u/t327u): close the tickRules gap so a routine tick carries
// the same direct-send whitelist as join.md §「什麼時候傳給對方」line 192 — a
// question the owner asked to put to the other side goes out without a card.
func TestV0611TickRulesOwnerQuestionDirectSend(t *testing.T) {
	want := "主人在私人對話裡問了要對方回答的事（例如「問 B 週四下午可以嗎」）"
	if !strings.Contains(tickRules, want) {
		t.Errorf("tickRules rule 4 missing the owner-question direct-send example %q", want)
	}
	// It belongs to the direct-send list (rule 4), never the ask-first list (rule 5).
	r4 := tickRules[strings.Index(tickRules, "4. 像助理一樣處理"):strings.Index(tickRules, "5. 下列一律先私下問主人")]
	if !strings.Contains(r4, want) {
		t.Errorf("owner-question example not inside rule 4 (direct-send):\n%s", r4)
	}
}

// v0.6.11: ownerCaution no longer reads as "confirm every outbound". It states
// plainly that ordinary messages go directly and only sensitive actions are
// confirmed — the over-read that drove the mini-mira Send-every-message bug.
func TestV0611OwnerCautionScoped(t *testing.T) {
	if !strings.Contains(ownerCautionZH, "一般訊息可直接收發") {
		t.Errorf("ownerCautionZH should say ordinary messages are sent directly: %q", ownerCautionZH)
	}
	if strings.Contains(ownerCautionZH, "一律先確認") {
		t.Errorf("ownerCautionZH still has the any-outbound phrasing: %q", ownerCautionZH)
	}
	if !strings.Contains(ownerCautionEN, "ordinary messages are sent and received directly") {
		t.Errorf("ownerCautionEN should say ordinary messages are sent directly: %q", ownerCautionEN)
	}
	// Sensitive categories must stay in the note.
	for _, w := range []string{"寄信", "付款", "個資"} {
		if !strings.Contains(ownerCautionZH, w) {
			t.Errorf("ownerCautionZH dropped the sensitive category %q: %q", w, ownerCautionZH)
		}
	}
}
