package allowlist

import "testing"

func mustIv(t *testing.T, s string) Interval {
	t.Helper()
	iv, ok := ParseInterval(s)
	if !ok {
		t.Fatalf("ParseInterval(%q) = !ok", s)
	}
	return iv
}

func TestParseInterval(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"10.0.0.0/24", true},
		{"10.0.0.5", true},            // bare IPv4
		{"10.0.0.10-10.0.0.50", true}, // range
		{"10.0.0.50-10.0.0.10", true}, // reversed range tolerated
		{"fc00::/7", true},            // IPv6 ULA
		{"2001:db8::1", true},         // bare IPv6
		{"db.example.com", false},     // hostname
		{"not a cidr", false},
		{"10.0.0.10-nope", false}, // bad range bound
		{"", false},
	}
	for _, c := range cases {
		_, ok := ParseInterval(c.in)
		if ok != c.ok {
			t.Errorf("ParseInterval(%q): ok=%v want %v", c.in, ok, c.ok)
		}
	}
}

func TestOverlaps(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"10.0.0.0/24", "10.0.0.0/16", true}, // contained
		{"10.0.0.0/16", "10.0.0.0/24", true}, // reverse
		{"10.0.0.0/24", "10.0.0.0/24", true}, // identical
		{"10.0.0.0/24", "10.0.1.0/24", false},
		{"10.0.0.0/8", "192.168.0.0/16", false},
		{"203.0.113.0/24", "203.0.113.128/25", true},
		{"10.0.0.0/24", "10.0.0.20-10.0.0.30", true},  // CIDR vs range
		{"10.0.0.40-10.0.0.60", "10.0.0.0/26", true},  // range vs CIDR (.0-.63)
		{"10.0.0.40-10.0.0.60", "10.0.0.0/27", false}, // range vs CIDR (.0-.31)
	}
	for _, c := range cases {
		if got := mustIv(t, c.a).Overlaps(mustIv(t, c.b)); got != c.want {
			t.Errorf("Overlaps(%s,%s)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestContains(t *testing.T) {
	cases := []struct {
		outer, inner string
		want         bool
	}{
		{"10.0.0.0/16", "10.0.0.0/24", true},
		{"10.0.0.0/24", "10.0.0.0/16", false},
		{"10.0.0.0/24", "10.0.0.0/24", true},
		{"10.0.0.0/16", "10.1.0.0/24", false},
		{"10.0.0.0/24", "10.0.0.10-10.0.0.50", true},
	}
	for _, c := range cases {
		if got := mustIv(t, c.outer).Contains(mustIv(t, c.inner)); got != c.want {
			t.Errorf("Contains(%s,%s)=%v want %v", c.outer, c.inner, got, c.want)
		}
	}
}

func TestPrivate(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"10.0.0.0/24", true},
		{"10.0.0.0/8", true},
		{"10.0.0.0/7", false}, // spans public 11.0.0.0/8 — not wholly private
		{"172.16.5.0/24", true},
		{"172.32.0.0/16", false},
		{"192.168.1.0/24", true},
		{"203.0.113.0/24", false},
		{"8.8.8.8", false},
		{"10.0.0.250-11.0.0.5", false}, // range crossing out of 10/8
		{"10.0.0.10-10.0.0.50", true},
		{"fc00::/7", true},
		{"2001:db8::/32", false},
	}
	for _, c := range cases {
		if got := mustIv(t, c.in).Private(); got != c.want {
			t.Errorf("Private(%s)=%v want %v", c.in, got, c.want)
		}
	}
}
