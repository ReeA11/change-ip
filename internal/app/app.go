package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ReeA11/change-ip/internal/backup"
	"github.com/ReeA11/change-ip/internal/config"
	"github.com/ReeA11/change-ip/internal/diagnostics"
	"github.com/ReeA11/change-ip/internal/network"
	"github.com/ReeA11/change-ip/internal/persist"
	"github.com/ReeA11/change-ip/internal/transaction"
)

type Options struct {
	Target, Prefix, Gateway, Interface, Profile    string
	DryRun, RuntimeOnly, Yes, CheckEgress, Verbose bool
	Progress                                       func(string)
}

const (
	ProgressAddress     = "address"
	ProgressGateway     = "gateway-route"
	ProgressDefault     = "default-route"
	ProgressPersistence = "persistence"
	ProgressVerify      = "verification"
)

func progress(o Options, stage string) {
	if o.Progress != nil {
		o.Progress(stage)
	}
}

type Application struct {
	Net        network.Backend
	Binary     string
	Out, Err   io.Writer
	BackupRoot string
}

func New(n network.Backend, binary string) *Application {
	return &Application{Net: n, Binary: binary, Out: os.Stdout, Err: os.Stderr, BackupRoot: "/root"}
}

func (a *Application) interfaceFor(explicit string) (string, error) {
	if explicit != "" {
		if explicit == "lo" {
			return "", fmt.Errorf("refusing loopback interface")
		}
		if e := persist.ValidateInterface(explicit); e != nil {
			return "", e
		}
	} else if routed, e := a.Net.RouteTo(netip.MustParseAddr("1.1.1.1"), ""); e == nil && network.InterfaceAllowed(routed.Interface) {
		return routed.Interface, nil
	}
	routes, e := a.Net.DefaultRoutes()
	if e != nil {
		return "", e
	}
	r, e := network.SelectDefault(routes, explicit)
	if e != nil {
		return "", e
	}
	return r.Interface, nil
}
func parseTarget(spec, prefix string) (netip.Prefix, bool, error) {
	if strings.Contains(spec, "/") {
		p, e := netip.ParsePrefix(spec)
		if e != nil || !p.Addr().Is4() || p.Bits() < 1 || !config.UsableIPv4(p.Addr()) {
			return netip.Prefix{}, false, fmt.Errorf("invalid or unusable IPv4 prefix %q", spec)
		}
		if prefix != "" && prefix != fmt.Sprint(p.Bits()) {
			return netip.Prefix{}, false, fmt.Errorf("conflicting prefix lengths")
		}
		return netip.PrefixFrom(p.Addr(), p.Bits()), true, nil
	}
	ip, e := netip.ParseAddr(spec)
	if e != nil || !config.UsableIPv4(ip) {
		return netip.Prefix{}, false, fmt.Errorf("invalid or unusable IPv4 %q", spec)
	}
	if prefix == "" {
		return netip.PrefixFrom(ip, 32), false, nil
	}
	p, e := netip.ParsePrefix(spec + "/" + strings.TrimPrefix(prefix, "/"))
	if e != nil || p.Bits() < 1 {
		return netip.Prefix{}, false, fmt.Errorf("invalid IPv4 prefix %q", prefix)
	}
	return netip.PrefixFrom(ip, p.Bits()), true, nil
}
func (a *Application) Resolve(o Options) (transaction.Plan, error) {
	iface, e := a.interfaceFor(o.Interface)
	if e != nil {
		return transaction.Plan{}, e
	}
	before, e := a.Net.Snapshot(iface)
	if e != nil {
		return transaction.Plan{}, e
	}
	target, prefixKnown, e := parseTarget(o.Target, o.Prefix)
	if e != nil {
		return transaction.Plan{}, e
	}
	var profileEntry config.ProfileEntry
	var profileFound bool
	if o.Profile != "" {
		entries, profileErr := config.LoadProfile(o.Profile)
		if profileErr != nil && !os.IsNotExist(profileErr) {
			return transaction.Plan{}, profileErr
		}
		profileEntry, profileFound = entries[target.Addr()]
		if profileFound && o.Gateway == "" && profileEntry.Gateway.IsValid() {
			o.Gateway = profileEntry.Gateway.String()
		}
	}
	if prefixKnown && profileFound && profileEntry.Prefix.Bits() != target.Bits() {
		return transaction.Plan{}, fmt.Errorf("requested /%d conflicts with profile /%d for %s", target.Bits(), profileEntry.Prefix.Bits(), target.Addr())
	}
	if !prefixKnown {
		if profileFound {
			target = profileEntry.Prefix
			prefixKnown = true
		} else if old, ok := network.HasAddress(before, target.Addr()); ok {
			target = old.Prefix
			prefixKnown = true
		}
		if !prefixKnown {
			return transaction.Plan{}, fmt.Errorf("prefix required for new IP %s; it is never guessed", target.Addr())
		}
	}
	gw := before.DefaultRoute.Gateway
	if o.Gateway != "" {
		gw, e = netip.ParseAddr(o.Gateway)
		if e != nil || !config.UsableIPv4(gw) {
			return transaction.Plan{}, fmt.Errorf("invalid gateway %q", o.Gateway)
		}
	}
	if !gw.IsValid() {
		return transaction.Plan{}, fmt.Errorf("no IPv4 gateway on %s", iface)
	}
	return transaction.BuildPlan(before, target, gw)
}
func printPlan(out io.Writer, p transaction.Plan, runtimeOnly bool) {
	fmt.Fprintf(out, "\nChangeIP plan\n  Interface : %s\n  Old source: %s\n  New IP    : %s\n  Gateway   : %s\n  Metric    : %d\n  Table     : %d\n  On-link   : %t\n  Add IP    : %t\n  Persist   : %t\n\n", p.Before.Interface, p.Before.OutboundSource, p.Target.OutboundSource, p.Target.DefaultRoute.Gateway, p.Target.DefaultRoute.Metric, p.Target.DefaultRoute.Table, p.Target.DefaultRoute.OnLink, p.AddressToAdd != nil, !runtimeOnly)
	if p.Before.DefaultRoute.Gateway != p.Target.DefaultRoute.Gateway {
		fmt.Fprintln(out, "[WARNING] Gateway changes; a wrong provider gateway can disconnect the server.")
	}
}
func confirm(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "Apply network change? [y/N] ")
	s := bufio.NewScanner(in)
	return s.Scan() && (s.Text() == "y" || s.Text() == "Y")
}
func systemdAvailable() bool {
	_, e := os.Stat("/run/systemd/system")
	if e != nil {
		return false
	}
	_, e = exec.LookPath("systemctl")
	return e == nil
}

