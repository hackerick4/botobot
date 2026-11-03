package cli

import "testing"

// TestParseInviteLink checks server/bridge extraction from invite links
// (DEV_SPEC §6.4, §12.3).
func TestParseInviteLink(t *testing.T) {
	ok := []struct {
		link       string
		wantServer string
		wantBridge string
	}{
		{"https://bridge.example/b/br_abcdefghijklmnopqrstuv", "https://bridge.example", "br_abcdefghijklmnopqrstuv"},
		{"http://localhost:8080/b/br_x", "http://localhost:8080", "br_x"},
		{"https://host/prefix/b/br_y", "https://host/prefix", "br_y"},
		{"https://host/b/br_z/", "https://host", "br_z"},
	}
	for _, c := range ok {
		s, b, err := parseInviteLink(c.link)
		if err != nil {
			t.Errorf("parseInviteLink(%q) error: %v", c.link, err)
			continue
		}
		if s != c.wantServer || b != c.wantBridge {
			t.Errorf("parseInviteLink(%q) = (%q,%q), want (%q,%q)", c.link, s, b, c.wantServer, c.wantBridge)
		}
	}

	bad := []string{
		"ftp://host/b/br_x",
		"https://host/nope",
		"https:///b/br_x",
		"https://host/b/",
		"https://host/b/not-a-bridge",
	}
	for _, link := range bad {
		if _, _, err := parseInviteLink(link); err == nil {
			t.Errorf("parseInviteLink(%q) should have failed", link)
		}
	}
}
