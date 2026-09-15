package ui

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/ReeA11/change-ip/internal/app"
	"github.com/ReeA11/change-ip/internal/diagnostics"
	"github.com/ReeA11/change-ip/internal/network"
)

func TestHomeShowsNetworkCIDRsAndPlainTextSelection(t *testing.T) {
	state := network.State{
		Interface:      "eth0",
		OutboundSource: netip.MustParseAddr("192.0.2.10"),
		Addresses: []network.Address{
			{Prefix: netip.MustParsePrefix("192.0.2.10/24")},
			{Prefix: netip.MustParsePrefix("192.0.2.11/32"), Dynamic: true},
		},
		DefaultRoute: network.Route{
			Gateway: netip.MustParseAddr("192.0.2.1"),
			Table:   254,
			Metric:  100,
		},
	}
	u := &UI{app: &app.Application{BackupRoot: t.TempDir()}, version: "3.1.0", color: false}
	got := u.renderHome(diagnostics.Status{
		State:            state,
		Desired:          &state,
		UnitEnabled:      "enabled",
		UnitActive:       "active",
		GatewayReachable: true,
	}, []string{"Change address", "Exit"}, 0)
	for _, want := range []string{
		"ChangeIP 3.1.0",
		"Interface      eth0",
		"Outbound       192.0.2.10",
		"main · metric 100",
		"192.0.2.10/24",
		"current outbound",
		"192.0.2.11/32",
		"dynamic",
		"› Change address",
		"✓ healthy",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("screen does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain screen contains ANSI colors: %q", got)
	}
}

func TestNavigationWraps(t *testing.T) {
	if got := previous(0, 5); got != 4 {
		t.Fatalf("previous = %d", got)
	}
	if got := next(4, 5); got != 0 {
		t.Fatalf("next = %d", got)
	}
}

func TestRouteLabelUsesNamedMainTableAndOnLink(t *testing.T) {
	got := routeLabel(network.Route{Table: 254, Metric: 50, OnLink: true})
	if got != "main · metric 50 · on-link" {
		t.Fatalf("label = %q", got)
	}
}

func TestRussianHomeTranslation(t *testing.T) {
	state := network.State{
		Interface:      "eth0",
		OutboundSource: netip.MustParseAddr("192.0.2.10"),
		Addresses:      []network.Address{{Prefix: netip.MustParsePrefix("192.0.2.10/24")}},
		DefaultRoute: network.Route{
			Gateway: netip.MustParseAddr("192.0.2.1"),
			Table:   254,
			Metric:  100,
		},
	}
	u := &UI{app: &app.Application{BackupRoot: t.TempDir()}, version: "3.1.0", language: russian}
	got := u.renderHome(diagnostics.Status{
		State:            state,
		Desired:          &state,
		UnitEnabled:      "enabled",
		UnitActive:       "active",
		GatewayReachable: true,
	}, []string{"Сменить адрес", "Язык · Русский"}, 1)
	for _, want := range []string{"Сеть", "Интерфейс", "Исходящий IP", "Шлюз", "IPv4-адреса", "текущий исходящий", "Автозагрузка", "Состояние", "Язык · Русский"} {
		if !strings.Contains(got, want) {
			t.Errorf("Russian screen does not contain %q:\n%s", want, got)
		}
	}
}
