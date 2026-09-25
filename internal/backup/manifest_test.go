package backup

import (
	"github.com/ReeA11/change-ip/internal/network"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

func TestManifestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifest.json")
	m := Manifest{
		Created:   time.Now().UTC(),
		Status:    "pending",
		Interface: "eth0",
		Before:    network.State{Interface: "eth0"},
		RoutesAdded: []network.Route{{
			Gateway: netip.MustParseAddr("192.0.2.1"), Interface: "eth0", Table: 12020,
		}},
		RulesAdded: []network.Rule{{Source: netip.MustParsePrefix("192.0.2.20/32"), Table: 12020, Priority: 12020}},
	}
	if e := Write(p, m); e != nil {
		t.Fatal(e)
	}
	got, e := Read(p)
	if e != nil {
		t.Fatal(e)
	}
	if got.Version != ManifestVersion || got.Interface != "eth0" {
		t.Fatalf("%+v", got)
	}
	if len(got.RoutesAdded) != 1 || len(got.RulesAdded) != 1 {
		t.Fatalf("routing rollback data missing: %+v", got)
	}
}
func TestRejectArbitraryPath(t *testing.T) {
	if ValidateManagedPath("/etc/passwd") == nil {
		t.Fatal("unsafe path accepted")
	}
}
