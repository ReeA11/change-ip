package app

import (
	"fmt"
	"net/netip"
	"sort"

	"github.com/ReeA11/change-ip/internal/config"
	"github.com/ReeA11/change-ip/internal/network"
	"github.com/ReeA11/change-ip/internal/transaction"
)

const (
	policyTableFirst    = 10000
	policyTableLast     = 59999
	policyPriorityFirst = 10000
	policyPriorityLast  = 29999
)

func (a *Application) interfaceStates() ([]network.State, error) {
	names, err := a.Net.Interfaces()
	if err != nil {
		return nil, err
	}
	states := make([]network.State, 0, len(names))
	for _, name := range names {
		if !network.InterfaceAllowed(name) {
			continue
		}
		state, snapshotErr := a.Net.Snapshot(name)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		states = append(states, state)
	}
	return states, nil
}

func duplicateAddressError(states []network.State) error {
	owners := make(map[netip.Addr][]string)
	for _, state := range states {
		for _, address := range state.Addresses {
			ip := address.Prefix.Addr()
			if !config.UsableIPv4(ip) {
				continue
			}
			owners[ip] = append(owners[ip], state.Interface)
		}
	}
	var conflicts []string
	for ip, interfaces := range owners {
		if len(interfaces) < 2 {
			continue
		}
		sort.Strings(interfaces)
		conflicts = append(conflicts, fmt.Sprintf("IP %s is configured on more than one interface (%s); keep it only on the provider-assigned interface", ip, joinNames(interfaces)))
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("%s", conflicts[0])
	}
	return nil
}

func (a *Application) validateAddressOwnership(target network.State) error {
	states, err := a.interfaceStates()
	if err != nil {
		return err
	}
	for i := range states {
		if states[i].Interface == target.Interface {
			states[i] = target
		}
	}
	return duplicateAddressError(states)
}

func (a *Application) addressOwner(ip netip.Addr, except string) (network.Address, string, bool, error) {
	states, err := a.interfaceStates()
	if err != nil {
		return network.Address{}, "", false, err
	}
	for _, state := range states {
		if state.Interface == except {
			continue
		}
		if address, ok := network.HasAddress(state, ip); ok {
			return address, state.Interface, true, nil
		}
	}
	return network.Address{}, "", false, nil
}

func (a *Application) activeDefaultRoute() (network.Route, []network.Route, error) {
	routes, err := a.Net.DefaultRoutes()
	if err != nil {
		return network.Route{}, nil, err
	}
	if routed, routeErr := a.Net.RouteTo(netip.MustParseAddr("1.1.1.1"), ""); routeErr == nil {
		for _, route := range routes {
			if !route.IsDefault() || route.Interface != routed.Interface {
				continue
			}
			if routed.Table != 0 && route.Table != routed.Table {
				continue
			}
			if routed.Gateway.IsValid() && route.Gateway != routed.Gateway {
				continue
			}
			return route, routes, nil
		}
	}
	active, err := network.SelectDefault(routes, "")
	return active, routes, err
}

