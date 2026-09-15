package transaction

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/ReeA11/change-ip/internal/network"
)

type Plan struct {
	Before         network.State  `json:"before"`
	Target         network.State  `json:"target"`
	AddressToAdd   *netip.Prefix  `json:"address_to_add,omitempty"`
	HostRouteToAdd *network.Route `json:"host_route_to_add,omitempty"`
}

type Transaction struct {
	Backend        network.Backend
	Plan           Plan
	AddressAdded   bool
	HostRouteAdded bool
	DefaultChanged bool
	Committed      bool
}

func BuildPlan(before network.State, targetPrefix netip.Prefix, gateway netip.Addr) (Plan, error) {
	if !targetPrefix.IsValid() || !targetPrefix.Addr().Is4() || targetPrefix.Bits() < 1 {
		return Plan{}, fmt.Errorf("invalid target IPv4 prefix")
	}
	if !gateway.Is4() || gateway == targetPrefix.Addr() {
		return Plan{}, fmt.Errorf("invalid gateway")
	}
	if existing, ok := network.HasAddress(before, targetPrefix.Addr()); ok {
		if existing.Prefix.Bits() != targetPrefix.Bits() {
			return Plan{}, fmt.Errorf("%s exists as /%d, not /%d", targetPrefix.Addr(), existing.Prefix.Bits(), targetPrefix.Bits())
		}
	}
	target := before
	target.OutboundSource = targetPrefix.Addr()
	target.DefaultRoute.Gateway = gateway
	target.DefaultRoute.Source = targetPrefix.Addr()
	target.DefaultRoute.OnLink = false
	if before.GatewayHostRoute == nil || before.GatewayHostRoute.Destination.Addr() != gateway {
		target.GatewayHostRoute = nil
	}
	plan := Plan{Before: before, Target: target}
	if _, ok := network.HasAddress(before, targetPrefix.Addr()); !ok {
		p := targetPrefix
		plan.AddressToAdd = &p
		target.Addresses = append(target.Addresses, network.Address{Prefix: p})
		plan.Target = target
	}
	covered := false
	for _, a := range target.Addresses {
		if a.Prefix.Contains(gateway) && a.Prefix.Bits() != 32 {
			covered = true
		}
	}
	for _, r := range before.Routes {
		if r.Destination.IsValid() && r.Destination.Contains(gateway) && !r.Gateway.IsValid() {
			covered = true
		}
	}
	if !covered && (before.GatewayHostRoute == nil || before.GatewayHostRoute.Destination.Addr() != gateway) {
		r := network.Route{Destination: netip.PrefixFrom(gateway, 32), Interface: before.Interface, Table: before.DefaultRoute.Table, Scope: network.ScopeLink}
		plan.HostRouteToAdd = &r
		plan.Target.GatewayHostRoute = &r
		plan.Target.DefaultRoute.OnLink = true
	}
	return plan, nil
}

func (t *Transaction) Apply() error {
	if t.Plan.AddressToAdd != nil {
		if e := t.Backend.AddAddress(t.Plan.Before.Interface, *t.Plan.AddressToAdd); e != nil {
			return fmt.Errorf("add address: %w", e)
		}
		t.AddressAdded = true
	}
	if t.Plan.HostRouteToAdd != nil {
		if e := t.Backend.ReplaceRoute(*t.Plan.HostRouteToAdd); e != nil {
			return t.fail("add gateway host route", e)
		}
		t.HostRouteAdded = true
	}
	if e := t.Backend.ReplaceRoute(t.Plan.Target.DefaultRoute); e != nil {
		return t.fail("replace default route", e)
	}
	t.DefaultChanged = true
	return nil
}
func (t *Transaction) Verify() error {
	s, e := t.Backend.Snapshot(t.Plan.Target.Interface)
	if e != nil {
		return e
	}
	src := t.Plan.Target.OutboundSource
	if _, ok := network.HasAddress(s, src); !ok {
		return fmt.Errorf("target address missing")
	}
	r, e := t.Backend.RouteTo(netip.MustParseAddr("1.1.1.1"), s.Interface)
	if e != nil {
		return e
	}
	if r.Interface != s.Interface || r.Source != src {
		return fmt.Errorf("route source is %s on %s, expected %s on %s", r.Source, r.Interface, src, s.Interface)
	}
	if _, err := t.Backend.RouteTo(t.Plan.Target.DefaultRoute.Gateway, s.Interface); err != nil {
		return fmt.Errorf("gateway route: %w", err)
	}
	if t.Plan.HostRouteToAdd != nil && s.GatewayHostRoute == nil {
		return fmt.Errorf("gateway host route is missing")
	}
	if s.DefaultRoute.Gateway != t.Plan.Target.DefaultRoute.Gateway || s.DefaultRoute.Source != src || s.DefaultRoute.Table != t.Plan.Target.DefaultRoute.Table || s.DefaultRoute.Metric != t.Plan.Target.DefaultRoute.Metric {
		return fmt.Errorf("default route differs from target")
	}
	return nil
}
func (t *Transaction) Rollback() error {
	var errs []error
	if t.DefaultChanged {
		if e := t.Backend.ReplaceRoute(t.Plan.Before.DefaultRoute); e != nil {
			errs = append(errs, fmt.Errorf("restore default route: %w", e))
		} else {
			t.DefaultChanged = false
		}
	}
	if t.HostRouteAdded && t.Plan.HostRouteToAdd != nil {
		if e := t.Backend.DeleteRoute(*t.Plan.HostRouteToAdd); e != nil {
			errs = append(errs, fmt.Errorf("delete gateway host route: %w", e))
		} else {
			t.HostRouteAdded = false
		}
	}
	if t.AddressAdded && t.Plan.AddressToAdd != nil {
		if e := t.Backend.DeleteAddress(t.Plan.Before.Interface, *t.Plan.AddressToAdd); e != nil {
			errs = append(errs, fmt.Errorf("delete added address: %w", e))
		} else {
			t.AddressAdded = false
		}
	}
	return errors.Join(errs...)
}
func (t *Transaction) fail(op string, e error) error {
	rb := t.Rollback()
	if rb != nil {
		return errors.Join(fmt.Errorf("%s: %w", op, e), fmt.Errorf("rollback: %w", rb))
	}
	return fmt.Errorf("%s: %w", op, e)
}
