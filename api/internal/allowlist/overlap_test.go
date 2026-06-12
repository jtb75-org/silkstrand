package allowlist

import "testing"

func TestParseCIDROrIP(t *testing.T) {
	cases := []struct {
		in  string
		nil bool
	}{
		{"10.0.0.0/24", false},
		{"10.0.0.5", false},      // bare IPv4 -> /32
		{"fc00::/7", false},      // IPv6 ULA
		{"2001:db8::1", false},   // bare IPv6 -> /128
		{"db.example.com", true}, // hostname -> no range
		{"not a cidr", true},
		{"", true},
	}
	for _, c := range cases {
		got := ParseCIDROrIP(c.in)
		if (got == nil) != c.nil {
			t.Errorf("ParseCIDROrIP(%q): nil=%v want nil=%v", c.in, got == nil, c.nil)
		}
	}
}

func TestOverlaps(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"10.0.0.0/24", "10.0.0.0/16", true}, // contained
		{"10.0.0.0/16", "10.0.0.0/24", true}, // reverse containment
		{"10.0.0.0/24", "10.0.0.0/24", true}, // identical
		{"10.0.0.0/24", "10.0.1.0/24", false},
		{"10.0.0.0/8", "192.168.0.0/16", false},
		{"203.0.113.0/24", "203.0.113.128/25", true},
	}
	for _, c := range cases {
		a, b := ParseCIDROrIP(c.a), ParseCIDROrIP(c.b)
		if got := Overlaps(a, b); got != c.want {
			t.Errorf("Overlaps(%s,%s)=%v want %v", c.a, c.b, got, c.want)
		}
	}
	if Overlaps(nil, ParseCIDROrIP("10.0.0.0/8")) {
		t.Error("Overlaps(nil,...) should be false")
	}
}

func TestContains(t *testing.T) {
	cases := []struct {
		outer, inner string
		want         bool
	}{
		{"10.0.0.0/16", "10.0.0.0/24", true},
		{"10.0.0.0/24", "10.0.0.0/16", false}, // inner bigger
		{"10.0.0.0/24", "10.0.0.0/24", true},  // equal counts as contained
		{"10.0.0.0/16", "10.1.0.0/24", false},
	}
	for _, c := range cases {
		if got := Contains(ParseCIDROrIP(c.outer), ParseCIDROrIP(c.inner)); got != c.want {
			t.Errorf("Contains(%s,%s)=%v want %v", c.outer, c.inner, got, c.want)
		}
	}
}

func TestIsPrivate(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"10.0.0.0/24", true},
		{"172.16.5.0/24", true},
		{"172.32.0.0/16", false}, // outside 172.16/12
		{"192.168.1.0/24", true},
		{"203.0.113.0/24", false}, // public
		{"8.8.8.8", false},
		{"fc00::/7", true},
		{"fd12:3456::/32", true},
		{"2001:db8::/32", false},
	}
	for _, c := range cases {
		if got := IsPrivate(ParseCIDROrIP(c.in)); got != c.want {
			t.Errorf("IsPrivate(%s)=%v want %v", c.in, got, c.want)
		}
	}
}
