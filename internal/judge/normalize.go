// Package judge is bot-bridge's deterministic, client-side message judge
// (docs/EXFIL_DEFENSE.md §4): an inbound classifier for elicitation and
// injection attempts and an outbound DLP scanner. It uses fixed rules only, with
// no network, no model and no API key. It runs only when there is a message to
// judge, so empty wakes cost nothing. It is defense in depth. It never replaces
// the owner's confirmation and it can miss things.
package judge

import (
	"regexp"
	"strings"
	"unicode"
)

// variants is the set of normalized views one text is matched in. A rule that
// only matches an obfuscation-undoing view (despaced / leet) also adds an
// obfuscation signal.
type variants struct {
	folded   string // fold(): width, case, confusables, simplified->traditional, invisible chars removed
	despaced string // folded with single-letter spacing and intra-Han separators collapsed
	leet     string // despaced with leetspeak digits/symbols inside words mapped to letters
	obf      int    // obfuscation evidence count observed while folding
}

// confusables maps common Cyrillic/Greek/other look-alikes to Latin.
var confusables = map[rune]rune{
	'а': 'a', 'в': 'b', 'е': 'e', 'ё': 'e', 'к': 'k', 'м': 'm', 'н': 'h', 'о': 'o', 'р': 'p', 'с': 'c', 'т': 't', 'у': 'y', 'х': 'x',
	'і': 'i', 'ї': 'i', 'ј': 'j', 'ѕ': 's', 'ԁ': 'd', 'ԛ': 'q', 'ԝ': 'w', 'ɡ': 'g', 'ɑ': 'a', 'ı': 'i', 'ℓ': 'l',
	'А': 'a', 'В': 'b', 'Е': 'e', 'К': 'k', 'М': 'm', 'Н': 'h', 'О': 'o', 'Р': 'p', 'С': 'c', 'Т': 't', 'Х': 'x', 'У': 'y', 'І': 'i', 'Ѕ': 's', 'Ј': 'j',
	'α': 'a', 'β': 'b', 'ε': 'e', 'ι': 'i', 'κ': 'k', 'ν': 'v', 'ο': 'o', 'ρ': 'p', 'τ': 't', 'υ': 'u', 'χ': 'x', 'ω': 'w',
	'Α': 'a', 'Β': 'b', 'Ε': 'e', 'Ζ': 'z', 'Η': 'h', 'Ι': 'i', 'Κ': 'k', 'Μ': 'm', 'Ν': 'n', 'Ο': 'o', 'Ρ': 'p', 'Τ': 't', 'Υ': 'y', 'Χ': 'x',
	'ⅰ': 'i', 'ⅼ': 'l', 'ⅽ': 'c', 'ⅾ': 'd', 'ⅿ': 'm', '０': '0',
	'‘': '\'', '’': '\'', '‛': '\'', '“': '"', '”': '"', '＂': '"', '‐': '-', '‑': '-', '‒': '-', '–': '-', '—': '-', '―': '-', '−': '-',
	'：': ':', '，': ',', '。': '.', '；': ';', '！': '!', '？': '?', '（': '(', '）': ')', '　': ' ',
}

// s2t maps simplified Chinese characters that occur in the rule vocabulary to
// their traditional form, so rules are written once (in zh-TW).
var s2t = func() map[rune]rune {
	const pairs = "码碼钥鑰资資讯訊发發执執规規则則统統账帳号號电電话話证證银銀传傳输輸显顯内內环環变變数數档檔读讀运運网網链鏈编編权權员員紧緊马馬测測试試开開词詞设設个個隐隱护護户戶转轉汇匯余餘额額几幾过過这這么麼们們请請机機务務给給复覆制製贴貼诉訴说說讲講经經许許应應实實际際问問让讓别別瞒瞞忆憶记記视視从從现現绕繞检檢认認确確协協议議团團队隊层層级級凭憑据據验驗动動态態备備会會领領册冊书書钱錢币幣录錄历歷计計划劃驾駕处處里裡邮郵联聯络絡对對换換头頭后後删刪锁鎖终終结結围圍栏欄状狀仅僅帮幫烦煩够夠托託贝貝载載进進庄莊东東台臺湾灣国國区區县縣乡鄉镇鎮询詢报報价價单單时時间間约約见見泄洩条條样樣吗嗎为為与與着著还還没沒长長辈輩亲親属屬儿兒妈媽图圖体體择擇选選项項赖賴签簽众眾写寫扰擾剧劇"
	m := map[rune]rune{}
	rs := []rune(pairs)
	for i := 0; i+1 < len(rs); i += 2 {
		m[rs[i]] = rs[i+1]
	}
	return m
}()

