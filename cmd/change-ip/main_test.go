package main

import "testing"

func TestParseCompatibility(t *testing.T) {
	o, e := parseApply([]string{"--dry-run", "192.0.2.20/24", "--gateway", "192.0.2.1", "-i", "eth0"})
	if e != nil {
		t.Fatal(e)
	}
	if !o.DryRun || o.Target != "192.0.2.20/24" || o.Gateway != "192.0.2.1" || o.Interface != "eth0" {
		t.Fatalf("%+v", o)
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
