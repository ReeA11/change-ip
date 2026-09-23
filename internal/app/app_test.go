package app

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReeA11/change-ip/internal/network"
)

type fakeBackend struct {
	state     network.State
	mutations int
}

func (f *fakeBackend) Interfaces() ([]string, error) { return []string{f.state.Interface}, nil }
func (f *fakeBackend) DefaultRoutes() ([]network.Route, error) {
	return []network.Route{f.state.DefaultRoute}, nil
}
func (f *fakeBackend) Snapshot(string) (network.State, error) { return f.state, nil }
func (f *fakeBackend) RouteTo(_ netip.Addr, iface string) (network.RouteResult, error) {
	return network.RouteResult{Interface: f.state.Interface, Source: f.state.OutboundSource, Gateway: f.state.DefaultRoute.Gateway, Table: f.state.DefaultRoute.Table}, nil
}
func (f *fakeBackend) AddAddress(string, netip.Prefix) error    { f.mutations++; return nil }
func (f *fakeBackend) DeleteAddress(string, netip.Prefix) error { f.mutations++; return nil }
func (f *fakeBackend) ReplaceRoute(network.Route) error         { f.mutations++; return nil }
func (f *fakeBackend) DeleteRoute(network.Route) error          { f.mutations++; return nil }
func appState() network.State {
	return network.State{Interface: "eth0", Addresses: []network.Address{{Prefix: netip.MustParsePrefix("192.0.2.10/24")}, {Prefix: netip.MustParsePrefix("192.0.2.20/24")}}, OutboundSource: netip.MustParseAddr("192.0.2.10"), DefaultRoute: network.Route{Interface: "eth0", Gateway: netip.MustParseAddr("192.0.2.1"), Source: netip.MustParseAddr("192.0.2.10"), Table: 254, Metric: 50}}
}
func TestResolveExistingAddressWithoutPrefix(t *testing.T) {
	f := &fakeBackend{state: appState()}
	a := New(f, "/usr/local/sbin/change-ip")
	p, e := a.Resolve(Options{Target: "192.0.2.20", Interface: "eth0"})
	if e != nil {
		t.Fatal(e)
	}
	if p.Target.OutboundSource != netip.MustParseAddr("192.0.2.20") || p.AddressToAdd != nil {
		t.Fatalf("%+v", p)
	}
}
func TestDryRunDoesNotMutate(t *testing.T) {
	f := &fakeBackend{state: appState()}
	a := New(f, "/usr/local/sbin/change-ip")
	a.Out = os.Stdout
	if _, e := a.Apply(Options{Target: "192.0.2.30/24", Interface: "eth0", DryRun: true, Yes: true}, nil); e != nil {
		t.Fatal(e)
	}
	if f.mutations != 0 {
		t.Fatalf("mutations=%d", f.mutations)
	}
}
func TestNewAddressRequiresPrefix(t *testing.T) {
	f := &fakeBackend{state: appState()}
	a := New(f, "/usr/local/sbin/change-ip")
	if _, e := a.Resolve(Options{Target: "192.0.2.30", Interface: "eth0"}); e == nil {
		t.Fatal("expected prefix error")
	}
}