// isInvisible reports format/zero-width/tag/bidi runes used to hide text.
func isInvisible(r rune) bool {
	switch {
	case r == 0x200B || r == 0x200C || r == 0x200D || r == 0x2060 || r == 0xFEFF || r == 0x00AD || r == 0x180E:
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	case r >= 0xE0000 && r <= 0xE007F:
		return true
	case r >= 0xFE00 && r <= 0xFE0F:
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// fold produces the canonical matching form and counts obfuscation evidence.
func fold(s string) (string, int) {
	var b strings.Builder
	b.Grow(len(s))
	obf := 0
	fullwidthRun := 0
	for _, r := range s {
		switch {
		case isInvisible(r):
			obf++
			continue
		case r >= 0xFF01 && r <= 0xFF5E: // full-width ASCII
			fullwidthRun++
			r -= 0xFEE0
		case r == 0x3000:
			r = ' '
		default:
			if fullwidthRun >= 4 {
				obf++
			}
			fullwidthRun = 0
		}
		if c, ok := confusables[r]; ok {
			if unicode.In(r, unicode.Cyrillic, unicode.Greek) {
				obf++
			}
			r = c
		}
		if t, ok := s2t[r]; ok {
			r = t
		}
		if r == 0x2028 || r == 0x2029 || r == 0x85 {
			r = '\n'
		}
		b.WriteRune(unicode.ToLower(r))
	}
	if fullwidthRun >= 4 {
		obf++
	}
	return b.String(), obf
}

var (
	// "p a s s w o r d", "p.a.s.s.w.o.r.d", "p-a-s-s" (>= 4 single chars).
	spacedLettersRe = regexp.MustCompile(`(?:^|[^a-z0-9])((?:[a-z0-9][ .\-_*·/|]{1,2}){2,}[a-z0-9])(?:$|[^a-z0-9])`)
	spacedSepRe     = regexp.MustCompile(`[ .\-_*·/|]`)
	hanSepRe        = regexp.MustCompile(`(\p{Han})[ \t\-_*·・/|]+(\p{Han})`)
	leetWordRe      = regexp.MustCompile(`[a-z0-9@$!|+]*[a-z][a-z0-9@$!|+]*`)
)

var leetMap = map[rune]rune{'0': 'o', '1': 'i', '3': 'e', '4': 'a', '5': 's', '7': 't', '8': 'b', '@': 'a', '$': 's', '!': 'i', '|': 'l', '+': 't'}

func despace(s string) string {
	out := spacedLettersRe.ReplaceAllStringFunc(s, func(m string) string {
		// keep the surrounding boundary characters intact
		sub := spacedLettersRe.FindStringSubmatchIndex(m)
		if sub == nil {
			return m
		}
		inner := m[sub[2]:sub[3]]
		return m[:sub[2]] + spacedSepRe.ReplaceAllString(inner, "") + m[sub[3]:]
	})
	for i := 0; i < 4; i++ { // overlapping Han pairs need a few passes
		n := hanSepRe.ReplaceAllString(out, "$1$2")
		if n == out {
			break
		}
		out = n
	}
	return out
}

func unleet(s string) string {
	return leetWordRe.ReplaceAllStringFunc(s, func(w string) string {
		hasDigit := strings.ContainsAny(w, "0134578@$!|+")
		if !hasDigit {
			return w
		}
		letters := 0
		for _, r := range w {
			if r >= 'a' && r <= 'z' {
				letters++
			}
		}
		if letters < 1 || len(w) < 3 {
			return w
		}
		return strings.Map(func(r rune) rune {
			if m, ok := leetMap[r]; ok {
				return m
			}
			return r
		}, w)
	})
}

func makeVariants(s string) variants {
	f, obf := fold(s)
	d := despace(f)
	return variants{folded: f, despaced: d, leet: unleet(d), obf: obf}
}
