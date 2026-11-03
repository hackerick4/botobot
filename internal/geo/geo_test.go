package geo

import "testing"

func TestIsTW(t *testing.T) {
	if Ranges() < 1000 {
		t.Fatalf("only %d TW ranges loaded", Ranges())
	}
	for ip, want := range map[string]bool{
		"1.32.199.1":           true,  // first TW range in the file
		"168.95.1.1":           true,  // HiNet DNS
		"::ffff:168.95.1.1":    true,  // IPv4-mapped
		"8.8.8.8":              false, // US
		"1.1.1.1":              false,
		"127.0.0.1":            false,
		"2001:4860:4860::8888": false,
		"not an ip":            false,
		"":                     false,
	} {
		if got := IsTW(ip); got != want {
			t.Errorf("IsTW(%q) = %v, want %v", ip, got, want)
		}
	}
}
