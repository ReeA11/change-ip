package backup

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReeA11/change-ip/internal/network"
)

const ManifestVersion = 2

type FileSnapshot struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode,omitempty"`
	Data    string `json:"data_base64,omitempty"`
}
type UnitState struct {
	Path       string `json:"path"`
	WasEnabled bool   `json:"was_enabled"`
}
type Manifest struct {
	Version            int                   `json:"version"`
	Created            time.Time             `json:"created"`
	Status             string                `json:"status"`
	Interface          string                `json:"interface"`
	Before             network.State         `json:"before"`
	Target             network.State         `json:"target"`
	AddressAdded       bool                  `json:"address_added"`
	AddressesAdded     []netip.Prefix        `json:"addresses_added,omitempty"`
	HostRouteAdded     bool                  `json:"host_route_added"`
	RouteChanges       []network.RouteChange `json:"route_changes,omitempty"`
	RoutesAdded        []network.Route       `json:"routes_added,omitempty"`
	RulesAdded         []network.Rule        `json:"rules_added,omitempty"`
	PersistenceChanged bool                  `json:"persistence_changed"`
	UnitWasEnabled     bool                  `json:"unit_was_enabled"`
	OtherUnits         []UnitState           `json:"other_units,omitempty"`
	Files              []FileSnapshot        `json:"files"`
}

func ValidateManagedPath(path string) error {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	validUnit := filepath.Dir(clean) == "/etc/systemd/system" && strings.HasPrefix(base, "change-ip-") && strings.HasSuffix(base, ".service")
	validConfig := filepath.Dir(clean) == "/usr/local/lib/change-ip" && strings.HasPrefix(base, "apply-") && strings.HasSuffix(base, ".json")
	if !validUnit && !validConfig {
		return fmt.Errorf("path outside ChangeIP managed locations: %s", path)
	}
	if strings.Contains(filepath.Base(clean), "..") {
		return fmt.Errorf("unsafe path")
	}
	return nil
}
func Capture(path string) (FileSnapshot, error) {
	if e := ValidateManagedPath(path); e != nil {
		return FileSnapshot{}, e
	}
	data, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return FileSnapshot{Path: path}, nil
	}
	if e != nil {
		return FileSnapshot{}, e
	}
	st, e := os.Stat(path)
	if e != nil {
		return FileSnapshot{}, e
	}
	return FileSnapshot{Path: path, Existed: true, Mode: uint32(st.Mode().Perm()), Data: base64.StdEncoding.EncodeToString(data)}, nil
}
func Write(path string, m Manifest) error {
	m.Version = ManifestVersion
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func Read(path string) (Manifest, error) {
	var m Manifest
	b, e := os.ReadFile(path)
	if e != nil {
		return m, e
	}
	if e = json.Unmarshal(b, &m); e != nil {
		return m, e
	}
	if m.Version != ManifestVersion {
		return m, fmt.Errorf("unsupported manifest version %d", m.Version)
	}
	for _, f := range m.Files {
		if e := ValidateManagedPath(f.Path); e != nil {
			return m, e
		}
	}
	for _, unit := range m.OtherUnits {
		if e := ValidateManagedPath(unit.Path); e != nil {
			return m, e
		}
	}
	return m, nil
}
func RestoreFiles(files []FileSnapshot) error {
	for _, f := range files {
		if e := ValidateManagedPath(f.Path); e != nil {
			return e
		}
		if !f.Existed {
			if e := os.Remove(f.Path); e != nil && !os.IsNotExist(e) {
				return e
			}
			continue
		}
		data, e := base64.StdEncoding.DecodeString(f.Data)
		if e != nil {
			return e
		}
		if e = os.MkdirAll(filepath.Dir(f.Path), 0755); e != nil {
			return e
		}
		if e = os.WriteFile(f.Path, data, os.FileMode(f.Mode)); e != nil {
			return e
		}
	}
	return nil
}
