package network

import "net/netip"

type Address struct {
	Prefix  netip.Prefix `json:"prefix"`
	Dynamic bool         `json:"dynamic,omitempty"`
}

type Route struct {
	Destination netip.Prefix `json:"destination,omitempty"`
	Gateway     netip.Addr   `json:"gateway,omitempty"`
	Source      netip.Addr   `json:"source,omitempty"`
	Interface   string       `json:"interface"`
	Table       int          `json:"table"`
	Metric      int          `json:"metric"`
	OnLink      bool         `json:"on_link"`
	Scope       int          `json:"scope,omitempty"`
}

const ScopeLink = 253

func (r Route) IsDefault() bool { return !r.Destination.IsValid() || r.Destination.Bits() == 0 }

type State struct {
	Interface        string     `json:"interface"`
	Addresses        []Address  `json:"addresses"`
	OutboundSource   netip.Addr `json:"outbound_source,omitempty"`
	DefaultRoute     Route      `json:"default_route"`
	GatewayHostRoute *Route     `json:"gateway_host_route,omitempty"`
	Routes           []Route    `json:"routes,omitempty"`
	ManagedRoutes    []Route    `json:"managed_routes,omitempty"`
}

type RouteResult struct {
	Interface string
	Source    netip.Addr
	Gateway   netip.Addr
	Table     int
}

type RouteChange struct {
	Before Route `json:"before"`
	Target Route `json:"target"`
}

type Backend interface {
	Interfaces() ([]string, error)
	DefaultRoutes() ([]Route, error)
	Snapshot(interfaceName string) (State, error)
	RouteTo(destination netip.Addr, interfaceName string) (RouteResult, error)
	AddAddress(interfaceName string, prefix netip.Prefix) error
	DeleteAddress(interfaceName string, prefix netip.Prefix) error
	ReplaceRoute(route Route) error
	DeleteRoute(route Route) error
}
