package scheduler

import (
	"fmt"
	"math/bits"
	"net/netip"
	"strings"

	"github.com/jtb75/silkstrand/api/internal/model"
	"github.com/jtb75/silkstrand/api/internal/store"
)

const DefaultDiscoveryChunkIPs = 1024

// splitDiscoveryTarget turns a discovery target into durable, batch-sized
// chunks. It deliberately keeps hostnames and single IPs as one chunk; only
// IPv4 CIDRs/ranges are split in slice 1.
func splitDiscoveryTarget(scanID, tenantID string, agentID *string, targetType, targetIdentifier string, maxIPs int) ([]store.CreateScanChunkInput, error) {
	if maxIPs <= 0 {
		maxIPs = DefaultDiscoveryChunkIPs
	}
	targetIdentifier = strings.TrimSpace(targetIdentifier)
	switch targetType {
	case model.TargetTypeCIDR:
		prefix, err := netip.ParsePrefix(targetIdentifier)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", targetIdentifier, err)
		}
		prefix = prefix.Masked()
		if !prefix.Addr().Is4() {
			return oneChunk(scanID, tenantID, agentID, targetType, targetIdentifier), nil
		}
		start := prefix.Addr()
		size := uint64(1) << uint64(32-prefix.Bits())
		return splitIPv4Window(scanID, tenantID, agentID, targetType, start, size, maxIPs), nil
	case model.TargetTypeNetworkRange:
		start, end, err := parseIPv4Range(targetIdentifier)
		if err != nil {
			return nil, err
		}
		startN := ipv4ToUint32(start)
		endN := ipv4ToUint32(end)
		return splitIPv4Window(scanID, tenantID, agentID, model.TargetTypeCIDR, start, uint64(endN-startN)+1, maxIPs), nil
	default:
		return oneChunk(scanID, tenantID, agentID, targetType, targetIdentifier), nil
	}
}

func oneChunk(scanID, tenantID string, agentID *string, targetType, targetIdentifier string) []store.CreateScanChunkInput {
	return []store.CreateScanChunkInput{{
		ScanID:           scanID,
		TenantID:         tenantID,
		AgentID:          agentID,
		ChunkIndex:       0,
		TargetType:       targetType,
		TargetIdentifier: targetIdentifier,
	}}
}

func splitIPv4Window(scanID, tenantID string, agentID *string, targetType string, start netip.Addr, size uint64, maxIPs int) []store.CreateScanChunkInput {
	maxChunkSize := uint64(maxIPs)
	if maxChunkSize == 0 {
		maxChunkSize = DefaultDiscoveryChunkIPs
	}
	out := make([]store.CreateScanChunkInput, 0)
	startN := ipv4ToUint32(start)
	for offset := uint64(0); offset < size; {
		cur := startN + uint32(offset)
		remaining := size - offset
		n := largestCIDRBlockSize(cur, remaining, maxChunkSize)
		chunkStart := uint32ToIPv4(startN + uint32(offset))
		chunkEnd := uint32ToIPv4(startN + uint32(offset+n-1))
		startS := chunkStart.String()
		endS := chunkEnd.String()
		out = append(out, store.CreateScanChunkInput{
			ScanID:           scanID,
			TenantID:         tenantID,
			AgentID:          agentID,
			ChunkIndex:       len(out),
			TargetType:       targetType,
			TargetIdentifier: chunkIdentifier(targetType, chunkStart, chunkEnd),
			IPStart:          &startS,
			IPEnd:            &endS,
			IPCount:          int(n),
		})
		offset += n
	}
	return out
}

func chunkIdentifier(targetType string, start, end netip.Addr) string {
	if start == end {
		return start.String()
	}
	if prefix, ok := contiguousCIDR(start, end); ok {
		return prefix.String()
	}
	return start.String() + "-" + end.String()
}

func largestCIDRBlockSize(start uint32, remaining, maxChunkSize uint64) uint64 {
	limit := maxChunkSize
	if remaining < limit {
		limit = remaining
	}
	n := uint64(1) << bits.TrailingZeros32(start)
	if start == 0 {
		n = uint64(1) << 32
	}
	for n > limit {
		n >>= 1
	}
	return n
}

func contiguousCIDR(start, end netip.Addr) (netip.Prefix, bool) {
	startN := ipv4ToUint32(start)
	endN := ipv4ToUint32(end)
	size := uint64(endN) - uint64(startN) + 1
	if size == 0 || size&(size-1) != 0 || uint64(startN)%size != 0 {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(start, 32-bits.TrailingZeros64(size)).Masked(), true
}

func parseIPv4Range(s string) (netip.Addr, netip.Addr, error) {
	left, right, ok := strings.Cut(s, "-")
	if !ok {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("invalid IP range %q", s)
	}
	start, err := netip.ParseAddr(strings.TrimSpace(left))
	if err != nil || !start.Is4() {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("invalid IP range start %q", strings.TrimSpace(left))
	}
	end, err := netip.ParseAddr(strings.TrimSpace(right))
	if err != nil || !end.Is4() {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("invalid IP range end %q", strings.TrimSpace(right))
	}
	if ipv4ToUint32(end) < ipv4ToUint32(start) {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("invalid IP range %q: end before start", s)
	}
	return start, end, nil
}

func ipv4ToUint32(addr netip.Addr) uint32 {
	b := addr.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func uint32ToIPv4(n uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}
