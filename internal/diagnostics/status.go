package diagnostics

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ReeA11/change-ip/internal/network"
	"github.com/ReeA11/change-ip/internal/persist"
)

type Status struct {
	State                                         network.State
	Desired                                       *network.State
	UnitPath, ConfigPath, UnitEnabled, UnitActive string
	GatewayReachable                              bool
	PersistenceManagers                           []string
	AddressConflicts                              []string
	Rules                                         []network.Rule
}

func systemctl(args ...string) string {
	b, e := exec.Command("systemctl", args...).Output()
	if e != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(b))
}
func Collect(b network.Backend, iface, binary string) (Status, error) {
	s, e := b.Snapshot(iface)
	if e != nil {
		return Status{}, e
	}
	p := persist.NewSystemd(binary)
	cfg, unit, e := p.Paths(iface)
	if e != nil {
		return Status{}, e
	}
	out := Status{State: s, ConfigPath: cfg, UnitPath: unit}
	if rules, rulesErr := b.Rules(); rulesErr == nil {
		out.Rules = rules
	}
	owners := make(map[netip.Addr][]string)
	if names, listErr := b.Interfaces(); listErr == nil {
		for _, name := range names {
			if !network.InterfaceAllowed(name) {
				continue
			}
			state, snapshotErr := b.Snapshot(name)
			if snapshotErr != nil {
				continue
			}
			for _, address := range state.Addresses {
				owners[address.Prefix.Addr()] = append(owners[address.Prefix.Addr()], name)
			}
		}
	}
	for ip, interfaces := range owners {
		if len(interfaces) > 1 {
			sort.Strings(interfaces)
			out.AddressConflicts = append(out.AddressConflicts, fmt.Sprintf("IP %s is configured on multiple interfaces: %s", ip, strings.Join(interfaces, ", ")))
		}
	}
	sort.Strings(out.AddressConflicts)
	if s.DefaultRoute.Gateway.IsValid() {
		_, routeErr := b.RouteTo(s.DefaultRoute.Gateway, iface)
		out.GatewayReachable = routeErr == nil
	}
	for _, name := range []string{"NetworkManager.service", "systemd-networkd.service", "networking.service"} {
		if systemctl("is-active", name) == "active" {
			out.PersistenceManagers = append(out.PersistenceManagers, name)
		}
	}
	if c, e := persist.Load(cfg); e == nil {
		out.Desired = &c.State
	}
	if _, e := os.Stat(unit); e == nil {
		out.UnitEnabled = systemctl("is-enabled", filepath.Base(unit))
		out.UnitActive = systemctl("is-active", filepath.Base(unit))
	} else {
		out.UnitEnabled = "missing"
		out.UnitActive = "missing"
	}
	return out, nil
}
func Problems(s Status) []string {
	var p []string
	if s.Desired == nil {
		p = append(p, "apply configuration is missing")
	} else {
		d := s.Desired
		if _, ok := network.HasAddress(s.State, d.OutboundSource); !ok {
			p = append(p, fmt.Sprintf("target IP %s is absent", d.OutboundSource))
		}
		if s.State.OutboundSource != d.OutboundSource {
			p = append(p, fmt.Sprintf("outbound source is %s, expected %s", s.State.OutboundSource, d.OutboundSource))
		}
		if s.State.DefaultRoute.Gateway != d.DefaultRoute.Gateway {
			p = append(p, "gateway differs from desired state")
		}
		if s.State.DefaultRoute.Table != d.DefaultRoute.Table || s.State.DefaultRoute.Metric != d.DefaultRoute.Metric {
			p = append(p, "default route table/metric differs from desired state")
		}
		for _, wanted := range d.ManagedRules {
			found := false
			for _, rule := range s.Rules {
				if rule == wanted {
					found = true
					break
				}
			}
			if !found {
				p = append(p, fmt.Sprintf("return path for IP %s is missing", wanted.Source.Addr()))
			}
		}
	}
	if s.UnitEnabled == "missing" {
		p = append(p, "persistence unit is missing")
	} else if s.UnitEnabled != "enabled" {
		p = append(p, "persistence unit is not enabled")
	}
	if s.UnitActive == "failed" {
		p = append(p, "persistence unit failed")
	}
	if s.State.DefaultRoute.Gateway.IsValid() && !s.GatewayReachable {
		p = append(p, "gateway route is missing or uses another interface")
	}
	if len(s.PersistenceManagers) > 1 {
		p = append(p, fmt.Sprintf("potential persistence conflict: active managers: %s", strings.Join(s.PersistenceManagers, ", ")))
	}
	p = append(p, s.AddressConflicts...)
	return p
}
