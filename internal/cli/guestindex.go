package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// guestIndex is entries/<id>/guests.json (v0.6.0): for each guest line, the
// guest's names and hashes of contact-like tokens seen in what that guest sent.
// It exists only so a reply on one guest's line that carries another guest's
// details is held for the owner (cross-guest leak guard). Raw tokens are never
// stored: only salted hashes, kept guestTokTTL.
type guestIndex struct {
	Salt    string               `json:"salt"`
	Bridges map[string]*guestRec `json:"bridges"`
}

type guestRec struct {
	Names []string         `json:"names,omitempty"`
	Toks  map[string]int64 `json:"toks,omitempty"`
}

const guestTokTTL = int64(7 * 24 * 60 * 60 * 1000)

func (ks *keystore) guestIndexPath(entryID string) string {
	return filepath.Join(ks.entryDir(entryID), "guests.json")
}

func (ks *keystore) loadGuestIndex(entryID string) *guestIndex {
	gi := &guestIndex{}
	_ = readJSON(ks.guestIndexPath(entryID), gi)
	if gi.Bridges == nil {
		gi.Bridges = map[string]*guestRec{}
	}
	if gi.Salt == "" {
		gi.Salt = randToken()
	}
	return gi
}

func (ks *keystore) saveGuestIndex(entryID string, gi *guestIndex) {
	_ = writeJSONAtomic(ks.guestIndexPath(entryID), gi)
}

func (gi *guestIndex) rec(bid string) *guestRec {
	r := gi.Bridges[bid]
	if r == nil {
		r = &guestRec{}
		gi.Bridges[bid] = r
	}
	return r
}

func (gi *guestIndex) addNames(bid string, names ...string) {
	r := gi.rec(bid)
	for _, n := range names {
		n = strings.TrimSpace(n)
		if !leakNameOK(n) {
			continue
		}
		dup := false
		for _, x := range r.Names {
			if strings.EqualFold(x, n) {
				dup = true
			}
		}
		if !dup {
			r.Names = append(r.Names, n)
		}
	}
}

func (gi *guestIndex) addText(bid, text string, nowMS int64) {
	toks := contactTokens(text)
	if len(toks) == 0 {
		return
	}
	r := gi.rec(bid)
	if r.Toks == nil {
		r.Toks = map[string]int64{}
	}
	for t := range r.Toks {
		if nowMS-r.Toks[t] > guestTokTTL {
			delete(r.Toks, t)
		}
	}
	for _, t := range toks {
		r.Toks[gi.hash(t)] = nowMS
	}
}

func (gi *guestIndex) hash(tok string) string {
	h := sha256.Sum256([]byte(gi.Salt + "\x00" + tok))
	return hex.EncodeToString(h[:12])
}

// leaks reports whether text, about to go to guest bid, names another guest
// or repeats a contact token another guest sent.
func (gi *guestIndex) leaks(bid, text string, nowMS int64) bool {
	own := gi.Bridges[bid]
	isOwn := func(n string) bool {
		if own == nil {
			return false
		}
		for _, x := range own.Names {
			if strings.EqualFold(x, n) {
				return true
			}
		}
		return false
	}
	var hashes []string
	for _, t := range contactTokens(text) {
		hashes = append(hashes, gi.hash(t))
	}
	low := strings.ToLower(foldWidth(text))
	for other, r := range gi.Bridges {
		if other == bid {
			continue
		}
		for _, n := range r.Names {
			if !isOwn(n) && nameIn(low, strings.ToLower(foldWidth(n))) {
				return true
			}
		}
		for _, h := range hashes {
			if at, ok := r.Toks[h]; ok && nowMS-at <= guestTokTTL {
				if own == nil || own.Toks[h] == 0 {
					return true
				}
			}
		}
	}
	return false
}

// leakNameOK keeps names specific enough to match on: three or more Latin
// letters, or two or more CJK characters.
func leakNameOK(n string) bool {
	letters, cjk := 0, 0
	for _, r := range n {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r):
			cjk++
		case unicode.IsLetter(r):
			letters++
		}
	}
	return letters >= 3 || cjk >= 2
}

// nameIn matches n in s as a whole word (Latin) or as a substring (CJK).
func nameIn(s, n string) bool {
	if n == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(s[i:], n)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(n)
		before, after := rune(' '), rune(' ')
		if start > 0 {
			before = lastRune(s[:start])
		}
		if end < len(s) {
			after = firstRune(s[end:])
		}
		if isCJK(firstRune(n)) || (!latinWord(before) && !latinWord(after)) {
			return true
		}
		i = start + 1
		if i >= len(s) {
			return false
		}
	}
}

// latinWord is a rune that continues a non-CJK word.
func latinWord(r rune) bool {
	return r == '_' || unicode.IsDigit(r) || (unicode.IsLetter(r) && !isCJK(r))
}
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}
func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return ' '
}
func lastRune(s string) rune {
	rs := []rune(s)
	if len(rs) == 0 {
		return ' '
	}
	return rs[len(rs)-1]
}

var (
	emailTokRe = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	digitRunRe = regexp.MustCompile(`\+?\d[\d\s().\-]{4,}\d`)
)

// contactTokens returns the e-mail addresses and digit runs of six or more
// digits (phones, booking or card numbers) in text, normalised.
func contactTokens(text string) []string {
	text = foldWidth(text)
	var out []string
	for _, m := range emailTokRe.FindAllString(text, -1) {
		out = append(out, "e:"+strings.ToLower(m))
	}
	for _, m := range digitRunRe.FindAllString(text, -1) {
		var b strings.Builder
		for _, r := range m {
			if r >= '0' && r <= '9' {
				b.WriteRune(r)
			}
		}
		d := b.String()
		if len(d) >= 6 {
			out = append(out, "d:"+d)
		}
		if len(d) >= 8 {
			// The same phone with or without a country or trunk prefix.
			out = append(out, "s:"+d[len(d)-8:])
		}
	}
	return out
}

// foldWidth maps full-width ASCII (Ｂｅｎ, ０９１２, ＠) and the ideographic space
// to plain ASCII, and Arabic-Indic digits to 0-9, so the leak guard cannot be
// sidestepped by retyping a phone or name in another width (RT-111, v0.6.0).
func foldWidth(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == 0x3000:
			return ' '
		case r >= 0x0660 && r <= 0x0669:
			return '0' + (r - 0x0660)
		case r >= 0x06F0 && r <= 0x06F9:
			return '0' + (r - 0x06F0)
		}
		return r
	}, s)
}