func unitEnabled(path string) bool {
	return exec.Command("systemctl", "is-enabled", filepath.Base(path)).Run() == nil
}

func restoreUnitState(unitPath string, wasEnabled bool) error {
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return err
	}
	action := "disable"
	if wasEnabled {
		action = "enable"
	}
	cmd := exec.Command("systemctl", action, filepath.Base(unitPath))
	if err := cmd.Run(); err != nil && wasEnabled {
		return err
	}
	return nil
}

func (a *Application) Apply(o Options, signals <-chan os.Signal) (backupDir string, err error) {
	p, e := a.Resolve(o)
	if e != nil {
		return "", e
	}
	printPlan(a.Out, p, o.RuntimeOnly)
	if o.DryRun {
		fmt.Fprintln(a.Out, "[OK] Dry-run complete; no changes applied.")
		return "", nil
	}
	if os.Geteuid() != 0 {
		return "", fmt.Errorf("run as root")
	}
	if !o.Yes && !confirm(os.Stdin, a.Out) {
		return "", fmt.Errorf("aborted by user")
	}
	if !o.RuntimeOnly && !systemdAvailable() {
		return "", fmt.Errorf("active systemd is required; use --runtime-only for a temporary change")
	}
	tx := &transaction.Transaction{Backend: a.Net, Plan: p}
	var manifest backup.Manifest
	var manifestPath string
	var ps *persist.Systemd
	rollback := func(cause error) error {
		var all []error
		all = append(all, cause)
		if manifest.PersistenceChanged {
			if e := backup.RestoreFiles(manifest.Files); e != nil {
				all = append(all, fmt.Errorf("rollback persistence: %w", e))
			}
			if len(manifest.Files) > 1 {
				_ = restoreUnitState(manifest.Files[1].Path, manifest.UnitWasEnabled)
			}
		}
		if e := tx.Rollback(); e != nil {
			all = append(all, e)
		}
		if manifestPath != "" {
			manifest.Status = "rolled_back"
			_ = backup.Write(manifestPath, manifest)
		}
		return errors.Join(all...)
	}
	if !o.RuntimeOnly {
		ps = persist.NewSystemd(a.Binary)
		cfg, unit, e := ps.Paths(p.Before.Interface)
		if e != nil {
			return "", e
		}
		f1, e := backup.Capture(cfg)
		if e != nil {
			return "", e
		}
		f2, e := backup.Capture(unit)
		if e != nil {
			return "", e
		}
		backupDir, e = os.MkdirTemp(a.BackupRoot, "change-ip-backup.")
		if e != nil {
			return "", fmt.Errorf("create backup: %w", e)
		}
		manifestPath = filepath.Join(backupDir, "manifest.json")
		manifest = backup.Manifest{Created: time.Now().UTC(), Status: "pending", Interface: p.Before.Interface, Before: p.Before, Target: p.Target, AddressAdded: p.AddressToAdd != nil, HostRouteAdded: p.HostRouteToAdd != nil, PersistenceChanged: true, UnitWasEnabled: unitEnabled(unit), Files: []backup.FileSnapshot{f1, f2}}
		if e = backup.Write(manifestPath, manifest); e != nil {
			return "", e
		}
	}
	checkSignal := func() error {
		if signals == nil {
			return nil
		}
		select {
		case sig := <-signals:
			return fmt.Errorf("interrupted by %s", sig)
		default:
			return nil
		}
	}
	if signalErr := checkSignal(); signalErr != nil {
		return backupDir, rollback(signalErr)
	}
	if e = tx.Apply(); e != nil {
		if manifestPath != "" {
			manifest.Status = "rolled_back"
			_ = backup.Write(manifestPath, manifest)
		}
		return backupDir, e
	}
	if p.AddressToAdd != nil {
		progress(o, ProgressAddress)
	}
	if p.HostRouteToAdd != nil {
		progress(o, ProgressGateway)
	}
	progress(o, ProgressDefault)
	if signalErr := checkSignal(); signalErr != nil {
		return backupDir, rollback(signalErr)
	}
	manifest.AddressAdded = tx.AddressAdded
	manifest.HostRouteAdded = tx.HostRouteAdded
	if !o.RuntimeOnly {
		_, unit, e := ps.Write(p.Target)
		if e != nil {
			return backupDir, rollback(fmt.Errorf("write persistence: %w", e))
		}
		manifest.PersistenceChanged = true
		if e = backup.Write(manifestPath, manifest); e != nil {
			return backupDir, rollback(e)
		}
		if e = ps.Enable(unit); e != nil {
			return backupDir, rollback(e)
		}
		progress(o, ProgressPersistence)
		if signalErr := checkSignal(); signalErr != nil {
			return backupDir, rollback(signalErr)
		}
	}
	if e = tx.Verify(); e != nil {
		return backupDir, rollback(fmt.Errorf("verification failed: %w", e))
	}
	progress(o, ProgressVerify)
	if o.CheckEgress {
		a.checkEgress(p.Target.OutboundSource, p.Target.Interface)
	}
	if signalErr := checkSignal(); signalErr != nil {
		return backupDir, rollback(signalErr)
	}
	if manifestPath != "" {
		manifest.Status = "committed"
		if e = backup.Write(manifestPath, manifest); e != nil {
			return backupDir, rollback(fmt.Errorf("commit transaction metadata: %w", e))
		}
	}
	tx.Committed = true
	fmt.Fprintf(a.Out, "[OK] Outbound source is %s\n", p.Target.OutboundSource)
	if backupDir != "" {
		fmt.Fprintf(a.Out, "Backup: %s\n", backupDir)
	}
	return backupDir, nil
}

