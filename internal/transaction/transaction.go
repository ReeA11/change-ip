package transaction

import (
	"errors"
	"fmt"
	"net/netip"

	"github.com/ReeA11/change-ip/internal/network"
)

type Plan struct {
	Before            network.State         `json:"before"`
	Target            network.State         `json:"target"`
	AddressToAdd      *netip.Prefix         `json:"address_to_add,omitempty"`
	AddressesToAdd    []netip.Prefix        `json:"addresses_to_add,omitempty"`
	HostRouteToAdd    *network.Route        `json:"host_route_to_add,omitempty"`
	ReplaceDefault    bool                  `json:"replace_default"`
	RouteChanges      []network.RouteChange `json:"route_changes,omitempty"`
	RoutesToAdd       []network.Route       `json:"routes_to_add,omitempty"`
	RulesToAdd        []network.Rule        `json:"rules_to_add,omitempty"`
	VerifyGlobal      bool                  `json:"verify_global,omitempty"`
	DisableInterfaces []string              `json:"disable_interfaces,omitempty"`
}

type Transaction struct {
	Backend        network.Backend
	Plan           Plan
	AddressAdded   bool
	AddressesAdded []netip.Prefix
	HostRouteAdded bool
	DefaultChanged bool
	RoutesChanged  int
	RoutesAdded    int
	RulesAdded     int
	Committed      bool
}

func (p Plan) AddedAddresses() []netip.Prefix {
	if len(p.AddressesToAdd) > 0 {
		return p.AddressesToAdd
	}
	if p.AddressToAdd != nil {
		return []netip.Prefix{*p.AddressToAdd}
	}
	return nil
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
	target.Addresses = append([]network.Address(nil), before.Addresses...)
	target.Routes = append([]network.Route(nil), before.Routes...)
	target.OutboundSource = targetPrefix.Addr()
	target.DefaultRoute.Gateway = gateway
	target.DefaultRoute.Source = targetPrefix.Addr()
	target.DefaultRoute.Interface = before.Interface
	if target.DefaultRoute.Table == 0 {
		target.DefaultRoute.Table = 254
	}
	target.DefaultRoute.OnLink = false
	if before.GatewayHostRoute == nil || before.GatewayHostRoute.Destination.Addr() != gateway {
		target.GatewayHostRoute = nil
	}
	plan := Plan{Before: before, Target: target, ReplaceDefault: true}
	if _, ok := network.HasAddress(before, targetPrefix.Addr()); !ok {
		p := targetPrefix
		plan.AddressToAdd = &p
		plan.AddressesToAdd = []netip.Prefix{p}
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
		r := network.Route{Destination: netip.PrefixFrom(gateway, 32), Interface: before.Interface, Table: plan.Target.DefaultRoute.Table, Scope: network.ScopeLink}
		plan.HostRouteToAdd = &r
		plan.Target.GatewayHostRoute = &r
		plan.Target.DefaultRoute.OnLink = true
	}
	return plan, nil
}

// BuildAddressPlan creates an add-only transaction. It deliberately leaves the
// default route and its preferred source untouched.
func BuildAddressPlan(before network.State, prefixes []netip.Prefix) (Plan, error) {
	if len(prefixes) == 0 {
		return Plan{}, fmt.Errorf("at least one IPv4 prefix is required")
	}
	target := before
	target.Addresses = append([]network.Address(nil), before.Addresses...)
	target.Routes = append([]network.Route(nil), before.Routes...)
	seen := make(map[netip.Addr]bool, len(prefixes))
	plan := Plan{Before: before, Target: target}
	for _, prefix := range prefixes {
		if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Bits() < 1 {
			return Plan{}, fmt.Errorf("invalid IPv4 prefix %q", prefix)
		}
		if seen[prefix.Addr()] {
			return Plan{}, fmt.Errorf("duplicate IPv4 address %s", prefix.Addr())
		}
		seen[prefix.Addr()] = true
		if existing, ok := network.HasAddress(before, prefix.Addr()); ok {
			if existing.Prefix.Bits() != prefix.Bits() {
				return Plan{}, fmt.Errorf("%s exists as /%d, not /%d", prefix.Addr(), existing.Prefix.Bits(), prefix.Bits())
			}
			continue
		}
		plan.AddressesToAdd = append(plan.AddressesToAdd, prefix)
		plan.Target.Addresses = append(plan.Target.Addresses, network.Address{Prefix: prefix})
	}
	return plan, nil
}

