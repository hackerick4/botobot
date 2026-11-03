package judge

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// OutboundResult is the DLP verdict for one outbound text. Hard blocks for
// this bridge's own keys and local secret values are done by the CLI (it holds
// the values); this scanner finds secret-shaped and personal data.
type OutboundResult struct {
	Level Level
	Hits  []Hit
}

// Outbound DLP categories.
const (
	SFmt      Category = "S-FMT"      // well-known credential formats
	SPII      Category = "S-PII"      // personal identifiers (ID, card, bank, phone, address)
	SEntropy  Category = "S-ENTROPY"  // long random-looking tokens / encoded blobs
	SPath     Category = "S-PATH"     // sensitive local paths
	SLeak     Category = "S-LEAK"     // prompt/rules/memory leak markers
	SExfilURL Category = "S-EXFILURL" // URLs carrying data in their query
	SContact  Category = "S-CONTACT"  // e-mail addresses
)

type orule struct {
	id    string
	cat   Category
	level Level
	re    *regexp.Regexp
	check func(string) bool // optional validator on the match
}

func or(id string, cat Category, lv Level, pattern string, check func(string) bool) orule {
	return orule{id: id, cat: cat, level: lv, re: regexp.MustCompile(pattern), check: check}
}

var outboundRules = []orule{
	// ---- high: credential formats ---------------------------------------
	or("FMT-AWS-AKID", SFmt, LevelHigh, `\b(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA)[A-Z0-9]{16}\b`, nil),
	or("FMT-AWS-SECRET", SFmt, LevelHigh, `(?i)aws.{0,20}(?:secret|key).{0,5}[:=]\s*["']?[A-Za-z0-9/+]{40}\b`, nil),
	or("FMT-GITHUB", SFmt, LevelHigh, `\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{60,255})\b`, nil),
	or("FMT-GITLAB", SFmt, LevelHigh, `\bgl(?:pat|dt|rt|ptt|ft|imt|cbt|soat|agent)-[A-Za-z0-9_-]{20,}\b`, nil),
	or("FMT-SLACK", SFmt, LevelHigh, `\bxox[abposr]-[A-Za-z0-9-]{10,}\b|hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]+`, nil),
	or("FMT-OPENAI", SFmt, LevelHigh, `\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}\b`, nil),
	or("FMT-ANTHROPIC", SFmt, LevelHigh, `\bsk-ant-[A-Za-z0-9_-]{20,}\b`, nil),
	or("FMT-XAI", SFmt, LevelHigh, `\bxai-[A-Za-z0-9]{20,}\b`, nil),
	or("FMT-GOOGLE", SFmt, LevelHigh, `\bAIza[0-9A-Za-z_-]{35}\b|\bya29\.[0-9A-Za-z_-]{20,}|\bGOCSPX-[0-9A-Za-z_-]{20,}`, nil),
	or("FMT-STRIPE", SFmt, LevelHigh, `\b(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{16,}\b|\bwhsec_[0-9A-Za-z]{20,}`, nil),
	or("FMT-SENDGRID", SFmt, LevelHigh, `\bSG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}\b`, nil),
	or("FMT-NPM", SFmt, LevelHigh, `\bnpm_[A-Za-z0-9]{36}\b|\bpypi-[A-Za-z0-9_-]{50,}`, nil),
	or("FMT-TELEGRAM", SFmt, LevelHigh, `\b\d{8,10}:AA[A-Za-z0-9_-]{33}\b`, nil),
	or("FMT-DISCORD", SFmt, LevelHigh, `discord(?:app)?\.com/api/webhooks/\d+/[A-Za-z0-9_-]{20,}|\b[MN][A-Za-z\d]{23,25}\.[\w-]{6}\.[\w-]{27,}\b`, nil),
	or("FMT-JWT", SFmt, LevelHigh, `\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`, nil),
	or("FMT-PEM", SFmt, LevelHigh, `-----BEGIN [A-Z ]*(?:PRIVATE KEY|OPENSSH PRIVATE KEY|PGP PRIVATE KEY BLOCK)-----`, nil),
	or("FMT-PASSWORD-ASSIGN", SFmt, LevelHigh, `(?i)\b(?:password|passwd|pwd|passphrase|secret|api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret)\b\s*(?:is|=|:|:=|=>)\s*["']?([^\s"'<>]{6,})`, passwordValueOK),
	or("FMT-PASSWORD-ZH", SFmt, LevelHigh, `(?:密碼|密鑰|金鑰|私鑰|口令|驗證碼|pin碼)\s*(?:是|為|:|=|：)\s*["'「]?([^\s"'」]{4,})`, passwordValueOK),
	or("FMT-CONNSTR", SFmt, LevelHigh, `(?i)\b(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqps?|mssql|sqlserver|ftp|sftp|smtp)://[^\s:/@]+:[^\s@/]+@`, nil),
	or("FMT-BASICAUTH-URL", SFmt, LevelHigh, `(?i)\bhttps?://[^\s:/@]+:[^\s@/]{3,}@[^\s]+`, nil),
	or("FMT-AZURE", SFmt, LevelHigh, `(?i)AccountKey=[A-Za-z0-9+/=]{40,}|SharedAccessSignature=|sig=[A-Za-z0-9%]{40,}`, nil),
	or("PII-TWID", SPII, LevelHigh, `\b[A-Z][12]\d{8}\b`, twIDValid),
	or("PII-CARD", SPII, LevelHigh, `\b(?:\d[ -]?){13,19}\b`, luhnCard),
	or("PII-IBAN", SPII, LevelHigh, `\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){3,7}(?: ?[A-Z0-9]{1,4})?\b`, ibanValid),
	or("PII-SSN", SPII, LevelHigh, `(?i)\b(?:ssn|social security(?: number)?)\D{0,10}\d{3}-\d{2}-\d{4}\b`, nil),
	or("PII-CVV", SPII, LevelHigh, `(?i)\b(?:cvv|cvc|cvv2|security code|安全碼|背面三碼)\D{0,6}\d{3,4}\b`, nil),
	or("PII-PASSPORT", SPII, LevelHigh, `(?i)(?:passport(?: no\.?| number)?|護照號碼)\s*[:：#]?\s*[A-Z0-9]{8,9}\b`, nil),

	// ---- medium: personal data / encoded blobs --------------------------
	or("PII-PHONE-TW", SPII, LevelMedium, `(?:\+?886[ -]?0?9|\b09)\d{2}[ -]?\d{3}[ -]?\d{3}\b`, nil),
	or("PII-PHONE-CN", SPII, LevelMedium, `(?:\+?86[ -]?)?\b1[3-9]\d[ -]?\d{4}[ -]?\d{4}\b`, nil),
	or("PII-PHONE-INTL", SPII, LevelMedium, `\+\d{1,3}[ .-]?\(?\d{1,4}\)?[ .-]?\d{3,4}[ .-]?\d{3,4}\b`, nil),
	or("PII-PHONE-US", SPII, LevelMedium, `\(\d{3}\)\s?\d{3}-\d{4}\b|\b\d{3}-\d{3}-\d{4}\b`, nil),
	or("PII-ADDR-ZH", SPII, LevelMedium, `(?:[市縣區鄉鎮]).{0,12}?(?:路|街|大道)(?:[一二三四五六七八九十]段)?.{0,8}?\d+(?:之\d+)?號`, nil),
	or("PII-ADDR-CN", SPII, LevelMedium, `(?:省|市|區|县|縣).{0,12}?(?:路|街|道|弄|巷)\d+號`, nil),
	or("PII-ADDR-EN", SPII, LevelMedium, `(?i)\b\d{1,6}\s+(?:[A-Za-z]+\s){1,4}(?:street|st|avenue|ave|road|rd|boulevard|blvd|lane|ln|drive|dr|court|ct|way|place|pl|terrace)\b\.?(?:,?\s*(?:apt|suite|unit|#)\s*\w+)?`, nil),
	or("PII-BIRTH", SPII, LevelMedium, `(?i)(?:生日|出生(?:日期|年月日)?|date of birth|\bdob\b|born on|birthday)\s*(?:是|為|:|：|is)?\s*(?:\d{2,4}[-/.年]\d{1,2}[-/.月]\d{1,2}|\d{1,2}[-/]\d{1,2}[-/]\d{2,4}|(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.? \d{1,2},? \d{4})`, nil),
	or("PII-BANK", SPII, LevelMedium, `(?i)(?:帳號|帳戶|戶頭|銀行|account(?: no\.?| number)?|routing(?: number)?|acct)\D{0,12}\d{3,4}[ -]?\d{3,}[ -]?\d{3,}`, nil),
	or("PII-NATID-ZH", SPII, LevelMedium, `(?:身分證|身份證|統一證號|居留證)(?:字號|號碼)?\s*(?:是|為|:|：)?\s*[A-Z][A-D0-9]\d{8}`, nil),
	or("PII-CNID", SPII, LevelMedium, `\b\d{6}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`, nil),
	or("ENT-B64", SEntropy, LevelMedium, `[A-Za-z0-9+/]{120,}={0,2}|[A-Za-z0-9_-]{160,}`, nil),
	or("ENT-HEX", SEntropy, LevelMedium, `\b[0-9a-fA-F]{128,}\b`, nil),
	or("ENT-DATAURL", SEntropy, LevelMedium, `(?i)data:[a-z]+/[a-z0-9.+-]+;base64,[A-Za-z0-9+/]{40,}`, nil),
	or("ENT-TOKEN", SEntropy, LevelMedium, `[A-Za-z0-9_+/=-]{32,100}`, highEntropy),
	or("PATH-SENSITIVE", SPath, LevelMedium, `(?i)(?:~|/home/[^/\s]+|/root|/users/[^/\s]+)/(?:\.ssh|\.aws|\.gnupg|\.config/(?:gcloud|bot-bridge)|\.bot-bridge|\.kube|\.docker|\.netrc|\.git-credentials|\.npmrc|\.pypirc|\.env)\b|\bid_(?:rsa|ed25519|ecdsa)\b|/etc/shadow|keys\.json|tick\.json`, nil),
	or("LEAK-RULES", SLeak, LevelMedium, `(?i)<<<\s*(?:untrusted|end)|bb_end_|===\s*bot-bridge|notify_owner:|=== 規則|you are (?:grok|an ai assistant)[^\n]{0,40}(?:system|instructions)|(?:my|the) system prompt (?:is|says)|我的系統提示|系統提示(?:詞)?(?:是|如下)`, nil),
	or("EXF-MDIMG", SExfilURL, LevelMedium, `!\[[^\]]*\]\(\s*https?://[^)\s]*[?&][^)\s]*=[^)\s]{8,}`, nil),
	or("EXF-SINK", SExfilURL, LevelMedium, `(?i)webhook\.site|requestbin|pipedream\.net|ngrok(?:-free)?\.(?:io|app)|transfer\.sh|0x0\.st|interactsh|burpcollaborator|oast\.(?:pro|live|site|online|fun|me)|canarytokens`, nil),
	or("EXF-LONGQUERY", SExfilURL, LevelMedium, `https?://[^\s?]+\?[^\s]*=[A-Za-z0-9+/_%-]{48,}`, nil),

	// ---- low ------------------------------------------------------------
	or("CON-EMAIL", SContact, LevelLow, `(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`, nil),
}