func TestResolveAddAddressesPreservesOutboundRoute(t *testing.T) {
	f := &fakeBackend{state: appState()}
	a := New(f, "/usr/local/sbin/change-ip")
	p, err := a.ResolveAddAddresses(Options{Interface: "eth0", Targets: []string{"192.0.2.30/24", "198.51.100.30/32"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.ReplaceDefault || p.Target.OutboundSource != p.Before.OutboundSource || p.Target.DefaultRoute != p.Before.DefaultRoute {
		t.Fatalf("address-only plan changed outbound routing: %+v", p)
	}
	if len(p.AddedAddresses()) != 2 {
		t.Fatalf("addresses=%v", p.AddedAddresses())
	}
}

func TestResolveGatewayKeepsOutboundSource(t *testing.T) {
	f := &fakeBackend{state: appState()}
	a := New(f, "/usr/local/sbin/change-ip")
	p, err := a.ResolveGateway(Options{Interface: "eth0", Gateway: "192.0.2.254"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Target.OutboundSource != p.Before.OutboundSource || p.Target.DefaultRoute.Source != p.Before.OutboundSource {
		t.Fatalf("gateway plan changed source: %+v", p)
	}
	if p.Target.DefaultRoute.Gateway != netip.MustParseAddr("192.0.2.254") {
		t.Fatalf("gateway=%s", p.Target.DefaultRoute.Gateway)
	}
}

type multiBackend struct {
	states map[string]network.State
	routes []network.Route
}

func (f *multiBackend) Interfaces() ([]string, error)               { return []string{"eth0", "eth1"}, nil }
func (f *multiBackend) DefaultRoutes() ([]network.Route, error)     { return f.routes, nil }
func (f *multiBackend) Snapshot(name string) (network.State, error) { return f.states[name], nil }
func (f *multiBackend) RouteTo(_ netip.Addr, iface string) (network.RouteResult, error) {
	name := iface
	if name == "" {
		name = "eth0"
	}
	s := f.states[name]
	return network.RouteResult{Interface: name, Source: s.OutboundSource, Gateway: s.DefaultRoute.Gateway, Table: s.DefaultRoute.Table}, nil
}
func (f *multiBackend) AddAddress(string, netip.Prefix) error    { return nil }
func (f *multiBackend) DeleteAddress(string, netip.Prefix) error { return nil }
func (f *multiBackend) ReplaceRoute(network.Route) error         { return nil }
func (f *multiBackend) DeleteRoute(network.Route) error          { return nil }

func TestResolveDefaultInterfaceSelectsItsAddressAndBetterMetric(t *testing.T) {
	eth0 := appState()
	eth1 := network.State{
		Interface:      "eth1",
		Addresses:      []network.Address{{Prefix: netip.MustParsePrefix("198.51.100.10/24")}},
		OutboundSource: netip.MustParseAddr("198.51.100.10"),
		DefaultRoute:   network.Route{Interface: "eth1", Gateway: netip.MustParseAddr("198.51.100.1"), Source: netip.MustParseAddr("198.51.100.10"), Table: 254, Metric: 200},
	}
	f := &multiBackend{states: map[string]network.State{"eth0": eth0, "eth1": eth1}, routes: []network.Route{eth0.DefaultRoute, eth1.DefaultRoute}}
	a := New(f, "/usr/local/sbin/change-ip")
	p, err := a.ResolveDefaultInterface(Options{Interface: "eth1"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Target.Interface != "eth1" || p.Target.OutboundSource != netip.MustParseAddr("198.51.100.10") || p.Target.DefaultRoute.Metric != 49 || !p.VerifyGlobal {
		t.Fatalf("unexpected interface plan: %+v", p)
	}
	if len(p.DisableInterfaces) != 1 || p.DisableInterfaces[0] != "eth0" {
		t.Fatalf("persistence units to disable=%v", p.DisableInterfaces)
	}
}

func TestResolveDefaultInterfaceDemotesCompetingMetricZeroRoute(t *testing.T) {
	eth0 := appState()
	eth0.DefaultRoute.Metric = 0
	eth1 := network.State{
		Interface:      "eth1",
		Addresses:      []network.Address{{Prefix: netip.MustParsePrefix("198.51.100.10/24")}},
		OutboundSource: netip.MustParseAddr("198.51.100.10"),
		DefaultRoute:   network.Route{Interface: "eth1", Gateway: netip.MustParseAddr("198.51.100.1"), Source: netip.MustParseAddr("198.51.100.10"), Table: 254, Metric: 200},
	}
	f := &multiBackend{states: map[string]network.State{"eth0": eth0, "eth1": eth1}, routes: []network.Route{eth0.DefaultRoute, eth1.DefaultRoute}}
	a := New(f, "/usr/local/sbin/change-ip")
	p, err := a.ResolveDefaultInterface(Options{Interface: "eth1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.RouteChanges) != 1 || p.RouteChanges[0].Before.Metric != 0 || p.RouteChanges[0].Target.Metric != 1 {
		t.Fatalf("route changes=%+v", p.RouteChanges)
	}
	if len(p.Target.ManagedRoutes) != 1 || p.Target.ManagedRoutes[0].Interface != "eth0" {
		t.Fatalf("managed routes=%+v", p.Target.ManagedRoutes)
	}
}

func TestDefaultBackupRootUsesInvokingUserHomeFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUDO_USER", "change-ip-user-that-does-not-exist")
	if got, want := defaultBackupRoot(), filepath.Join(home, ".local", "state", "change-ip", "backups"); got != want {
		t.Fatalf("backup root=%q want %q", got, want)
	}
}
