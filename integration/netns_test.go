//go:build integration

package integration

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ReeA11/change-ip/internal/app"
	"github.com/ReeA11/change-ip/internal/network"
	"github.com/ReeA11/change-ip/internal/transaction"
)

func ip(t *testing.T, args ...string) {
	t.Helper()
	b, e := exec.Command("ip", args...).CombinedOutput()
	if e != nil {
		t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), e, b)
	}
}
func TestNetworkNamespaceTransactions(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root/CAP_NET_ADMIN")
	}
	if _, e := exec.LookPath("ip"); e != nil {
		t.Skip("iproute2 required")
	}
	cases := []string{"existing", "new", "onlink32", "nonmain-multiple-defaults"}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			suffix := fmt.Sprint(time.Now().UnixNano() % 100000)
			ns := "cip" + suffix
			host := "ch" + suffix
			peer := "cn" + suffix
			ip(t, "netns", "add", ns)
			defer exec.Command("ip", "netns", "del", ns).Run()
			ip(t, "link", "add", host, "type", "veth", "peer", "name", peer)
			defer exec.Command("ip", "link", "del", host).Run()
			ip(t, "link", "set", peer, "netns", ns)
			ip(t, "addr", "add", "192.0.2.1/24", "dev", host)
			ip(t, "addr", "add", "203.0.113.1/32", "dev", host)
			ip(t, "link", "set", host, "up")
			ip(t, "-n", ns, "link", "set", "lo", "up")
			ip(t, "-n", ns, "addr", "add", "192.0.2.10/24", "dev", peer)
			ip(t, "-n", ns, "link", "set", peer, "up")
			if tc == "nonmain-multiple-defaults" {
				ip(t, "-n", ns, "route", "add", "default", "via", "192.0.2.1", "dev", peer, "src", "192.0.2.10", "metric", "200")
				ip(t, "-n", ns, "route", "add", "table", "100", "192.0.2.0/24", "dev", peer, "scope", "link")
				ip(t, "-n", ns, "route", "add", "table", "100", "default", "via", "192.0.2.1", "dev", peer, "src", "192.0.2.10", "metric", "77")
				ip(t, "-n", ns, "rule", "add", "priority", "100", "to", "1.1.1.1/32", "table", "100")
			} else {
				ip(t, "-n", ns, "route", "add", "default", "via", "192.0.2.1", "dev", peer, "src", "192.0.2.10", "metric", "77")
			}
			if tc == "existing" {
				ip(t, "-n", ns, "addr", "add", "192.0.2.20/24", "dev", peer)
			}
			self, _ := os.Executable()
			cmd := exec.Command("ip", "netns", "exec", ns, self, "-test.run=TestNetNSHelper")
			cmd.Env = append(os.Environ(), "CHANGEIP_HELPER=1", "CHANGEIP_CASE="+tc, "CHANGEIP_IFACE="+peer)
			if b, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("helper: %v\n%s", e, b)
			}
		})
	}
}
func TestNetNSHelper(t *testing.T) {
	if os.Getenv("CHANGEIP_HELPER") != "1" {
		return
	}
	iface := os.Getenv("CHANGEIP_IFACE")
	b := network.NewNetlinkBackend()
	before, e := b.Snapshot(iface)
	if e != nil {
		t.Fatal(e)
	}
	if os.Getenv("CHANGEIP_CASE") == "nonmain-multiple-defaults" && before.DefaultRoute.Table != 100 {
		t.Fatalf("selected table %d, expected 100", before.DefaultRoute.Table)
	}
	target := netip.MustParsePrefix("192.0.2.20/24")
	gw := netip.MustParseAddr("192.0.2.1")
	if os.Getenv("CHANGEIP_CASE") == "onlink32" {
		target = netip.MustParsePrefix("198.51.100.20/32")
		gw = netip.MustParseAddr("203.0.113.1")
	}
	p, e := transaction.BuildPlan(before, target, gw)
	if e != nil {
		t.Fatal(e)
	}
	tx := transaction.Transaction{Backend: b, Plan: p}
	if e = tx.Apply(); e != nil {
		t.Fatal(e)
	}
	if e = tx.Verify(); e != nil {
		t.Fatal(e)
	}
	after, e := b.Snapshot(iface)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := network.HasAddress(after, netip.MustParseAddr("192.0.2.10")); !ok {
		t.Fatal("old address removed")
	}
	// Repeated apply must be harmless and must not claim ownership of an
	// address created by the first transaction.
	repeatPlan, e := transaction.BuildPlan(after, target, gw)
	if e != nil {
		t.Fatal(e)
	}
	repeat := transaction.Transaction{Backend: b, Plan: repeatPlan}
	if e = repeat.Apply(); e != nil {
		t.Fatal(e)
	}
	if repeat.AddressAdded {
		t.Fatal("repeated apply claimed existing address")
	}
	if e = repeat.Verify(); e != nil {
		t.Fatal(e)
	}
	if e = repeat.Rollback(); e != nil {
		t.Fatal(e)
	}
	// Dry-run resolves and plans against real netlink state without mutation.
	dry := app.New(b, "/usr/local/sbin/change-ip")
	dry.Out = os.Stdout
	dry.Err = os.Stderr
	if _, e = dry.Apply(app.Options{Target: target.String(), Gateway: gw.String(), Interface: iface, DryRun: true, Yes: true}, nil); e != nil {
		t.Fatal(e)
	}
	dryState, e := b.Snapshot(iface)
	if e != nil {
		t.Fatal(e)
	}
	if len(dryState.Addresses) != len(after.Addresses) {
		t.Fatal("dry-run changed addresses")
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	restored, e := b.Snapshot(iface)
	if e != nil {
		t.Fatal(e)
	}
	if restored.DefaultRoute.Source != before.DefaultRoute.Source || restored.DefaultRoute.Metric != 77 {
		t.Fatalf("rollback route=%+v", restored.DefaultRoute)
	}
	if p.AddressToAdd != nil {
		if _, ok := network.HasAddress(restored, target.Addr()); ok {
			t.Fatal("transaction-added address survived rollback")
		}
	}
	// Simulate the netlink body of the systemd oneshot after a reboot/network
	// manager reset, then run it twice to prove idempotence.
	bootApp := app.New(b, "/usr/local/sbin/change-ip")
	if e = bootApp.ApplyDesiredState(restored, p.Target); e != nil {
		t.Fatal(e)
	}
	bootState, e := b.Snapshot(iface)
	if e != nil {
		t.Fatal(e)
	}
	if bootState.OutboundSource != target.Addr() {
		t.Fatalf("boot source=%s", bootState.OutboundSource)
	}
	if e = bootApp.ApplyDesiredState(bootState, p.Target); e != nil {
		t.Fatal(e)
	}
}
