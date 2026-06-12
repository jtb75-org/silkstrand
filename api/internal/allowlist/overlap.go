package allowlist

import "net"

// ADR 013 D6: CIDR interval helpers for the install-time overlap heuristic.
// These are pure range math — never an authorization boundary (D9). The
// allowlist's Allows() remains the enforcement path; this is UI guidance.

// privateBlocks are the address ranges where CIDR reuse across sites is
// expected (RFC1918 + IPv6 unique-local). An overlap inside these is only a
// warning the operator can dismiss; a public overlap is far more likely a real
// duplicate.
var privateBlocks = func() []*net.IPNet {
	cidrs := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// ParseCIDROrIP accepts a CIDR ("10.0.0.0/24") or a bare IP ("10.0.0.5", taken
// as a host /32 or /128). It returns nil for anything else (e.g. a hostname),
// since only address ranges participate in overlap math.
func ParseCIDROrIP(s string) *net.IPNet {
	if _, n, err := net.ParseCIDR(s); err == nil {
		return n
	}
	if ip := net.ParseIP(s); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
	}
	return nil
}

// Overlaps reports whether two CIDR blocks share any address. CIDR blocks are
// aligned, so two blocks are either disjoint or one contains the other —
// either network IP falling inside the other proves an intersection.
func Overlaps(a, b *net.IPNet) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// Contains reports whether outer strictly or equally contains inner (every
// address of inner is in outer). Used to flag intra-input redundancy
// (a typed /24 inside a typed /16).
func Contains(outer, inner *net.IPNet) bool {
	if outer == nil || inner == nil {
		return false
	}
	if !outer.Contains(inner.IP) {
		return false
	}
	oOnes, oBits := outer.Mask.Size()
	iOnes, iBits := inner.Mask.Size()
	return oBits == iBits && oOnes <= iOnes
}

// IsPrivate reports whether a CIDR sits entirely within RFC1918 / ULA space,
// where cross-site reuse is legitimate and a zone difference can suppress the
// warning (D6/D10).
func IsPrivate(n *net.IPNet) bool {
	if n == nil {
		return false
	}
	for _, p := range privateBlocks {
		if p.Contains(n.IP) {
			return true
		}
	}
	return false
}