func (a *Application) checkEgress(source netip.Addr, iface string) {
	if r, e := a.Net.RouteTo(netip.MustParseAddr("8.8.8.8"), iface); e != nil {
		fmt.Fprintf(a.Err, "[WARNING] route to 8.8.8.8: %v\n", e)
	} else {
		fmt.Fprintf(a.Out, "[OK] route to 8.8.8.8 uses %s src %s\n", r.Interface, r.Source)
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{IP: net.IP(source.AsSlice())}}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext}}
	resp, e := client.Get("https://ifconfig.me/ip")
	if e != nil {
		fmt.Fprintf(a.Err, "[WARNING] external IPv4 service unavailable: %v\n", e)
		return
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 64))
	if e != nil || resp.StatusCode/100 != 2 {
		fmt.Fprintf(a.Err, "[WARNING] external IPv4 service returned %s\n", resp.Status)
		return
	}
	external := strings.TrimSpace(string(b))
	fmt.Fprintf(a.Out, "External IPv4: %s\n", external)
	if external != source.String() {
		fmt.Fprintf(a.Err, "[WARNING] external IPv4 differs from selected source (NAT/provider policy may apply)\n")
	}
}

func (a *Application) Status(iface string, doctor bool) error {
	s, e := a.Collect(iface)
	if e != nil {
		return e
	}
	fmt.Fprintf(a.Out, "Interface     : %s\nOutbound src  : %s\nGateway       : %s\nDefault route : table %d metric %d onlink=%t\nAddresses     :", s.State.Interface, s.State.OutboundSource, s.State.DefaultRoute.Gateway, s.State.DefaultRoute.Table, s.State.DefaultRoute.Metric, s.State.DefaultRoute.OnLink)
	for _, x := range s.State.Addresses {
		fmt.Fprintf(a.Out, " %s", x.Prefix)
	}
	fmt.Fprintf(a.Out, "\nPersistence   : %s\nSystemd unit  : %s / %s\n", s.ConfigPath, s.UnitEnabled, s.UnitActive)
	if s.Desired != nil {
		fmt.Fprintf(a.Out, "After reboot  : src %s via %s table %d metric %d\n", s.Desired.OutboundSource, s.Desired.DefaultRoute.Gateway, s.Desired.DefaultRoute.Table, s.Desired.DefaultRoute.Metric)
	} else {
		fmt.Fprintln(a.Out, "After reboot  : current network manager configuration")
	}
	if doctor {
		problems := diagnostics.Problems(s)
		if len(problems) == 0 {
			fmt.Fprintln(a.Out, "[OK] No problems detected")
		} else {
			for _, p := range problems {
				fmt.Fprintf(a.Out, "[WARNING] %s\n", p)
			}
			unit := filepath.Base(s.UnitPath)
			if s.UnitActive == "failed" {
				cmd := exec.Command("journalctl", "-u", unit, "-b", "--no-pager", "-n", "40")
				cmd.Stdout = a.Out
				cmd.Stderr = a.Err
				_ = cmd.Run()
			}
		}
		if xs, e := a.Backups(); e == nil {
			for _, dir := range xs {
				if m, e := backup.Read(filepath.Join(dir, "manifest.json")); e == nil && m.Status == "pending" {
					fmt.Fprintf(a.Err, "[WARNING] unfinished transaction metadata: %s\n", dir)
				}
			}
		}
	}
	return nil
}

