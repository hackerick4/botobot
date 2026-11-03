package store

import (
	"crypto/rand"
	"fmt"
)

// ID prefixes (DEV_SPEC §5). messages use the client-supplied msg_id and
// delivery_status has no synthetic id, so neither appears here.
const (
	prefixBridge = "br_"
	prefixLand   = "land_"
	prefixKey    = "key_"
	prefixPubKey = "pk_"
	prefixEnv    = "env_"
)

// idRandLen is the number of base62 characters after the prefix. 22 base62
// characters carry log2(62)*22 ≈ 131 bits, comfortably above the 128-bit
// minimum required by DEV_SPEC §5.
const idRandLen = 22

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// newID returns prefix followed by idRandLen base62 characters drawn from
// crypto/rand. Rejection sampling keeps the distribution uniform (no modulo
// bias). It panics only if the system CSPRNG fails, which is unrecoverable.
func newID(prefix string) string {
	buf := make([]byte, idRandLen)
	// 62*4 = 248: reject bytes >= 248 so each accepted byte maps uniformly.
	const limit = 248
	var b [1]byte
	for i := 0; i < idRandLen; {
		if _, err := rand.Read(b[:]); err != nil {
			panic(fmt.Sprintf("store: crypto/rand failed: %v", err))
		}
		if b[0] >= limit {
			continue
		}
		buf[i] = base62[b[0]%62]
		i++
	}
	return prefix + string(buf)
}
