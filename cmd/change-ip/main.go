package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ReeA11/change-ip/internal/app"
	"github.com/ReeA11/change-ip/internal/network"
	"github.com/ReeA11/change-ip/internal/platform"
	"github.com/ReeA11/change-ip/internal/ui"
	"github.com/ReeA11/change-ip/internal/updater"
)

// Release builds override this value from the Git tag with -ldflags.
var version = "3.0.2-dev"

func usage() {
	fmt.Printf(`change-ip %s — make a provider-assigned IPv4 the outbound source

Usage:
  change-ip                         interactive wizard
  change-ip NEW_IP[/PREFIX] [IFACE]
  change-ip status [IFACE]
  change-ip doctor [IFACE]
  change-ip rollback [BACKUP_DIR]
  change-ip update

Options:
  --gateway, -g GW       gateway IPv4
  --interface, -i IFACE  uplink interface
  --prefix, -p N         prefix length (1-32)
  --profile FILE         address profile
  --dry-run              discover, validate and print plan only
  --runtime-only         do not create persistence or backup
  --yes, -y              skip confirmation
  --check-egress         optional egress diagnostics
  --verbose              verbose diagnostics
  --version              print version
`, version)
}

func value(args []string, i *int, name string) (string, error) {
	if *i+1 >= len(args) {
		return "", fmt.Errorf("%s requires a value", name)
	}
	*i++
	return args[*i], nil
}
func parseApply(args []string) (app.Options, error) {
	o := app.Options{Profile: "/etc/change-ip-addresses.conf"}
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			o.DryRun = true
		case "--runtime-only":
			o.RuntimeOnly = true
		case "--yes", "-y":
			o.Yes = true
		case "--check-egress":
			o.CheckEgress = true
		case "--verbose":
			o.Verbose = true
		case "--gateway", "-g":
			v, e := value(args, &i, args[i])
			if e != nil {
				return o, e
			}
			o.Gateway = v
		case "--interface", "-i":
			v, e := value(args, &i, args[i])
			if e != nil {
				return o, e
			}
			o.Interface = v
		case "--prefix", "-p":
			v, e := value(args, &i, args[i])
			if e != nil {
				return o, e
			}
			o.Prefix = strings.TrimPrefix(v, "/")
		case "--profile":
			v, e := value(args, &i, args[i])
			if e != nil {
				return o, e
			}
			o.Profile = v
		case "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(args[i], "-") {
				return o, fmt.Errorf("unknown option %s", args[i])
			}
			positional = append(positional, args[i])
		}
	}
	if len(positional) == 0 {
		return o, fmt.Errorf("NEW_IP is required")
	}
	if len(positional) > 2 {
		return o, fmt.Errorf("unexpected argument %s", positional[2])
	}
	o.Target = positional[0]
	if len(positional) == 2 {
		if o.Interface != "" && o.Interface != positional[1] {
			return o, fmt.Errorf("conflicting interfaces")
		}
		o.Interface = positional[1]
	}
	return o, nil
}

func readLine(prompt string) (string, error) {
	fmt.Print(prompt)
	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		return "", fmt.Errorf("input ended")
	}
	return strings.TrimSpace(s.Text()), nil
}
func isTerminal() bool { st, e := os.Stdin.Stat(); return e == nil && st.Mode()&os.ModeCharDevice != 0 }

func systemdRunArgs(self string, args []string) []string {
	cmdArgs := []string{"--quiet", "--wait", "--pty", "--same-dir", "--setenv=CHANGE_IP_SYSTEMD_RUN=1", self}
	return append(cmdArgs, args...)
}

