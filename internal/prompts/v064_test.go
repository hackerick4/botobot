package prompts

import (
	"strings"
	"testing"
)

// v0.6.4 finding 4: each check in the install line says in plain words when
// it stops the install, and the line still stops there.
func TestV064InstallBlockNamesFailedStep(t *testing.T) {
	ib := InstallBlock("https://botobot.app/dl")
	for _, w := range []string{InstallStopDownload, InstallStopSignature, InstallStopSHA, InstallStopVerify} {
		if !strings.Contains(ib, "|| { echo '"+w+"' >&2; false; }; }") {
			t.Errorf("install line lacks %q", w)
		}
		if strings.ContainsAny(w, "'\"$`\\") {
			t.Errorf("message %q must not need quoting", w)
		}
	}
	if strings.Contains(ib, " | sh ") || strings.Contains(ib, " | bash") || strings.Count(ib, "--proto =https") != 3 {
		t.Errorf("install line shape changed:\n%s", ib)
	}
}
