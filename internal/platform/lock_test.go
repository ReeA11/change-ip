//go:build linux

package platform

import (
	"path/filepath"
	"testing"
)

func TestExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	one, e := AcquireLock(path)
	if e != nil {
		t.Fatal(e)
	}
	defer one.Close()
	if _, e = AcquireLock(path); e == nil {
		t.Fatal("second lock succeeded")
	}
}