func maybeSystemdRun(args []string) error {
	if os.Getenv("CHANGE_IP_SYSTEMD_RUN") == "1" || os.Getenv("INVOCATION_ID") != "" {
		return nil
	}
	if !isTerminal() {
		fmt.Fprintln(os.Stderr, "[WARNING] no TTY: independent systemd-run execution is unavailable; loss of this process may interrupt rollback")
		return nil
	}
	path, e := exec.LookPath("systemd-run")
	if e != nil {
		fmt.Fprintln(os.Stderr, "[WARNING] systemd-run unavailable; SSH may disconnect during route change")
		return nil
	}
	self, e := os.Executable()
	if e != nil {
		return nil
	}
	cmd := exec.Command(path, systemdRunArgs(self, args)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "CHANGE_IP_SYSTEMD_RUN=1")
	if e = cmd.Run(); e != nil {
		if x, ok := e.(*exec.ExitError); ok {
			os.Exit(x.ExitCode())
		}
		return e
	}
	os.Exit(0)
	return nil
}
func withLock(fn func() error) error {
	lock, e := platform.AcquireLock("/run/change-ip.lock")
	if e != nil {
		return e
	}
	defer lock.Close()
	return fn()
}
func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(version)
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		usage()
		return
	}
	backend := network.NewNetlinkBackend()
	self, e := os.Executable()
	if e != nil {
		self = "/usr/local/sbin/change-ip"
	}
	a := app.New(backend, self)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	args := os.Args[1:]
	cmd := "wizard"
	if len(args) > 0 {
		cmd = args[0]
	}
	var runErr error
	switch cmd {
	case "update":
		if len(args) != 1 {
			runErr = fmt.Errorf("update does not accept arguments")
			break
		}
		if os.Geteuid() != 0 {
			runErr = fmt.Errorf("run as root: sudo change-ip update")
			break
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			select {
			case <-sig:
				cancel()
			case <-done:
			}
		}()
		u := updater.New(updater.Config{CurrentVersion: version, Out: os.Stdout})
		runErr = withLock(func() error {
			_, updateErr := u.Run(ctx)
			return updateErr
		})
		close(done)
		cancel()
	case "status", "doctor":
		iface := ""
		if len(args) > 1 {
			iface = args[1]
		}
		runErr = a.Status(iface, cmd == "doctor")
	case "rollback", "--rollback":
		dir := ""
		if len(args) > 1 {
			dir = args[1]
		}
		if dir == "" {
			xs, e := a.Backups()
			if e != nil {
				runErr = e
				break
			}
			if len(xs) == 0 {
				runErr = fmt.Errorf("no backups found")
				break
			}
			if !isTerminal() {
				runErr = fmt.Errorf("backup directory required without a TTY")
				break
			}
			for i, x := range xs {
				fmt.Printf("  %d) %s\n", i+1, x)
			}
			v, e := readLine("Backup number: ")
			if e != nil {
				runErr = e
				break
			}
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > len(xs) {
				runErr = fmt.Errorf("invalid backup selection")
				break
			}
			dir = xs[n-1]
		}
		if e := maybeSystemdRun(args); e != nil {
			runErr = e
			break
		}
		runErr = withLock(func() error { return a.Rollback(dir) })
	case "apply-profile":
		var path string
		if len(args) == 3 && args[1] == "--config" {
			path = args[2]
		} else {
			runErr = fmt.Errorf("apply-profile requires --config FILE")
			break
		}
		runErr = withLock(func() error { return a.ApplyProfile(filepath.Clean(path)) })
	case "wizard", "--tui":
		if !ui.CanRun(os.Stdin, os.Stdout) {
			runErr = fmt.Errorf("interactive mode requires TTY stdin and stdout")
			break
		}
		if e := maybeSystemdRun(args); e != nil {
			runErr = e
			break
		}
		runErr = withLock(func() error { return ui.Run(a, version, sig) })
	case "apply":
		o, parseErr := parseApply(args[1:])
		if parseErr != nil {
			runErr = parseErr
			break
		}
		if !o.DryRun {
			if e := maybeSystemdRun(args); e != nil {
				runErr = e
				break
			}
		}
		if o.DryRun {
			_, runErr = a.Apply(o, nil)
		} else {
			runErr = withLock(func() error { _, e := a.Apply(o, sig); return e })
		}
	default:
		o, e := parseApply(args)
		if e != nil {
			runErr = e
			break
		}
		if !o.DryRun {
			if e := maybeSystemdRun(args); e != nil {
				runErr = e
				break
			}
		}
		if o.DryRun {
			_, runErr = a.Apply(o, nil)
		} else {
			runErr = withLock(func() error { _, e := a.Apply(o, sig); return e })
		}
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] %v\n", runErr)
		os.Exit(1)
	}
}