func (t *Transaction) Apply() error {
	for _, prefix := range t.Plan.AddedAddresses() {
		if e := t.Backend.AddAddress(t.Plan.Before.Interface, prefix); e != nil {
			return t.fail(fmt.Sprintf("add address %s", prefix), e)
		}
		t.AddressAdded = true
		t.AddressesAdded = append(t.AddressesAdded, prefix)
	}
	if t.Plan.HostRouteToAdd != nil {
		if e := t.Backend.ReplaceRoute(*t.Plan.HostRouteToAdd); e != nil {
			return t.fail("add gateway host route", e)
		}
		t.HostRouteAdded = true
	}
	for _, change := range t.Plan.RouteChanges {
		if e := t.Backend.ReplaceRoute(change.Target); e != nil {
			return t.fail("replace competing default route", e)
		}
		t.RoutesChanged++
	}
	if t.Plan.ReplaceDefault {
		if e := t.Backend.ReplaceRoute(t.Plan.Target.DefaultRoute); e != nil {
			return t.fail("replace default route", e)
		}
		t.DefaultChanged = true
	}
	for _, route := range t.Plan.RoutesToAdd {
		if e := t.Backend.ReplaceRoute(route); e != nil {
			return t.fail("add source route", e)
		}
		t.RoutesAdded++
	}
	for _, rule := range t.Plan.RulesToAdd {
		if e := t.Backend.AddRule(rule); e != nil {
			return t.fail("add source rule", e)
		}
		t.RulesAdded++
	}
	return nil
}
func (t *Transaction) Verify() error {
	s, e := t.Backend.Snapshot(t.Plan.Target.Interface)
	if e != nil {
		return e
	}
	for _, prefix := range t.Plan.AddedAddresses() {
		if _, ok := network.HasAddress(s, prefix.Addr()); !ok {
			return fmt.Errorf("target address %s missing", prefix.Addr())
		}
	}
	if !t.Plan.ReplaceDefault {
		if s.OutboundSource != t.Plan.Before.OutboundSource || s.DefaultRoute != t.Plan.Before.DefaultRoute {
			return fmt.Errorf("default route changed during address-only operation")
		}
		return nil
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
	if t.Plan.VerifyGlobal {
		r, err := t.Backend.RouteTo(netip.MustParseAddr("1.1.1.1"), "")
		if err != nil {
			return err
		}
		if r.Interface != t.Plan.Target.Interface || r.Source != src {
			return fmt.Errorf("global default uses %s src %s, expected %s src %s", r.Interface, r.Source, t.Plan.Target.Interface, src)
		}
	}
	if len(t.Plan.Target.ManagedRules) > 0 {
		rules, err := t.Backend.Rules()
		if err != nil {
			return err
		}
		for _, wanted := range t.Plan.Target.ManagedRules {
			found := false
			for _, rule := range rules {
				if rule == wanted {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("source routing for %s is missing", wanted.Source.Addr())
			}
		}
	}
	return nil
}
func (t *Transaction) Rollback() error {
	var errs []error
	for i := t.RulesAdded - 1; i >= 0; i-- {
		if e := t.Backend.DeleteRule(t.Plan.RulesToAdd[i]); e != nil {
			errs = append(errs, fmt.Errorf("delete added source rule: %w", e))
		} else {
			t.RulesAdded--
		}
	}
	for i := t.RoutesAdded - 1; i >= 0; i-- {
		if e := t.Backend.DeleteRoute(t.Plan.RoutesToAdd[i]); e != nil {
			errs = append(errs, fmt.Errorf("delete added source route: %w", e))
		} else {
			t.RoutesAdded--
		}
	}
	if t.DefaultChanged {
		var e error
		if t.Plan.Before.DefaultRoute.Interface == "" {
			e = t.Backend.DeleteRoute(t.Plan.Target.DefaultRoute)
		} else {
			e = t.Backend.ReplaceRoute(t.Plan.Before.DefaultRoute)
		}
		if e != nil {
			errs = append(errs, fmt.Errorf("restore default route: %w", e))
		} else {
			t.DefaultChanged = false
		}
	}
	for i := t.RoutesChanged - 1; i >= 0; i-- {
		if e := t.Backend.ReplaceRoute(t.Plan.RouteChanges[i].Before); e != nil {
			errs = append(errs, fmt.Errorf("restore competing default route: %w", e))
		} else {
			t.RoutesChanged--
		}
	}
	if t.HostRouteAdded && t.Plan.HostRouteToAdd != nil {
		if e := t.Backend.DeleteRoute(*t.Plan.HostRouteToAdd); e != nil {
			errs = append(errs, fmt.Errorf("delete gateway host route: %w", e))
		} else {
			t.HostRouteAdded = false
		}
	}
	added := t.AddressesAdded
	if len(added) == 0 && t.AddressAdded {
		added = t.Plan.AddedAddresses()
	}
	for i := len(added) - 1; i >= 0; i-- {
		if e := t.Backend.DeleteAddress(t.Plan.Before.Interface, added[i]); e != nil {
			errs = append(errs, fmt.Errorf("delete added address %s: %w", added[i], e))
		}
	}
	if len(errs) == 0 {
		t.AddressAdded = false
		t.AddressesAdded = nil
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
