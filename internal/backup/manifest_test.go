package backup

import (
	"github.com/ReeA11/change-ip/internal/network"
	"path/filepath"
	"testing"
	"time"
)

func TestManifestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifest.json")
	m := Manifest{Created: time.Now().UTC(), Status: "pending", Interface: "eth0", Before: network.State{Interface: "eth0"}}
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
}
func TestRejectArbitraryPath(t *testing.T) {
	if ValidateManagedPath("/etc/passwd") == nil {
		t.Fatal("unsafe path accepted")
	}
}
