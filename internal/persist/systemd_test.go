package persist

import (
	"errors"
	"fmt"
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
	state := network.State{
		Interface:      "eth0",
		OutboundSource: netip.MustParseAddr("192.0.2.20"),
		DefaultRoute:   network.Route{Interface: "eth0", Gateway: netip.MustParseAddr("192.0.2.1"), Source: netip.MustParseAddr("192.0.2.20"), Table: 254, Metric: 10},
		ManagedRules:   []network.Rule{{Source: netip.MustParsePrefix("192.0.2.20/32"), Table: 12020, Priority: 12020}},
	}
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
	if len(got.State.ManagedRules) != 1 || got.State.ManagedRules[0] != state.ManagedRules[0] {
		t.Fatalf("managed rules changed: %+v", got.State.ManagedRules)
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

func TestWriteFollowsOwningNetworkManager(t *testing.T) {
	for _, tc := range []struct {
		manager string
		want    []string
		reject  string
	}{
		{"systemd-networkd.service", []string{"PartOf=systemd-networkd.service", "WantedBy=multi-user.target systemd-networkd.service", "systemd-networkd-wait-online --interface=eth0 --timeout=30"}, "NetworkManager.service"},
		{"NetworkManager.service", []string{"PartOf=NetworkManager.service", "WantedBy=multi-user.target NetworkManager.service", "ExecStartPre=nm-online -q --timeout=30"}, "systemd-networkd.service"},
		{"networking.service", []string{"PartOf=networking.service", "WantedBy=multi-user.target networking.service"}, "ExecStartPre="},
		{"", []string{"WantedBy=multi-user.target"}, "PartOf="},
	} {
		t.Run(tc.manager, func(t *testing.T) {
			root := t.TempDir()
			s := &Systemd{
				BinaryPath: "/usr/local/bin/change-ip",
				ConfigDir:  filepath.Join(root, "lib"),
				UnitDir:    filepath.Join(root, "units"),
				Run:        func(string, ...string) error { return nil },
				ManagerFor: func(string) string {
					return tc.manager
				},
			}
			_, unit, err := s.Write(network.State{Interface: "eth0"})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(unit)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("unit missing %q:\n%s", want, text)
				}
			}
			if tc.reject != "" && strings.Contains(text, tc.reject) {
				t.Errorf("unit unexpectedly contains %q:\n%s", tc.reject, text)
			}
		})
	}
}

func TestMigrateRewritesProfilesAndPreservesDisabledUnits(t *testing.T) {
	root := t.TempDir()
	configDir, unitDir := filepath.Join(root, "lib"), filepath.Join(root, "units")
	old := &Systemd{BinaryPath: "/usr/local/bin/change-ip", ConfigDir: configDir, UnitDir: unitDir, Run: func(string, ...string) error { return nil }}
	for _, iface := range []string{"eth0", "eth1"} {
		if _, _, err := old.Write(network.State{Interface: iface}); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	migrator := &Systemd{
		BinaryPath: "/usr/local/bin/change-ip",
		ConfigDir:  configDir,
		UnitDir:    unitDir,
		ManagerFor: func(string) string {
			return "systemd-networkd.service"
		},
		IsEnabled: func(unit string) bool { return unit == "change-ip-eth0.service" },
		Run: func(name string, args ...string) error {
			calls = append(calls, strings.Join(append([]string{name}, args...), " "))
			return nil
		},
	}
	if err := migrator.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, iface := range []string{"eth0", "eth1"} {
		data, err := os.ReadFile(filepath.Join(unitDir, "change-ip-"+iface+".service"))
		if err != nil || !strings.Contains(string(data), "PartOf=systemd-networkd.service") {
			t.Fatalf("%s was not migrated: %v\n%s", iface, err, data)
		}
	}
	want := []string{"systemctl daemon-reload", "systemctl reenable change-ip-eth0.service"}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
	before, err := os.ReadFile(filepath.Join(unitDir, "change-ip-eth0.service"))
	if err != nil {
		t.Fatal(err)
	}
	if err = migrator.Migrate(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(unitDir, "change-ip-eth0.service"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("migration is not idempotent: %v", err)
	}
}

func TestMigrateRejectsInvalidProfileBeforeReplacingUnit(t *testing.T) {
	root := t.TempDir()
	configDir, unitDir := filepath.Join(root, "lib"), filepath.Join(root, "units")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "apply-eth0.json"), []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(unitDir, "change-ip-eth0.service")
	if err := os.WriteFile(unit, []byte("old unit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Systemd{BinaryPath: "/usr/local/bin/change-ip", ConfigDir: configDir, UnitDir: unitDir, Run: func(string, ...string) error { return nil }}
	if err := s.Migrate(); err == nil {
		t.Fatal("invalid profile was migrated")
	}
	data, err := os.ReadFile(unit)
	if err != nil || string(data) != "old unit\n" {
		t.Fatalf("unit changed after rejected migration: %q, %v", data, err)
	}
}

func TestMigrateRestoresUnitWhenEnableFails(t *testing.T) {
	root := t.TempDir()
	configDir, unitDir := filepath.Join(root, "lib"), filepath.Join(root, "units")
	old := &Systemd{BinaryPath: "/old/change-ip", ConfigDir: configDir, UnitDir: unitDir, Run: func(string, ...string) error { return nil }}
	_, unit, err := old.Write(network.State{Interface: "eth0"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	s := &Systemd{
		BinaryPath: "/usr/local/bin/change-ip",
		ConfigDir:  configDir,
		UnitDir:    unitDir,
		ManagerFor: func(string) string {
			return "systemd-networkd.service"
		},
		IsEnabled: func(string) bool { return true },
		Run: func(_ string, args ...string) error {
			if len(args) > 0 && args[0] == "reenable" {
				return errors.New("enable failed")
			}
			return nil
		},
	}
	if err = s.Migrate(); err == nil {
		t.Fatal("migration succeeded despite enable failure")
	}
	after, readErr := os.ReadFile(unit)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("unit was not restored: %v\n%s", readErr, after)
	}
}
func TestRejectUnsafeInterface(t *testing.T) {
	for _, x := range []string{"../x", "eth0/foo", "", "interface-name-too-long"} {
		if ValidateInterface(x) == nil {
			t.Errorf("accepted %q", x)
		}
	}
}
