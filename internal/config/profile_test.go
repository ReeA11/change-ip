package config

import (
	"strings"
	"testing"
)

func TestParseProfile(t *testing.T) {
	p, e := ParseProfile(strings.NewReader("# x\n176.96.136.246/25 176.96.136.129\n"))
	if e != nil || len(p) != 1 {
		t.Fatalf("%v %v", p, e)
	}
}
func TestProfileRejectsConflict(t *testing.T) {
	_, e := ParseProfile(strings.NewReader("192.0.2.1/24 192.0.2.254\n192.0.2.1/25 192.0.2.129\n"))
	if e == nil {
		t.Fatal("expected conflict")
	}
}
func TestProfileRejectsBadIPv4(t *testing.T) {
	_, e := ParseProfile(strings.NewReader("127.0.0.1/24 192.0.2.1\n"))
	if e == nil {
		t.Fatal("expected unusable address")
	}
}
