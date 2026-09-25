//go:build linux

package network

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type NetlinkBackend struct{}

func NewNetlinkBackend() *NetlinkBackend { return &NetlinkBackend{} }

func addrFromIP(ip net.IP) netip.Addr {
	if ip == nil {
		return netip.Addr{}
	}
	a, _ := netip.AddrFromSlice(ip.To4())
	return a.Unmap()
}

func prefixFromNet(ipnet *net.IPNet) netip.Prefix {
	if ipnet == nil {
		return netip.Prefix{}
	}
	a := addrFromIP(ipnet.IP)
	ones, _ := ipnet.Mask.Size()
	if !a.IsValid() {
		return netip.Prefix{}
	}
	return netip.PrefixFrom(a, ones)
}

func prefixIPNet(p netip.Prefix) *net.IPNet {
	if !p.IsValid() {
		return nil
	}
	return &net.IPNet{IP: net.IP(p.Addr().AsSlice()), Mask: net.CIDRMask(p.Bits(), 32)}
}

func (b *NetlinkBackend) Interfaces() ([]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	names := make([]string, 0, len(links))
	for _, l := range links {
		names = append(names, l.Attrs().Name)
	}
	sort.Strings(names)
	return names, nil
}

func (b *NetlinkBackend) DefaultRoutes() ([]Route, error) {
	nrs, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, fmt.Errorf("list IPv4 routes: %w", err)
	}
	var out []Route
	for _, r := range nrs {
		if r.Dst != nil && prefixFromNet(r.Dst).Bits() != 0 {
			continue
		}
		link, e := netlink.LinkByIndex(r.LinkIndex)
		if e != nil {
			continue
		}
		table := r.Table
		if table == 0 {
			table = unix.RT_TABLE_MAIN
		}
		out = append(out, Route{Gateway: addrFromIP(r.Gw), Source: addrFromIP(r.Src), Interface: link.Attrs().Name, Table: table, Metric: r.Priority, OnLink: r.Flags&int(netlink.FLAG_ONLINK) != 0})
	}
	return out, nil
}

func routesForLink(link netlink.Link) ([]Route, error) {
	nrs, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{LinkIndex: link.Attrs().Index, Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, err
	}
	out := make([]Route, 0, len(nrs))
	for _, r := range nrs {
		table := r.Table
		if table == 0 {
			table = unix.RT_TABLE_MAIN
		}
		out = append(out, Route{Destination: prefixFromNet(r.Dst), Gateway: addrFromIP(r.Gw), Source: addrFromIP(r.Src), Interface: link.Attrs().Name, Table: table, Metric: r.Priority, OnLink: r.Flags&int(netlink.FLAG_ONLINK) != 0, Scope: int(r.Scope)})
	}
	return out, nil
}

func (b *NetlinkBackend) Snapshot(name string) (State, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return State{}, fmt.Errorf("find interface %s: %w", name, err)
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return State{}, fmt.Errorf("list IPv4 on %s: %w", name, err)
	}
	state := State{Interface: name}
	for _, a := range addrs {
		if p := prefixFromNet(a.IPNet); p.IsValid() {
			state.Addresses = append(state.Addresses, Address{Prefix: p, Dynamic: a.Flags&unix.IFA_F_PERMANENT == 0})
		}
	}
	routes, err := routesForLink(link)
	if err != nil {
		return State{}, fmt.Errorf("list routes on %s: %w", name, err)
	}
	state.Routes = routes
	var routed RouteResult
	var routedOK bool
	if rr, routeErr := b.RouteTo(netip.MustParseAddr("1.1.1.1"), name); routeErr == nil {
		routed = rr
		routedOK = true
	}
	selection := routes
	if routedOK {
		var matched []Route
		for _, r := range routes {
			if !r.IsDefault() {
				continue
			}
			if routed.Table != 0 && r.Table != routed.Table {
				continue
			}
			if routed.Gateway.IsValid() && r.Gateway != routed.Gateway {
				continue
			}
			matched = append(matched, r)
		}
		if len(matched) > 0 {
			selection = matched
		}
	}
	// Additional provider interfaces frequently have an address but no
	// default route. Keep them inspectable so the application can configure
	// the missing route instead of rejecting the interface.
	def, selectErr := SelectDefault(selection, name)
	if selectErr == nil {
		state.DefaultRoute = def
	}
	if state.DefaultRoute.Gateway.IsValid() {
		for _, r := range routes {
			if r.Destination.IsValid() && r.Destination.Bits() == 32 && r.Destination.Addr() == state.DefaultRoute.Gateway && !r.Gateway.IsValid() && r.Scope == ScopeLink {
				x := r
				state.GatewayHostRoute = &x
				break
			}
		}
	}
	if routedOK {
		state.OutboundSource = routed.Source
		if !state.DefaultRoute.Source.IsValid() {
			state.DefaultRoute.Source = routed.Source
		}
	}
	return state, nil
}

