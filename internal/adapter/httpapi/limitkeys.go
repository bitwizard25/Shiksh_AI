package httpapi

import (
	"net/netip"

	"golang.org/x/text/unicode/norm"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

// emailKey returns a normalized rate-limit key for an email address: the parsed, normalized
// address, or the constant "invalid" when parsing fails. This bounds the key to at most 254
// bytes and collapses any amount of junk input into a single shared bucket.
func emailKey(raw string) string {
	email, err := entity.ParseEmail(norm.NFC.String(raw))
	if err != nil {
		return "invalid"
	}
	return email.String()
}

// limiterIP returns a rate-limit key for a client IP. IPv4 (including IPv4-mapped IPv6)
// addresses key on the bare address; other IPv6 addresses key on their /64 prefix, since a
// single residential or campus customer is commonly routed a /64 and per-address keying would
// let them bypass the limit by rotating addresses within it. A parse failure returns the raw
// input unchanged, so unparsable values still collapse into a shared (if unbounded) key.
func limiterIP(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if addr.Is4() || addr.Is4In6() {
		return addr.Unmap().String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return prefix.String()
}