func prepareDefaultSwitch(plan *transaction.Plan, active network.Route, routes []network.Route) {
	if plan.Before.DefaultRoute.Interface == "" {
		plan.Target.DefaultRoute.Table = active.Table
		if plan.Target.DefaultRoute.Table == 0 {
			plan.Target.DefaultRoute.Table = 254
		}
		covered := false
		for _, route := range plan.Before.Routes {
			if route.Table == plan.Target.DefaultRoute.Table && route.Destination.IsValid() && route.Destination.Contains(plan.Target.DefaultRoute.Gateway) && !route.Gateway.IsValid() {
				covered = true
				break
			}
		}
		if !covered && plan.HostRouteToAdd == nil {
			hostRoute := network.Route{Destination: netip.PrefixFrom(plan.Target.DefaultRoute.Gateway, 32), Interface: plan.Target.Interface, Table: plan.Target.DefaultRoute.Table, Scope: network.ScopeLink}
			plan.HostRouteToAdd = &hostRoute
			plan.Target.GatewayHostRoute = &hostRoute
			plan.Target.DefaultRoute.OnLink = true
		} else if plan.HostRouteToAdd != nil {
			plan.HostRouteToAdd.Table = plan.Target.DefaultRoute.Table
			plan.Target.GatewayHostRoute = plan.HostRouteToAdd
		}
	}
	if active.Interface != plan.Target.Interface {
		if active.Metric > 0 {
			plan.Target.DefaultRoute.Metric = active.Metric - 1
		} else {
			plan.Target.DefaultRoute.Metric = 0
			for _, route := range routes {
				if route.Interface == plan.Target.Interface || route.Table != active.Table || route.Metric != 0 {
					continue
				}
				demoted := route
				demoted.Metric = 1
				plan.RouteChanges = appendRouteChangeOnce(plan.RouteChanges, network.RouteChange{Before: route, Target: demoted})
				plan.Target.ManagedRoutes = appendRouteOnce(plan.Target.ManagedRoutes, demoted)
			}
		}
	}
	seenInterfaces := make(map[string]bool)
	for _, route := range routes {
		if route.Interface == plan.Target.Interface || !route.IsDefault() || !network.InterfaceAllowed(route.Interface) || seenInterfaces[route.Interface] {
			continue
		}
		seenInterfaces[route.Interface] = true
		plan.DisableInterfaces = append(plan.DisableInterfaces, route.Interface)
	}
	plan.VerifyGlobal = true
}

func joinNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	out := names[0]
	for _, name := range names[1:] {
		out += ", " + name
	}
	return out
}

func routeKeyEqual(a, b network.Route) bool {
	return a.Interface == b.Interface && a.Table == b.Table && a.Destination == b.Destination
}

func ruleEqual(a, b network.Rule) bool {
	return a.Source == b.Source && a.Table == b.Table && a.Priority == b.Priority
}

func findRoute(states []network.State, wanted network.Route) (network.Route, bool) {
	for _, state := range states {
		for _, route := range state.Routes {
			if routeKeyEqual(route, wanted) {
				return route, true
			}
		}
	}
	return network.Route{}, false
}

func routeGatewayForTable(state network.State, table int) netip.Addr {
	for _, route := range state.Routes {
		if route.Table == table && route.IsDefault() && route.Gateway.IsValid() {
			return route.Gateway
		}
	}
	return netip.Addr{}
}

func gatewayForState(state network.State, selected network.State, fallback netip.Addr, rules []network.Rule) netip.Addr {
	if state.Interface == selected.Interface && selected.DefaultRoute.Gateway.IsValid() {
		return selected.DefaultRoute.Gateway
	}
	if state.DefaultRoute.Gateway.IsValid() {
		return state.DefaultRoute.Gateway
	}
	for _, rule := range rules {
		for _, address := range state.Addresses {
			if rule.Source.Addr() == address.Prefix.Addr() {
				if gateway := routeGatewayForTable(state, rule.Table); gateway.IsValid() {
					return gateway
				}
			}
		}
	}
	if fallback.IsValid() {
		for _, address := range state.Addresses {
			if address.Prefix.Bits() < 32 && address.Prefix.Contains(fallback) {
				return fallback
			}
		}
	}
	return netip.Addr{}
}

func policyTableSeed(ip netip.Addr) int {
	b := ip.As4()
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return policyTableFirst + int(n%uint32(policyTableLast-policyTableFirst+1))
}

func allocatePolicyTable(ip netip.Addr, usedTables, usedPriorities map[int]bool) (int, int, error) {
	table := policyTableSeed(ip)
	tableFound := false
	for attempts := 0; attempts <= policyTableLast-policyTableFirst; attempts++ {
		if !usedTables[table] {
			usedTables[table] = true
			tableFound = true
			break
		}
		table++
		if table > policyTableLast {
			table = policyTableFirst
		}
	}
	if !tableFound {
		return 0, 0, fmt.Errorf("no free routing table is available")
	}
	b := ip.As4()
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	priority := policyPriorityFirst + int(n%uint32(policyPriorityLast-policyPriorityFirst+1))
	for attempts := 0; attempts <= policyPriorityLast-policyPriorityFirst; attempts++ {
		if !usedPriorities[priority] {
			usedPriorities[priority] = true
			return table, priority, nil
		}
		priority++
		if priority > policyPriorityLast {
			priority = policyPriorityFirst
		}
	}
	return 0, 0, fmt.Errorf("no free source-rule priority is available")
}

