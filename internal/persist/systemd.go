package persist

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
}

func NewSystemd(binary string) *Systemd {
	return &Systemd{BinaryPath: binary, ConfigDir: "/usr/local/lib/change-ip", UnitDir: "/etc/systemd/system", Run: run}
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
	unit := fmt.Sprintf(`[Unit]
Description=ChangeIP preferred source on %s
After=network-online.target networking.service cloud-init.service
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s apply-profile --config %s

[Install]
WantedBy=multi-user.target
`, state.Interface, s.BinaryPath, cfgPath)
	if strings.ContainsAny(s.BinaryPath, " \t\n") {
		return "", "", fmt.Errorf("binary path contains whitespace")
	}
	if e = atomicWrite(unitPath, []byte(unit), 0644); e != nil {
		return "", "", e
	}
	return cfgPath, unitPath, nil
}
func (s *Systemd) Enable(unitPath string) error {
	if e := s.Run("systemctl", "daemon-reload"); e != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", e)
	}
	if e := s.Run("systemctl", "enable", filepath.Base(unitPath)); e != nil {
		return fmt.Errorf("enable unit: %w", e)
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
