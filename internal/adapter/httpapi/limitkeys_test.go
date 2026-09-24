package httpapi

import (
	"strings"
	"testing"
)

func TestEmailKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"normal", "Asha@Example.com", "asha@example.com"},
		{"huge input", strings.Repeat("a", 1<<20), "invalid"},
		{"padded and mixed case", " ASHA@Example.com ", "asha@example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := emailKey(c.in); got != c.want {
				t.Errorf("emailKey(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

func TestLimiterIP(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ipv4", "203.0.113.9", "203.0.113.9"},
		{"ipv4-mapped ipv6", "::ffff:203.0.113.9", "203.0.113.9"},
		{"ipv6 a", "2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"ipv6 b", "2001:db8:1:2:ffff::1", "2001:db8:1:2::/64"},
		{"garbage", "garbage", "garbage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := limiterIP(c.in); got != c.want {
				t.Errorf("limiterIP(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
	a := limiterIP("2001:db8:1:2:3:4:5:6")
	b := limiterIP("2001:db8:1:2:ffff::1")
	if a != b {
		t.Errorf("expected the two /64-adjacent addresses to collapse to the same key, got %q and %q", a, b)
	}
}
