package pinyindata

import "testing"

func TestLookup(t *testing.T) {
	for r, want := range map[rune]string{'測': "ce", '試': "shi", '王': "wang", '女': "nv", '中': "zhong", 'a': "", '。': ""} {
		if got := Lookup(r); got != want {
			t.Errorf("Lookup(%q) = %q, want %q", r, got, want)
		}
	}
}

func TestLicenseShipped(t *testing.T) {
	if len(License) < 100 {
		t.Fatal("pypinyin license not embedded")
	}
}
