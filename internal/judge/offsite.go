package judge

import (
	"regexp"
	"strings"
	"unicode"
)

// OfficialHost is the only place bot-bridge is downloaded, installed or
// updated from (v0.6.4).
const OfficialHost = "botobot.app"

// hostRe finds a host in a URL or a bare domain. Letters include non-Latin
// ones so a look-alike host (Cyrillic "о") is caught as it is written.
var hostRe = regexp.MustCompile(`(?i)(?:\b[a-z][a-z0-9+.-]*://)?(?:[^\s/@:]+(?::[^\s/@]*)?@)?((?:[\p{L}\p{N}](?:[\p{L}\p{N}_-]*[\p{L}\p{N}])?\.)+(?:xn--[a-z0-9-]+|\p{L}[\p{L}\p{N}-]*\p{L}))(?::\d+)?(/[^\s]*)?`)

// bareTLDs are the endings a host written without a scheme must have to
// count (so "file.txt" or "v0.6.4" never does).
var bareTLDs = map[string]bool{}

// cjkTLDs are the real IDN top-level domains written in CJK script. A bare host
// (no scheme) that ends in one of these is treated as a domain; a bare host that
// ends in any other CJK run is treated as ideographic-period-separated prose and
// skipped (so "收到。有更新再跟你說" is not read as a host).
var cjkTLDs = map[string]bool{
	"中国": true, "中國": true, "台灣": true, "台湾": true,
	"香港": true, "澳门": true, "澳門": true,
	"公司": true, "网络": true, "網絡": true,
}

// hasCJK reports whether s contains any Han, Hiragana, Katakana or Hangul rune.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

func init() {
	for _, t := range strings.Fields(`com net org app io dev xyz sh co ai me info biz top site online link cc tw cn ru so us uk de jp kr hk
		store shop click live pro run page cloud tech tk ml ga cf gq zip mov tv ws gg ly to la vip win bid icu cyou buzz fun space website
		download update install bot tools software github gitlab`) {
		bareTLDs[t] = true
	}
}

// downloadWord asks for a download, install or update (zh, simplified zh,
// en), or a shell pipe that installs.
var downloadWord = regexp.MustCompile(`(?i)下載|下载|安裝|安装|更新|升級|升级|重裝|重装|download|install|reinstall|update|upgrade|curl\s|wget\s|\|\s*(?:ba|z)?sh\b|iwr\s|invoke-webrequest|brew\s|pip3?\s|npm\s|go\s+install`)

// programPath is a link straight to a program or script.
var programPath = regexp.MustCompile(`(?i)\.(?:sh|bash|ps1|exe|msi|dmg|pkg|apk|deb|rpm|appimage|bat|cmd|command|jar|bin|run|zip|tar|tgz|gz|xz|7z)(?:[?#]|$)`)

// OffsiteDownload reports whether text asks to download, install or update
// something from a host other than botobot.app (or its subdomains). A link
// to botobot.app alone never alarms, and nor does a host with no download
// word near it.
func OffsiteDownload(text string) bool {
	norm := normalizeHosts(text)
	for _, m := range hostRe.FindAllStringSubmatchIndex(norm, -1) {
		full := norm[m[0]:m[1]]
		host := strings.ToLower(norm[m[2]:m[3]])
		path := ""
		if m[4] >= 0 {
			path = norm[m[4]:m[5]]
		}
		hasScheme := strings.Contains(full[:m[2]-m[0]], "://")
		if !hasScheme && !strings.HasPrefix(host, "www.") {
			tld := host[strings.LastIndexByte(host, '.')+1:]
			if !bareTLDs[tld] && !strings.HasPrefix(tld, "xn--") && isASCII(tld) {
				continue
			}
			// A bare host (no scheme) whose last label is CJK script and is not a
			// real CJK TLD is almost always a sentence split by an ideographic
			// period ("收到。有更新再跟你說" -> host "收到.有更新再跟你說"), not a
			// domain. Skip it. Real CJK TLDs (中国, 台灣…) and look-alikes in other
			// scripts (a Cyrillic ".аpp") are not CJK, so they still go through.
			if hasCJK(tld) && !cjkTLDs[tld] {
				continue
			}
		}
		if host == OfficialHost || strings.HasSuffix(host, "."+OfficialHost) {
			continue
		}
		if programPath.MatchString(path) {
			return true
		}
		if downloadWord.MatchString(window(norm, m[0], m[1], 120)) {
			return true
		}
	}
	return false
}

// normalizeHosts maps full-width ASCII and the ideographic dots to ASCII so
// "ｅｘａｍｐｌｅ。ｃｏｍ" is seen as a host. It never maps look-alike letters,
// so a Cyrillic look-alike of botobot.app stays unofficial.
func normalizeHosts(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == '。' || r == '．' || r == '｡':
			return '.'
		case isInvisible(r):
			return -1
		}
		return r
	}, s)
}

// window is the text up to n bytes either side of [a,b), cut on rune edges.
func window(s string, a, b, n int) string {
	lo, hi := a-n, b+n
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	for lo > 0 && !isRuneStart(s[lo]) {
		lo--
	}
	for hi < len(s) && !isRuneStart(s[hi]) {
		hi++
	}
	return s[lo:hi]
}

func isRuneStart(c byte) bool { return c&0xC0 != 0x80 }

func isASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}
