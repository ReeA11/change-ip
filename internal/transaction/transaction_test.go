package transaction

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/ReeA11/change-ip/internal/network"
)

func baseState() network.State {
	return network.State{Interface: "eth0", Addresses: []network.Address{{Prefix: netip.MustParsePrefix("192.0.2.10/24")}}, OutboundSource: netip.MustParseAddr("192.0.2.10"), DefaultRoute: network.Route{Gateway: netip.MustParseAddr("192.0.2.1"), Source: netip.MustParseAddr("192.0.2.10"), Interface: "eth0", Table: 254, Metric: 100}}
}
func TestBuildPlanPreservesRouteProperties(t *testing.T) {
	p, e := BuildPlan(baseState(), netip.MustParsePrefix("192.0.2.20/24"), netip.MustParseAddr("192.0.2.1"))
	if e != nil {
		t.Fatal(e)
	}
	if p.AddressToAdd == nil {
		t.Fatal("expected address addition")
	}
	if p.Target.DefaultRoute.Metric != 100 || p.Target.DefaultRoute.Table != 254 {
		t.Fatal("metric/table not preserved")
	}
	if p.HostRouteToAdd != nil {
		t.Fatal("in-prefix gateway must not need host route")
	}
}
func TestBuildPlanOutsideGateway(t *testing.T) {
	p, e := BuildPlan(baseState(), netip.MustParsePrefix("198.51.100.20/32"), netip.MustParseAddr("203.0.113.1"))
	if e != nil {
		t.Fatal(e)
	}
	if p.HostRouteToAdd == nil || !p.Target.DefaultRoute.OnLink {
		t.Fatal("outside /32 gateway requires host route and onlink")
	}
}

func TestPlanDropsUnneededOldOnlink(t *testing.T) {
	s := baseState()
	s.DefaultRoute.OnLink = true
	p, e := BuildPlan(s, netip.MustParsePrefix("192.0.2.20/24"), netip.MustParseAddr("192.0.2.1"))
	if e != nil {
		t.Fatal(e)
	}
	if p.Target.DefaultRoute.OnLink {
		t.Fatal("onlink retained despite connected gateway")
	}
}

type fake struct {
	state          network.State
	failReplace    bool
	failDelete     bool
	failAddAt      int
	added, deleted []netip.Prefix
	replaced       []network.Route
}

func (f *fake) Interfaces() ([]string, error) { return []string{"eth0"}, nil }
func (f *fake) DefaultRoutes() ([]network.Route, error) {
	return []network.Route{f.state.DefaultRoute}, nil
}
func (f *fake) Snapshot(string) (network.State, error) { return f.state, nil }
func (f *fake) RouteTo(netip.Addr, string) (network.RouteResult, error) {
	return network.RouteResult{Interface: f.state.Interface, Source: f.state.OutboundSource}, nil
}
func (f *fake) AddAddress(_ string, p netip.Prefix) error {
	if f.failAddAt > 0 && len(f.added)+1 == f.failAddAt {
		return errors.New("add failed")
	}
	f.added = append(f.added, p)
	f.state.Addresses = append(f.state.Addresses, network.Address{Prefix: p})
	return nil
}
func (f *fake) DeleteAddress(_ string, p netip.Prefix) error {
	if f.failDelete {
		return errors.New("delete failed")
	}
	f.deleted = append(f.deleted, p)
	return nil
}

func TestBuildAddressPlanPreservesDefaultRoute(t *testing.T) {
	before := baseState()
	p, e := BuildAddressPlan(before, []netip.Prefix{
		netip.MustParsePrefix("192.0.2.20/24"),
		netip.MustParsePrefix("198.51.100.20/32"),
	})
	if e != nil {
		t.Fatal(e)
	}
	if p.ReplaceDefault || p.Target.OutboundSource != before.OutboundSource || p.Target.DefaultRoute != before.DefaultRoute {
		t.Fatalf("address plan changed default route: %+v", p)
	}
	if len(p.AddedAddresses()) != 2 || len(p.Target.Addresses) != len(before.Addresses)+2 {
		t.Fatalf("unexpected address plan: %+v", p)
	}
}

func TestAddressPlanRollsBackEarlierAddressesWhenLaterAddFails(t *testing.T) {
	p, e := BuildAddressPlan(baseState(), []netip.Prefix{
		netip.MustParsePrefix("192.0.2.20/24"),
		netip.MustParsePrefix("192.0.2.30/24"),
	})
	if e != nil {
		t.Fatal(e)
	}
	f := &fake{state: p.Before, failAddAt: 2}
	tx := Transaction{Backend: f, Plan: p}
	if e = tx.Apply(); e == nil {
		t.Fatal("expected second address failure")
	}
	if len(f.deleted) != 1 || f.deleted[0] != netip.MustParsePrefix("192.0.2.20/24") {
		t.Fatalf("rollback deleted=%v", f.deleted)
	}
}
func (f *fake) ReplaceRoute(r network.Route) error {
	if f.failReplace {
		return errors.New("boom")
	}
	f.replaced = append(f.replaced, r)
	f.state.DefaultRoute = r
	f.state.OutboundSource = r.Source
	return nil
}

func TestRollbackRestoresCompetingRoutes(t *testing.T) {
	before := network.Route{Interface: "eth1", Gateway: netip.MustParseAddr("198.51.100.1"), Table: 254, Metric: 0}
	target := before
	target.Metric = 1
	p := Plan{Before: baseState(), Target: baseState(), RouteChanges: []network.RouteChange{{Before: before, Target: target}}}
	f := &fake{state: p.Before}
	tx := Transaction{Backend: f, Plan: p}
	if err := tx.Apply(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(f.replaced) != 2 || f.replaced[0] != target || f.replaced[1] != before {
		t.Fatalf("replaced routes=%+v", f.replaced)
	}
}
func (f *fake) DeleteRoute(network.Route) error { return nil }
func TestApplyFailureRemovesOnlyAddedAddress(t *testing.T) {
	p, _ := BuildPlan(baseState(), netip.MustParsePrefix("192.0.2.20/24"), netip.MustParseAddr("192.0.2.1"))
	f := &fake{state: p.Before, failReplace: true}
	tx := Transaction{Backend: f, Plan: p}
	if tx.Apply() == nil {
		t.Fatal("expected error")
	}
	if len(f.deleted) != 1 || f.deleted[0].Addr() != netip.MustParseAddr("192.0.2.20") {
		t.Fatalf("rollback deleted=%v", f.deleted)
	}
}

func TestVerifyFailure(t *testing.T) {
	p, _ := BuildPlan(baseState(), netip.MustParsePrefix("192.0.2.20/24"), netip.MustParseAddr("192.0.2.1"))
	f := &fake{state: p.Before}
	tx := Transaction{Backend: f, Plan: p}
	if e := tx.Apply(); e != nil {
		t.Fatal(e)
	}
	f.state.OutboundSource = netip.MustParseAddr("192.0.2.10")
	if tx.Verify() == nil {
		t.Fatal("expected source verification failure")
	}
}

func TestPrimaryAndRollbackErrorsAreBothReported(t *testing.T) {
	p, _ := BuildPlan(baseState(), netip.MustParsePrefix("192.0.2.20/24"), netip.MustParseAddr("192.0.2.1"))
	f := &fake{state: p.Before, failReplace: true, failDelete: true}
	tx := Transaction{Backend: f, Plan: p}
	e := tx.Apply()
	if e == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(e.Error(), "boom") || !strings.Contains(e.Error(), "delete failed") {
		t.Fatalf("combined error=%v", e)
	}
}
