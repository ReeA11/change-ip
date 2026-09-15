//go:build linux

package platform

import (
	"fmt"
	"os"
	"syscall"
)

type Lock struct{ file *os.File }

func AcquireLock(path string) (*Lock, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		if e == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("another ChangeIP operation is running")
		}
		return nil, e
	}
	return &Lock{file: f}, nil
}
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}