func (b *NetlinkBackend) RouteTo(dest netip.Addr, iface string) (RouteResult, error) {
	routes, err := netlink.RouteGet(net.IP(dest.AsSlice()))
	if err != nil {
		return RouteResult{}, fmt.Errorf("route to %s: %w", dest, err)
	}
	for _, r := range routes {
		link, e := netlink.LinkByIndex(r.LinkIndex)
		if e != nil {
			continue
		}
		if iface != "" && link.Attrs().Name != iface {
			continue
		}
		return RouteResult{Interface: link.Attrs().Name, Source: addrFromIP(r.Src), Gateway: addrFromIP(r.Gw), Table: r.Table}, nil
	}
	return RouteResult{}, fmt.Errorf("route to %s does not use interface %s", dest, iface)
}

func (b *NetlinkBackend) AddAddress(iface string, prefix netip.Prefix) error {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return err
	}
	return netlink.AddrAdd(link, &netlink.Addr{IPNet: prefixIPNet(prefix)})
}
func (b *NetlinkBackend) DeleteAddress(iface string, prefix netip.Prefix) error {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return err
	}
	err = netlink.AddrDel(link, &netlink.Addr{IPNet: prefixIPNet(prefix)})
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EADDRNOTAVAIL) {
		return nil
	}
	return err
}
func nlRoute(r Route) (netlink.Route, error) {
	link, err := netlink.LinkByName(r.Interface)
	if err != nil {
		return netlink.Route{}, err
	}
	nr := netlink.Route{LinkIndex: link.Attrs().Index, Dst: prefixIPNet(r.Destination), Table: r.Table, Priority: r.Metric, Scope: netlink.Scope(r.Scope)}
	if r.Gateway.IsValid() {
		nr.Gw = net.IP(r.Gateway.AsSlice())
	}
	if r.Source.IsValid() {
		nr.Src = net.IP(r.Source.AsSlice())
	}
	if r.OnLink {
		nr.Flags |= int(netlink.FLAG_ONLINK)
	}
	return nr, nil
}
func (b *NetlinkBackend) ReplaceRoute(r Route) error {
	nr, e := nlRoute(r)
	if e != nil {
		return e
	}
	return netlink.RouteReplace(&nr)
}
func (b *NetlinkBackend) DeleteRoute(r Route) error {
	nr, e := nlRoute(r)
	if e != nil {
		return e
	}
	err := netlink.RouteDel(&nr)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func (b *NetlinkBackend) Rules() ([]Rule, error) {
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("list IPv4 rules: %w", err)
	}
	out := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		var source netip.Prefix
		if rule.Src != nil {
			source = prefixFromNet(rule.Src)
		}
		out = append(out, Rule{Source: source, Table: rule.Table, Priority: rule.Priority})
	}
	return out, nil
}

func nlRule(rule Rule) *netlink.Rule {
	nr := netlink.NewRule()
	nr.Family = netlink.FAMILY_V4
	nr.Src = prefixIPNet(rule.Source)
	nr.Table = rule.Table
	nr.Priority = rule.Priority
	return nr
}

func (b *NetlinkBackend) AddRule(rule Rule) error {
	err := netlink.RuleAdd(nlRule(rule))
	if errors.Is(err, unix.EEXIST) {
		return nil
	}
	return err
}

func (b *NetlinkBackend) DeleteRule(rule Rule) error {
	err := netlink.RuleDel(nlRule(rule))
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
