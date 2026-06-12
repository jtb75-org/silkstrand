package allowlist

import "net"

// ADR 013 D6: install-time overlap heuristic. CIDRs, bare IPs, and "a-b"
// ranges all reduce to an inclusive Interval so they compare uniformly — the
// install panel and allowlist parser accept all three. Pure range math, never
// an authorization boundary (D9); Allows() remains the enforcement path.

// Interval is an inclusive IP range [From, To], both in 16-byte form so IPv4
// (v4-in-v6 mapped) and IPv6 compare consistently.
type Interval struct{ From, To net.IP }

// privateIntervals are the ranges where CIDR reuse across sites is expected
// (RFC1918 + IPv6 ULA). An overlap that is *wholly* inside these is only a
// dismissible warning; anything touching public space warns unconditionally.
var privateIntervals = func() []Interval {
	var out []Interval
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, cidrInterval(n))
		}
	}
	return out
}()

// cidrInterval turns a parsed network into [network, broadcast].
func cidrInterval(n *net.IPNet) Interval {
	lo := make(net.IP, len(n.IP))
	hi := make(net.IP, len(n.IP))
	for i := range n.IP {
		lo[i] = n.IP[i] & n.Mask[i]
		hi[i] = n.IP[i] | ^n.Mask[i]
	}
	return Interval{From: lo.To16(), To: hi.To16()}
}

// ParseInterval accepts a CIDR ("10.0.0.0/24"), a bare IP ("10.0.0.5"), or an
// "a-b" range ("10.0.0.10-10.0.0.50"). It returns ok=false for a hostname or
// anything unparseable — only address ranges participate in overlap math.
func ParseInterval(s string) (Interval, bool) {
	n, ip, rng, host, err := parseEntry(s)
	if err != nil || host != "" {
		return Interval{}, false
	}
	switch {
	case n != nil:
		return cidrInterval(n), true
	case rng != nil:
		from, to := rng.from.To16(), rng.to.To16()
		if compareIPs(from, to) > 0 {
			from, to = to, from // tolerate reversed bounds
		}
		return Interval{From: from, To: to}, true
	case ip != nil:
		v := ip.To16()
		return Interval{From: v, To: v}, true
	}
	return Interval{}, false
}

// Overlaps reports whether two intervals share any address.
func (iv Interval) Overlaps(o Interval) bool {
	return compareIPs(iv.From, o.To) <= 0 && compareIPs(o.From, iv.To) <= 0
}

// Contains reports whether iv wholly covers o (every address of o is in iv).
func (iv Interval) Contains(o Interval) bool {
	return compareIPs(iv.From, o.From) <= 0 && compareIPs(o.To, iv.To) <= 0
}

// Private reports whether the whole interval sits inside RFC1918 / ULA space.
// Checking the entire range (not just the low address) is what stops a mixed
// supernet like 10.0.0.0/7 — which spans public 11.0.0.0/8 — from being
// treated as private and suppressing a real public overlap.
func (iv Interval) Private() bool {
	for _, p := range privateIntervals {
		if p.Contains(iv) {
			return true
		}
	}
	return false
}
