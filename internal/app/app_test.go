package app

import (
	"net/netip"
	"os"
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
