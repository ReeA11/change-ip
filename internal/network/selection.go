package network

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

var excludedPrefixes = []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "cbr", "kube", "wg", "tun", "utun", "tap", "ppp", "nerdctl"}

func InterfaceAllowed(name string) bool {
	if name == "" || name == "lo" {
		return false
	}
	for _, prefix := range excludedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

func SelectDefault(routes []Route, explicitInterface string) (Route, error) {
	var candidates []Route
	for _, route := range routes {
		if !route.IsDefault() {
			continue
		}
		if explicitInterface != "" {
			if route.Interface != explicitInterface {
				continue
			}
		} else if !InterfaceAllowed(route.Interface) {
			continue
		}
		candidates = append(candidates, route)
	}
	if len(candidates) == 0 {
		return Route{}, fmt.Errorf("no usable default IPv4 route")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		mi, mj := candidates[i].Metric, candidates[j].Metric
		if mi == 0 {
			mi = -1
		}
		if mj == 0 {
			mj = -1
		}
		if mi != mj {
			return mi < mj
		}
		if candidates[i].Table != candidates[j].Table {
			return candidates[i].Table < candidates[j].Table
		}
		return candidates[i].Interface < candidates[j].Interface
	})
	return candidates[0], nil
}

func GatewayNeedsOnLink(gateway netip.Addr, interfaceRoutes []Route) bool {
	for _, route := range interfaceRoutes {
		if route.Destination.IsValid() && route.Destination.Contains(gateway) && !route.Gateway.IsValid() {
			return false
		}
	}
	return true
}

func HasAddress(state State, addr netip.Addr) (Address, bool) {
	for _, a := range state.Addresses {
		if a.Prefix.Addr() == addr {
			return a, true
		}
	}
	return Address{}, false
}
