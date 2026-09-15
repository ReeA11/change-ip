package config

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"strings"
)

type ProfileEntry struct {
	Prefix  netip.Prefix
	Gateway netip.Addr
}

func ParseProfile(r io.Reader) (map[netip.Addr]ProfileEntry, error) {
	entries := make(map[netip.Addr]ProfileEntry)
	scan := bufio.NewScanner(r)
	line := 0
	for scan.Scan() {
		line++
		raw := strings.TrimSpace(scan.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) < 1 || len(fields) > 2 {
			return nil, fmt.Errorf("profile line %d: expected IP/PREFIX [GATEWAY]", line)
		}
		p, e := netip.ParsePrefix(fields[0])
		if e != nil || !p.Addr().Is4() || p.Bits() < 1 || !UsableIPv4(p.Addr()) {
			return nil, fmt.Errorf("profile line %d: invalid IPv4 prefix", line)
		}
		p = netip.PrefixFrom(p.Addr(), p.Bits())
		entry := ProfileEntry{Prefix: p}
		if len(fields) == 2 && fields[1] != "-" {
			g, e := netip.ParseAddr(fields[1])
			if e != nil || !g.Is4() || !UsableIPv4(g) {
				return nil, fmt.Errorf("profile line %d: invalid gateway", line)
			}
			entry.Gateway = g
		}
		if old, ok := entries[p.Addr()]; ok {
			if old != entry {
				return nil, fmt.Errorf("profile line %d: conflicting duplicate for %s", line, p.Addr())
			}
			return nil, fmt.Errorf("profile line %d: duplicate for %s", line, p.Addr())
		}
		entries[p.Addr()] = entry
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func LoadProfile(path string) (map[netip.Addr]ProfileEntry, error) {
	st, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("profile is not a regular file")
	}
	if st.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("profile is group/world writable")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return ParseProfile(f)
}
func UsableIPv4(a netip.Addr) bool {
	if !a.Is4() || a.IsUnspecified() || a.IsLoopback() || a.IsMulticast() {
		return false
	}
	return a != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}
func SecureMode(mode fs.FileMode) bool { return mode.Perm()&0022 == 0 }
