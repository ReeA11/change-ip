package persist

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ReeA11/change-ip/internal/network"
)

var interfaceName = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)

type Config struct {
	Version int           `json:"version"`
	State   network.State `json:"state"`
}
type Systemd struct {
	BinaryPath, ConfigDir, UnitDir string
	Run                            func(string, ...string) error
	ManagerFor                     func(string) string
	IsEnabled                      func(string) bool
}

func NewSystemd(binary string) *Systemd {
	return &Systemd{BinaryPath: binary, ConfigDir: "/usr/local/lib/change-ip", UnitDir: "/etc/systemd/system", Run: run, ManagerFor: managerForInterface, IsEnabled: unitEnabled}
}

func unitActive(name string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", name).Run() == nil
}

func unitEnabled(name string) bool {
	return exec.Command("systemctl", "is-enabled", "--quiet", name).Run() == nil
}

func managerForInterface(iface string) string {
	if unitActive("systemd-networkd.service") {
		cmd := exec.Command("networkctl", "status", "--no-pager", iface)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		if output, err := cmd.CombinedOutput(); err == nil && !strings.Contains(strings.ToLower(string(output)), "unmanaged") {
			return "systemd-networkd.service"
		}
	}
	if unitActive("NetworkManager.service") {
		cmd := exec.Command("nmcli", "-g", "GENERAL.STATE", "device", "show", iface)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		if output, err := cmd.Output(); err == nil {
			state, _, _ := strings.Cut(strings.TrimSpace(string(output)), " ")
			if state != "10" {
				return "NetworkManager.service"
			}
		}
	}
	loadState, err := exec.Command("systemctl", "show", "--property=LoadState", "--value", "networking.service").Output()
	if err == nil && strings.TrimSpace(string(loadState)) == "loaded" {
		output, queryErr := exec.Command("ifquery", "--list").Output()
		if queryErr == nil && slices.Contains(strings.Fields(string(output)), iface) {
			return "networking.service"
		}
	}
	return ""
}

type networkManager struct {
	service string
	wait    string
}

func (s *Systemd) networkManager(iface string) networkManager {
	if s.ManagerFor == nil {
		return networkManager{}
	}
	switch s.ManagerFor(iface) {
	case "systemd-networkd.service":
		return networkManager{service: "systemd-networkd.service", wait: "/usr/lib/systemd/systemd-networkd-wait-online --interface=" + iface + " --timeout=30"}
	case "NetworkManager.service":
		return networkManager{service: "NetworkManager.service", wait: "nm-online -q --timeout=30"}
	case "networking.service":
		return networkManager{service: "networking.service"}
	}
	return networkManager{}
}
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func ValidateInterface(name string) error {
	if !interfaceName.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("unsafe interface name %q", name)
	}
	return nil
}
func (s *Systemd) Paths(iface string) (string, string, error) {
	if e := ValidateInterface(iface); e != nil {
		return "", "", e
	}
	return filepath.Join(s.ConfigDir, "apply-"+iface+".json"), filepath.Join(s.UnitDir, "change-ip-"+iface+".service"), nil
}
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	tmp := path + ".tmp"
	if e := os.WriteFile(tmp, data, mode); e != nil {
		return e
	}
	if e := os.Rename(tmp, path); e != nil {
		return e
	}
	return nil
}
func (s *Systemd) Write(state network.State) (string, string, error) {
	cfgPath, unitPath, e := s.Paths(state.Interface)
	if e != nil {
		return "", "", e
	}
	b, e := json.MarshalIndent(Config{Version: 1, State: state}, "", "  ")
	if e != nil {
		return "", "", e
	}
	b = append(b, '\n')
	if e = atomicWrite(cfgPath, b, 0600); e != nil {
		return "", "", e
	}
	if e = s.writeUnit(state, cfgPath, unitPath); e != nil {
		return "", "", e
	}
	return cfgPath, unitPath, nil
}

