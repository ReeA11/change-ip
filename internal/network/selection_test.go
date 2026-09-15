package network

import (
	"net/netip"
	"testing"
)

func TestInterfaceFiltering(t *testing.T) {
	for _, n := range []string{"lo", "docker0", "br-x", "veth1", "wg0", "tun0", "cni0"} {
		if InterfaceAllowed(n) {
			t.Errorf("%s allowed", n)
		}
	}
	for _, n := range []string{"eth0", "enp1s0", "bond0", "vlan10"} {
		if !InterfaceAllowed(n) {
			t.Errorf("%s rejected", n)
		}
	}
}
func TestSelectDefault(t *testing.T) {
	rs := []Route{{Interface: "docker0", Metric: 0}, {Interface: "eth1", Metric: 200, Table: 100}, {Interface: "eth0", Metric: 100, Table: 254}}
	r, e := SelectDefault(rs, "")
	if e != nil || r.Interface != "eth0" {
		t.Fatalf("got %+v %v", r, e)
	}
}
func TestGatewayOnlinkDecision(t *testing.T) {
	gw := netip.MustParseAddr("192.0.2.1")
	if GatewayNeedsOnLink(gw, []Route{{Destination: netip.MustParsePrefix("192.0.2.0/24"), Interface: "eth0"}}) {
		t.Fatal("connected gateway marked onlink")
	}
	if !GatewayNeedsOnLink(netip.MustParseAddr("203.0.113.1"), nil) {
		t.Fatal("outside gateway not marked onlink")
	}
}
