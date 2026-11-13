package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseCompatibility(t *testing.T) {
	o, e := parseApply([]string{"--dry-run", "192.0.2.20/24", "--gateway", "192.0.2.1", "-i", "eth0"})
	if e != nil {
		t.Fatal(e)
	}
	if !o.DryRun || o.Target != "192.0.2.20/24" || o.Gateway != "192.0.2.1" || o.Interface != "eth0" {
		t.Fatalf("%+v", o)
	}
}

func TestRunUpdatedMigrationExecutesInstalledBinary(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	binary := filepath.Join(dir, "change-ip")
	script := "#!/bin/sh\nprintf '%s:%s' \"$CHANGE_IP_LOCK_HELD\" \"$1\" > \"" + marker + "\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runUpdatedMigration(binary); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(data)) != "1:migrate-persistence" {
		t.Fatalf("updated binary was not used: %q, %v", data, err)
	}
}
func TestParseLegacyPositionalInterface(t *testing.T) {
	o, e := parseApply([]string{"192.0.2.20/24", "eth0"})
	if e != nil || o.Interface != "eth0" {
		t.Fatalf("%+v %v", o, e)
	}
}

func TestParseApplySubcommandArguments(t *testing.T) {
	o, e := parseApply([]string{"192.0.2.20/24", "--runtime-only", "--yes"})
	if e != nil || o.Target != "192.0.2.20/24" || !o.RuntimeOnly || !o.Yes {
		t.Fatalf("%+v %v", o, e)
	}
}

func TestSystemdRunIsQuiet(t *testing.T) {
	t.Setenv("SUDO_USER", "alice")
	got := systemdRunArgs("/usr/local/bin/change-ip", []string{"192.0.2.20/24", "--yes"})
	if !slices.Contains(got, "--quiet") || !slices.Contains(got, "--wait") || !slices.Contains(got, "--pty") {
		t.Fatalf("systemd-run arguments = %v", got)
	}
	if got[len(got)-2] != "192.0.2.20/24" || got[len(got)-1] != "--yes" {
		t.Fatalf("ChangeIP arguments were not preserved: %v", got)
	}
	if !slices.Contains(got, "--setenv=SUDO_USER=alice") {
		t.Fatalf("invoking user was not preserved: %v", got)
	}
}

func TestParseManagementOperation(t *testing.T) {
	o, positional, err := parseOperation([]string{"--dry-run", "--interface", "eth1", "192.0.2.20/24", "192.0.2.21/24"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.DryRun || o.Interface != "eth1" || !slices.Equal(positional, []string{"192.0.2.20/24", "192.0.2.21/24"}) {
		t.Fatalf("options=%+v positional=%v", o, positional)
	}
}
