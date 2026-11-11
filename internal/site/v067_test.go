package site_test

import (
	"strings"
	"testing"
)

// The v0.6.7 "when to send" checks were replaced by v068_test.go (v0.6.8
// reworked the section); the font-credit check below still applies.

// v0.6.7: the font credits match the fonts that actually ship.
func TestV067OFLMatchesShippedFonts(t *testing.T) {
	_, body := get(t, "/OFL.txt")
	for _, w := range []string{
		"Fraunces — Copyright 2018 The Fraunces Project Authors",
		"Instrument Sans — Copyright 2022 The Instrument Sans Project Authors",
		"Noto Serif TC — (c) 2017-2024 Adobe",
		"Noto Sans TC — Copyright 2014-2021 Adobe (http://www.adobe.com/), with\n  Reserved Font Name 'Source'",
		"SIL OPEN FONT LICENSE Version 1.1",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("OFL.txt missing %q", w)
		}
	}
	for _, bad := range []string{"Newsreader", "Copyright (c) 2019 The Fraunces", "Source Han Serif"} {
		if strings.Contains(body, bad) {
			t.Errorf("OFL.txt still has %q", bad)
		}
	}
	for _, f := range []string{"fraunces", "instrument-sans", "instrument-sans-sb", "noto-serif-tc", "noto-sans-tc"} {
		if res, _ := get(t, "/fonts/"+f+".woff2"); res.StatusCode != 200 {
			t.Errorf("font %s not served (%d)", f, res.StatusCode)
		}
	}
}
