// Package geo answers one question offline: is this IP address in Taiwan?
// The data is the TW subset of the DB-IP Lite country database, embedded at
// build time (IP Geolocation by DB-IP, https://db-ip.com, CC BY 4.0). It makes
// no network calls and keeps no record of the addresses it is asked about.
// Regenerate with scripts/gen-geo-tw.py.
package geo

import (
	"bytes"
	_ "embed"
	"net/netip"
	"sort"
	"strings"
)

//go:embed tw_ranges.txt
var twData []byte

type ipRange struct{ lo, hi netip.Addr }

var twRanges = parse(twData)

func parse(b []byte) []ipRange {
	var out []ipRange
	for _, line := range bytes.Split(b, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		lo, hi, ok := strings.Cut(s, " ")
		a, err1 := netip.ParseAddr(lo)
		z, err2 := netip.ParseAddr(hi)
		if !ok || err1 != nil || err2 != nil || a.Is4() != z.Is4() {
			continue
		}
		out = append(out, ipRange{a, z})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].lo.Less(out[j].lo) })
	return out
}

// Ranges reports how many TW ranges are loaded (for tests and health checks).
func Ranges() int { return len(twRanges) }

// IsTW reports whether ip (a textual IPv4 or IPv6 address) is in Taiwan. An
// unparsable address is not.
func IsTW(ip string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	a = a.Unmap()
	// First range whose upper bound is >= a.
	i := sort.Search(len(twRanges), func(i int) bool { return !twRanges[i].hi.Less(a) })
	if i == len(twRanges) {
		return false
	}
	r := twRanges[i]
	return r.lo.Is4() == a.Is4() && !a.Less(r.lo)
}