// Collect returns fresh structured runtime and persistence diagnostics for UI clients.
func (a *Application) Collect(iface string) (diagnostics.Status, error) {
	if iface == "" {
		x, e := a.interfaceFor("")
		if e != nil {
			return diagnostics.Status{}, e
		}
		iface = x
	}
	s, e := diagnostics.Collect(a.Net, iface, a.Binary)
	if e != nil {
		return diagnostics.Status{}, e
	}
	return s, nil
}

func validBackupDir(root, dir string) bool {
	abs, e := filepath.Abs(dir)
	if e != nil {
		return false
	}
	rr, e := filepath.Abs(root)
	if e != nil {
		return false
	}
	return filepath.Dir(abs) == rr && strings.HasPrefix(filepath.Base(abs), "change-ip-backup.")
}
func (a *Application) Backups() ([]string, error) {
	xs, e := filepath.Glob(filepath.Join(a.BackupRoot, "change-ip-backup.*"))
	if e != nil {
		return nil, e
	}
	sort.Slice(xs, func(i, j int) bool {
		ai, _ := os.Stat(xs[i])
		aj, _ := os.Stat(xs[j])
		return ai.ModTime().After(aj.ModTime())
	})
	return xs, nil
}
func (a *Application) Rollback(dir string) error {
	if !validBackupDir(a.BackupRoot, dir) {
		return fmt.Errorf("unsafe backup directory %q", dir)
	}
	m, e := backup.Read(filepath.Join(dir, "manifest.json"))
	if e != nil {
		return e
	}
	p := transaction.Plan{Before: m.Before, Target: m.Target}
	if m.AddressAdded {
		for _, x := range m.Target.Addresses {
			if _, ok := network.HasAddress(m.Before, x.Prefix.Addr()); !ok {
				q := x.Prefix
				p.AddressToAdd = &q
				break
			}
		}
	}
	if m.HostRouteAdded && m.Target.GatewayHostRoute != nil {
		p.HostRouteToAdd = m.Target.GatewayHostRoute
	}
	tx := transaction.Transaction{Backend: a.Net, Plan: p, AddressAdded: m.AddressAdded, HostRouteAdded: m.HostRouteAdded, DefaultChanged: true}
	var errs []error
	if e = backup.RestoreFiles(m.Files); e != nil {
		errs = append(errs, e)
	}
	if len(m.Files) > 1 {
		if e = restoreUnitState(m.Files[1].Path, m.UnitWasEnabled); e != nil {
			errs = append(errs, e)
		}
	}
	if e = tx.Rollback(); e != nil {
		errs = append(errs, e)
	}
	m.Status = "rolled_back"
	if e = backup.Write(filepath.Join(dir, "manifest.json"), m); e != nil {
		errs = append(errs, e)
	}
	return errors.Join(errs...)
}

