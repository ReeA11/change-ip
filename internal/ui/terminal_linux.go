//go:build linux

package ui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type terminal struct {
	in, out *os.File
	old     *unix.Termios
	keys    chan key
	drawn   bool
}

type key int

const (
	keyUnknown key = iota
	keyUp
	keyDown
	keyEnter
	keyEscape
	keyQuit
	keyInterrupt
	keyBackspace
)

func isTTY(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

func openTerminal(in, out *os.File) (*terminal, error) {
	fd := int(in.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, fmt.Errorf("read terminal mode: %w", err)
	}
	raw := *old
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return nil, fmt.Errorf("enable terminal raw mode: %w", err)
	}
	t := &terminal{in: in, out: out, old: old, keys: make(chan key, 8)}
	fmt.Fprint(out, "\x1b[?25l")
	go t.readKeys()
	return t, nil
}

func (t *terminal) close() {
	_ = unix.IoctlSetTermios(int(t.in.Fd()), unix.TCSETS, t.old)
	fmt.Fprint(t.out, "\x1b[?25h\x1b[0m\n")
}

func (t *terminal) draw(content string) {
	content = strings.TrimRight(content, "\n") + "\n"
	if !t.drawn {
		// Clear the previous shell output once. Further updates rewrite only the
		// currently rendered CLI block instead of clearing the whole terminal.
		fmt.Fprint(t.out, "\x1b[2J\x1b[H\x1b[s")
		t.drawn = true
	} else {
		// Restore the beginning of the block. Unlike counting lines, this stays
		// correct when long text wraps or the SSH terminal is resized.
		fmt.Fprint(t.out, "\x1b[u\x1b[J")
	}
	fmt.Fprint(t.out, content)
}

func (t *terminal) readKeys() {
	b := make([]byte, 1)
	for {
		if _, err := t.in.Read(b); err != nil {
			close(t.keys)
			return
		}
		switch b[0] {
		case 3:
			t.keys <- keyInterrupt
		case '\r', '\n':
			t.keys <- keyEnter
		case 127, 8:
			t.keys <- keyBackspace
		case 27:
			fds := []unix.PollFd{{Fd: int32(t.in.Fd()), Events: unix.POLLIN}}
			if n, _ := unix.Poll(fds, 30); n == 0 {
				t.keys <- keyEscape
				continue
			}
			seq := make([]byte, 2)
			if _, err := t.in.Read(seq[:1]); err != nil || seq[0] != '[' {
				t.keys <- keyEscape
				continue
			}
			if _, err := t.in.Read(seq[1:]); err != nil {
				t.keys <- keyEscape
				continue
			}
			switch seq[1] {
			case 'A':
				t.keys <- keyUp
			case 'B':
				t.keys <- keyDown
			default:
				t.keys <- keyUnknown
			}
		case 'k':
			t.keys <- keyUp
		case 'j':
			t.keys <- keyDown
		case 'q':
			t.keys <- keyQuit
		default:
			t.keys <- key(b[0]) + 1000
		}
	}
}

func typedRune(k key) (rune, bool) {
	if k < 1000 {
		return 0, false
	}
	return rune(k - 1000), true
}
