package persist

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReeA11/change-ip/internal/network"
)

func TestWriteAndLoad(t *testing.T) {
	root := t.TempDir()
	s := &Systemd{BinaryPath: "/usr/local/sbin/change-ip", ConfigDir: filepath.Join(root, "lib"), UnitDir: filepath.Join(root, "units"), Run: func(string, ...string) error { return nil }}
	state := network.State{Interface: "eth0", OutboundSource: netip.MustParseAddr("192.0.2.20"), DefaultRoute: network.Route{Interface: "eth0", Gateway: netip.MustParseAddr("192.0.2.1"), Source: netip.MustParseAddr("192.0.2.20"), Table: 254, Metric: 10}}
	cfg, unit, e := s.Write(state)
	if e != nil {
		t.Fatal(e)
	}
	got, e := Load(cfg)
	if e != nil {
		t.Fatal(e)
	}
	if got.State.OutboundSource != state.OutboundSource {
		t.Fatal("state changed")
	}
	b, _ := os.ReadFile(unit)
	if strings.Contains(string(b), "/bin/sh") || !strings.Contains(string(b), "apply-profile --config") {
		t.Fatalf("unexpected unit: %s", b)
	}
	st, _ := os.Stat(cfg)
	if st.Mode().Perm() != 0600 {
		t.Fatalf("config mode %o", st.Mode().Perm())
	}
}
func TestRejectUnsafeInterface(t *testing.T) {
	for _, x := range []string{"../x", "eth0/foo", "", "interface-name-too-long"} {
		if ValidateInterface(x) == nil {
			t.Errorf("accepted %q", x)
		}
	}
}