func linkRouteFor(state network.State, source, gateway netip.Addr, table int) network.Route {
	destination := netip.PrefixFrom(gateway, 32)
	for _, address := range state.Addresses {
		if address.Prefix.Bits() < 32 && address.Prefix.Contains(gateway) {
			destination = address.Prefix.Masked()
			break
		}
	}
	return network.Route{
		Destination: destination,
		Source:      source,
		Interface:   state.Interface,
		Table:       table,
		Scope:       network.ScopeLink,
	}
}

// attachSourcePolicies makes every configured address reply through the
// interface that owns it. Users should not have to create tables and rules by
// hand when their provider supplies one public IP per virtual NIC.
func (a *Application) attachSourcePolicies(plan *transaction.Plan, fallbackGateway netip.Addr) error {
	states, err := a.interfaceStates()
	if err != nil {
		return err
	}
	for i := range states {
		if states[i].Interface == plan.Target.Interface {
			states[i] = plan.Target
		}
	}
	if err = duplicateAddressError(states); err != nil {
		return err
	}
	rules, err := a.Net.Rules()
	if err != nil {
		return err
	}
	usedTables := map[int]bool{253: true, 254: true, 255: true}
	usedPriorities := map[int]bool{0: true, 32766: true, 32767: true}
	for _, rule := range rules {
		usedTables[rule.Table] = true
		usedPriorities[rule.Priority] = true
	}
	for _, state := range states {
		for _, route := range state.Routes {
			usedTables[route.Table] = true
		}
	}

	for _, state := range states {
		gateway := gatewayForState(state, plan.Target, fallbackGateway, rules)
		if !gateway.IsValid() {
			continue
		}
		for _, address := range state.Addresses {
			ip := address.Prefix.Addr()
			if !config.UsableIPv4(ip) {
				continue
			}
			source := netip.PrefixFrom(ip, 32)
			var policyRule network.Rule
			for _, existing := range rules {
				if existing.Source == source && existing.Table != 253 && existing.Table != 254 && existing.Table != 255 {
					policyRule = existing
					break
				}
			}
			if !policyRule.Source.IsValid() {
				table, priority, allocateErr := allocatePolicyTable(ip, usedTables, usedPriorities)
				if allocateErr != nil {
					return allocateErr
				}
				policyRule = network.Rule{Source: source, Table: table, Priority: priority}
				plan.RulesToAdd = append(plan.RulesToAdd, policyRule)
				rules = append(rules, policyRule)
			}
			plan.Target.ManagedRules = appendRuleOnce(plan.Target.ManagedRules, policyRule)

			policyRoutes := []network.Route{
				linkRouteFor(state, ip, gateway, policyRule.Table),
				{Gateway: gateway, Source: ip, Interface: state.Interface, Table: policyRule.Table},
			}
			for _, wanted := range policyRoutes {
				plan.Target.ManagedRoutes = appendRouteOnce(plan.Target.ManagedRoutes, wanted)
				if existing, ok := findRoute(states, wanted); ok {
					if existing != wanted {
						plan.RouteChanges = appendRouteChangeOnce(plan.RouteChanges, network.RouteChange{Before: existing, Target: wanted})
					}
					continue
				}
				plan.RoutesToAdd = appendRouteOnce(plan.RoutesToAdd, wanted)
			}
		}
	}
	return nil
}

func appendRouteOnce(routes []network.Route, route network.Route) []network.Route {
	for _, existing := range routes {
		if existing == route {
			return routes
		}
	}
	return append(routes, route)
}

func appendRuleOnce(rules []network.Rule, rule network.Rule) []network.Rule {
	for _, existing := range rules {
		if ruleEqual(existing, rule) {
			return rules
		}
	}
	return append(rules, rule)
}

func appendRouteChangeOnce(changes []network.RouteChange, change network.RouteChange) []network.RouteChange {
	for _, existing := range changes {
		if existing == change {
			return changes
		}
	}
	return append(changes, change)
}
