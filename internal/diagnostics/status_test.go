package diagnostics

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/ReeA11/change-ip/internal/network"
)

func TestProblemsReportMissingReturnPathAndDuplicateAddress(t *testing.T) {
	rule := network.Rule{Source: netip.MustParsePrefix("104.167.197.74/32"), Table: 1074, Priority: 1074}
	desired := network.State{Interface: "eth1", OutboundSource: netip.MustParseAddr("104.167.197.74"), Addresses: []network.Address{{Prefix: netip.MustParsePrefix("104.167.197.74/24")}}, ManagedRules: []network.Rule{rule}}
	status := Status{
		State:            desired,
		Desired:          &desired,
		UnitEnabled:      "enabled",
		UnitActive:       "active",
		AddressConflicts: []string{"IP 104.167.197.74 is configured on multiple interfaces: eth0, eth1"},
	}
	problems := strings.Join(Problems(status), "\n")
	if !strings.Contains(problems, "return path for IP 104.167.197.74 is missing") {
		t.Fatalf("missing source policy was not reported: %s", problems)
	}
	if !strings.Contains(problems, "multiple interfaces") {
		t.Fatalf("duplicate address was not reported: %s", problems)
	}
}

func TestProblemsAcceptConfiguredReturnPath(t *testing.T) {
	rule := network.Rule{Source: netip.MustParsePrefix("104.167.197.74/32"), Table: 1074, Priority: 1074}
	desired := network.State{Interface: "eth1", OutboundSource: netip.MustParseAddr("104.167.197.74"), Addresses: []network.Address{{Prefix: netip.MustParsePrefix("104.167.197.74/24")}}, ManagedRules: []network.Rule{rule}}
	status := Status{State: desired, Desired: &desired, Rules: []network.Rule{rule}, UnitEnabled: "enabled", UnitActive: "active"}
	for _, problem := range Problems(status) {
		if strings.Contains(problem, "return path") {
			t.Fatalf("configured source policy reported as missing: %s", problem)
		}
	}
}