func (s *Systemd) writeUnit(state network.State, cfgPath, unitPath string) error {
	manager := s.networkManager(state.Interface)
	after, partOf, wantedBy, wait := "", "", "multi-user.target", ""
	if manager.service != "" {
		after = " " + manager.service
		partOf = "PartOf=" + manager.service + "\n"
		wantedBy += " " + manager.service
	}
	if manager.wait != "" {
		wait = "ExecStartPre=" + manager.wait + "\n"
	}
	unit := fmt.Sprintf(`[Unit]
Description=ChangeIP preferred source on %s
After=network-online.target cloud-init.service%s
Wants=network-online.target
%s

[Service]
Type=oneshot
RemainAfterExit=yes
%sExecStart=%s apply-profile --config %s

[Install]
WantedBy=%s
`, state.Interface, after, partOf, wait, s.BinaryPath, cfgPath, wantedBy)
	if strings.ContainsAny(s.BinaryPath, " \t\n") {
		return fmt.Errorf("binary path contains whitespace")
	}
	return atomicWrite(unitPath, []byte(unit), 0644)
}

func (s *Systemd) Migrate() error {
	configs, err := filepath.Glob(filepath.Join(s.ConfigDir, "apply-*.json"))
	if err != nil {
		return err
	}
	profiles := make([]Config, 0, len(configs))
	for _, path := range configs {
		config, loadErr := Load(path)
		if loadErr != nil {
			return loadErr
		}
		profiles = append(profiles, config)
	}
	type savedUnit struct {
		path    string
		data    []byte
		mode    os.FileMode
		existed bool
		links   map[string]string
	}
	var saved []savedUnit
	var enabled []string
	unitLinks := func(unit string) ([]string, error) {
		var links []string
		for _, pattern := range []string{"*.wants", "*.requires"} {
			matches, globErr := filepath.Glob(filepath.Join(s.UnitDir, pattern, filepath.Base(unit)))
			if globErr != nil {
				return nil, globErr
			}
			links = append(links, matches...)
		}
		return links, nil
	}
	restore := func() {
		for i := len(saved) - 1; i >= 0; i-- {
			if saved[i].existed {
				_ = atomicWrite(saved[i].path, saved[i].data, saved[i].mode)
			} else {
				_ = os.Remove(saved[i].path)
			}
			links, _ := unitLinks(saved[i].path)
			for _, link := range links {
				_ = os.Remove(link)
			}
			for link, target := range saved[i].links {
				_ = os.MkdirAll(filepath.Dir(link), 0755)
				_ = os.Symlink(target, link)
			}
		}
		_ = s.Run("systemctl", "daemon-reload")
	}
	// Snapshot every unit and enablement link before changing the first one.
	for _, config := range profiles {
		_, unit, pathErr := s.Paths(config.State.Interface)
		if pathErr != nil {
			return pathErr
		}
		snapshot := savedUnit{path: unit, links: map[string]string{}}
		if snapshot.data, err = os.ReadFile(unit); err == nil {
			info, statErr := os.Stat(unit)
			if statErr != nil {
				return statErr
			}
			snapshot.mode, snapshot.existed = info.Mode().Perm(), true
		} else if !os.IsNotExist(err) {
			return err
		}
		links, linkErr := unitLinks(unit)
		if linkErr != nil {
			return linkErr
		}
		for _, link := range links {
			target, readErr := os.Readlink(link)
			if readErr != nil {
				return readErr
			}
			snapshot.links[link] = target
		}
		saved = append(saved, snapshot)
		if s.IsEnabled != nil && s.IsEnabled(filepath.Base(unit)) {
			enabled = append(enabled, filepath.Base(unit))
		}
	}
	for i, config := range profiles {
		path := configs[i]
		_, unit, _ := s.Paths(config.State.Interface)
		if writeErr := s.writeUnit(config.State, path, unit); writeErr != nil {
			restore()
			return writeErr
		}
	}
	if len(configs) == 0 {
		return nil
	}
	if err = s.Run("systemctl", "daemon-reload"); err != nil {
		restore()
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	for _, unit := range enabled {
		if err = s.Run("systemctl", "reenable", unit); err != nil {
			restore()
			return fmt.Errorf("reenable unit: %w", err)
		}
	}
	return nil
}
func (s *Systemd) Enable(unitPath string) error {
	if e := s.Run("systemctl", "daemon-reload"); e != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", e)
	}
	if e := s.Run("systemctl", "reenable", filepath.Base(unitPath)); e != nil {
		return fmt.Errorf("reenable unit: %w", e)
	}
	return nil
}
func Load(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if c.Version != 1 {
		return c, fmt.Errorf("unsupported apply config version %d", c.Version)
	}
	if e = ValidateInterface(c.State.Interface); e != nil {
		return c, e
	}
	return c, nil
}