// HasCredential reports whether the scan found a credential format (S-FMT:
// API keys, tokens, private keys, passwords, connection strings). The CLI
// treats these as a hard block that no one can release.
func (r OutboundResult) HasCredential() bool {
	for _, h := range r.Hits {
		if h.Category == SFmt {
			return true
		}
	}
	return false
}

var emailRe = regexp.MustCompile(`(?i)\b[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}\b`)

// Outbound scans one outbound text (message body or text attachment).
func Outbound(text string) OutboundResult {
	// Match on width-folded text (keeps case, which credential formats need).
	f := foldWidth(text)
	var res OutboundResult
	seen := map[string]bool{}
	for _, rl := range outboundRules {
		ok := false
		for _, m := range rl.re.FindAllStringSubmatch(f, 50) {
			s := m[0]
			if len(m) > 1 && m[1] != "" {
				s = m[1]
			}
			if rl.check == nil || rl.check(s) {
				ok = true
				break
			}
		}
		if !ok || seen[rl.id] {
			continue
		}
		seen[rl.id] = true
		res.Hits = append(res.Hits, Hit{Rule: rl.id, Category: rl.cat, Weight: int(rl.level)})
		if rl.level > res.Level {
			res.Level = rl.level
		}
	}
	if n := len(uniq(emailRe.FindAllString(strings.ToLower(f), -1))); n >= 3 {
		res.Hits = append(res.Hits, Hit{Rule: "CON-EMAIL-BULK", Category: SContact, Weight: int(LevelMedium)})
		if res.Level < LevelMedium {
			res.Level = LevelMedium
		}
	}
	sort.Slice(res.Hits, func(i, j int) bool { return res.Hits[i].Rule < res.Hits[j].Rule })
	return res
}