func (a *Application) ApplyProfile(path string) error {
	clean := filepath.Clean(path)
	if filepath.Dir(clean) != "/usr/local/lib/change-ip" || !strings.HasPrefix(filepath.Base(clean), "apply-") || !strings.HasSuffix(clean, ".json") {
		return fmt.Errorf("unsafe apply configuration path %q", path)
	}
	c, e := persist.Load(clean)
	if e != nil {
		return e
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, e = a.Net.Snapshot(c.State.Interface); e == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("interface %s did not become ready", c.State.Interface)
		}
		time.Sleep(time.Second)
	}
	current, e := a.Net.Snapshot(c.State.Interface)
	if e != nil {
		return e
	}
	return a.ApplyDesiredState(current, c.State)
}

// ApplyDesiredState is the netlink-only body used by the systemd boot path.
// It is exported inside the internal package tree so namespace integration
// tests can exercise reboot persistence without running systemd.
func (a *Application) ApplyDesiredState(current, desired network.State) error {
	for _, addr := range desired.Addresses {
		if addr.Dynamic && addr.Prefix.Addr() != desired.OutboundSource {
			continue
		}
		if _, ok := network.HasAddress(current, addr.Prefix.Addr()); !ok {
			if e := a.Net.AddAddress(desired.Interface, addr.Prefix); e != nil {
				return fmt.Errorf("restore address %s: %w", addr.Prefix, e)
			}
		}
	}
	if desired.GatewayHostRoute != nil {
		if e := a.Net.ReplaceRoute(*desired.GatewayHostRoute); e != nil {
			return fmt.Errorf("restore gateway route: %w", e)
		}
	}
	if e := a.Net.ReplaceRoute(desired.DefaultRoute); e != nil {
		return fmt.Errorf("restore default route: %w", e)
	}
	return nil
}
