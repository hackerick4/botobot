package site_test

import (
	"strings"
	"testing"
)

// The first real warm creator run spent an extra tool turn on
// `update && verify-self` before `claim`, driven by section 1 of the manuals.
// On a warm (already-installed) bot that prep is unnecessary (tick auto-updates;
// verify-self is an install/upgrade check), so the manuals must send a warm bot
// straight to claim/join in one command and must not ask for a read-back. These
// assertions lock that in.
func TestWarmPathIsOneCall(t *testing.T) {
	_, en := get(t, "/en/join.md")
	_, zh := get(t, "/join.md")
	_, llms := get(t, "/llms.txt")

	// 1. Warm bots skip update/verify-self before claim/join.
	for _, w := range []string{
		"do not run `update` or `verify-self` first",
		"already installed: you are ready",
	} {
		if !strings.Contains(en, w) {
			t.Errorf("en/join.md missing warm-skip phrase %q", w)
		}
	}
	if !strings.Contains(zh, "不用先跑 `update` 或 `verify-self`") {
		t.Errorf("join.md missing zh warm-skip phrase")
	}
	if !strings.Contains(llms, "do not run `update` or `verify-self` first") {
		t.Errorf("llms.txt missing warm-skip phrase")
	}

	// 2. No read-back: the single claim output is relayed as-is.
	if !strings.Contains(en, "run nothing else") {
		t.Errorf("en/join.md missing the no-read-back instruction")
	}
	if !strings.Contains(zh, "不要再跑第二個指令") {
		t.Errorf("join.md missing the zh no-read-back instruction")
	}

	// 3. Regression guard: the old blanket trigger that made warm bots prep must
	//    not come back.
	if strings.Contains(en, "Run before every create or join") {
		t.Errorf("en/join.md still tells every bot to run a step before create/join")
	}
	if strings.Contains(zh, "每次建立或加入前執行一次") {
		t.Errorf("join.md still tells every bot to run a step before create/join")
	}

	// 4. A brand-new (cold) bot must still install first; verify-self still
	//    documented (install/upgrade). (v063 test also requires verify-self.)
	if !strings.Contains(en, "brand-new bot") || !strings.Contains(en, "verify-self") {
		t.Errorf("en/join.md lost the cold-install / verify-self guidance")
	}
}