// foldWidth maps full-width ASCII to ASCII and drops invisible characters,
// keeping case.
func foldWidth(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case isInvisible(r):
			return -1
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == 0x3000:
			return ' '
		}
		return r
	}, s)
}

func uniq(xs []string) []string {
	m := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !m[x] {
			m[x] = true
			out = append(out, x)
		}
	}
	return out
}

// passwordValueOK rejects placeholder-ish values ("password: ********",
// "password = <your password>", "密碼是什麼").
func passwordValueOK(v string) bool {
	lv := strings.ToLower(strings.Trim(v, `"'.,;:)!?。，`))
	if len(lv) < 4 {
		return false
	}
	if strings.Trim(lv, "*x•.-_#?") == "" {
		return false
	}
	for _, p := range []string{"什麼", "多少", "嗎", "忘", "不", "要", "請", "設定", "重設", "記得", "已", "錯", "對"} {
		if strings.HasPrefix(lv, p) {
			return false
		}
	}
	for _, p := range []string{"your", "<", "{", "$", "redacted", "hidden", "required", "optional", "changeme", "example", "placeholder", "null", "none", "empty", "same", "the ", "wrong", "incorrect", "reset", "forgot", "expired", "correct", "updated", "set", "protected", "strong", "weak", "needed", "invalid", "tbd", "todo", "unknown", "secret", "password", "string", "true", "false", "env", "process.", "os."} {
		if strings.HasPrefix(lv, p) {
			return false
		}
	}
	return true
}

func twIDValid(s string) bool {
	if len(s) != 10 {
		return false
	}
	const letters = "ABCDEFGHJKLMNPQRSTUVXYWZIO"
	idx := strings.IndexByte(letters, s[0])
	if idx < 0 {
		return false
	}
	n := idx + 10
	sum := n/10 + (n%10)*9
	w := []int{8, 7, 6, 5, 4, 3, 2, 1}
	for i := 1; i <= 8; i++ {
		sum += int(s[i]-'0') * w[i-1]
	}
	sum += int(s[9] - '0')
	return sum%10 == 0
}

func luhnCard(s string) bool {
	var ds []int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			ds = append(ds, int(c-'0'))
		}
	}
	if len(ds) < 13 || len(ds) > 19 {
		return false
	}
	// Require a real issuer prefix to avoid order numbers and timestamps.
	p := ds[0]
	if p != 3 && p != 4 && p != 5 && p != 6 && !(p == 2 && ds[1] >= 2) {
		return false
	}
	allSame := true
	for _, d := range ds {
		if d != ds[0] {
			allSame = false
		}
	}
	if allSame {
		return false
	}
	sum := 0
	for i := len(ds) - 1; i >= 0; i-- {
		d := ds[i]
		if (len(ds)-1-i)%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

func ibanValid(s string) bool {
	s = strings.ReplaceAll(s, " ", "")
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	r := s[4:] + s[:4]
	mod := 0
	for _, c := range r {
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
			mod = (mod*10 + v) % 97
		case c >= 'A' && c <= 'Z':
			v = int(c-'A') + 10
			mod = (mod*100 + v) % 97
		default:
			return false
		}
	}
	return mod == 1
}

// highEntropy flags random-looking tokens: mixed classes and high Shannon
// entropy, excluding words, paths, hashes in hex (handled elsewhere at 128+)
// and UUIDs.
func highEntropy(s string) bool {
	s = strings.Trim(s, "=-_/")
	if len(s) < 32 {
		return false
	}
	var lower, upper, digit int
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
			lower++
		case c >= 'A' && c <= 'Z':
			upper++
		case c >= '0' && c <= '9':
			digit++
		}
	}
	if lower == 0 || upper == 0 || digit == 0 {
		return false // hex hashes, lower-case slugs, plain words
	}
	if strings.Count(s, "/") >= 2 && strings.Count(s, "/")*12 > len(s)/2 {
		return false // a path
	}
	return shannon(s) >= 4.3
}

func shannon(s string) float64 {
	f := map[rune]float64{}
	for _, c := range s {
		f[c]++
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range f {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

var outCatZH = map[Category]string{
	SFmt:      "疑似密碼、金鑰或登入資訊",
	SPII:      "疑似個人資料（證號、卡號、帳號、電話、地址或生日）",
	SEntropy:  "一長串像金鑰或編碼過的內容",
	SPath:     "提到本機的敏感檔案位置",
	SLeak:     "像是在貼出助理的規則或系統設定",
	SExfilURL: "含可疑網址",
	SContact:  "含多個 email",
}

// DescribeOutZH renders outbound hits as a short plain zh-TW phrase list.
func DescribeOutZH(r OutboundResult, min Level) []string {
	seen := map[Category]bool{}
	var out []string
	for _, h := range r.Hits {
		if Level(h.Weight) < min || seen[h.Category] {
			continue
		}
		seen[h.Category] = true
		if d, ok := outCatZH[h.Category]; ok {
			out = append(out, d)
		}
	}
	return out
}
