package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	maxChecksumsSize = 1 << 20
	maxBinarySize    = 64 << 20
	maxManPageSize   = 4 << 20
)

type Config struct {
	Repository     string
	BaseURL        string
	Prefix         string
	Arch           string
	CurrentVersion string
	Client         *http.Client
	Out            io.Writer
	ValidateBinary func(string) (string, error)
}

type Updater struct {
	config Config
}

func New(c Config) *Updater {
	if c.Repository == "" {
		c.Repository = "ReeA11/change-ip"
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://github.com/" + c.Repository + "/releases/latest/download"
	}
	if c.Prefix == "" {
		c.Prefix = "/usr/local"
	}
	if c.Arch == "" {
		c.Arch = runtime.GOARCH
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if c.Out == nil {
		c.Out = io.Discard
	}
	if c.ValidateBinary == nil {
		c.ValidateBinary = validateBinary
	}
	return &Updater{config: c}
}

func (u *Updater) Run(ctx context.Context) (string, error) {
	arch, err := releaseArch(u.config.Arch)
	if err != nil {
		return "", err
	}
	artifact := "change-ip-linux-" + arch
	fmt.Fprintf(u.config.Out, "[INFO] Downloading latest ChangeIP release for linux/%s...\n", arch)

	checksumsData, err := u.fetch(ctx, "checksums.txt", maxChecksumsSize)
	if err != nil {
		return "", err
	}
	checksums, err := parseChecksums(string(checksumsData))
	if err != nil {
		return "", fmt.Errorf("parse release checksums: %w", err)
	}

	binaryData, err := u.fetchVerified(ctx, artifact, maxBinarySize, checksums)
	if err != nil {
		return "", err
	}
	manData, err := u.fetchVerified(ctx, "change-ip.8", maxManPageSize, checksums)
	if err != nil {
		return "", err
	}

	binDir := filepath.Join(u.config.Prefix, "bin")
	manDir := filepath.Join(u.config.Prefix, "share", "man", "man8")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("create binary directory: %w", err)
	}
	if err := os.MkdirAll(manDir, 0o755); err != nil {
		return "", fmt.Errorf("create man directory: %w", err)
	}

	temporary, err := writeTemporary(binDir, ".change-ip.update-*", binaryData, 0o755)
	if err != nil {
		return "", fmt.Errorf("stage update: %w", err)
	}
	defer os.Remove(temporary)

	latestVersion, err := u.config.ValidateBinary(temporary)
	if err != nil {
		return "", fmt.Errorf("validate downloaded binary: %w", err)
	}
	if err := requireMajorVersion(latestVersion, 3); err != nil {
		return "", err
	}

	canonical := filepath.Join(binDir, "change-ip")
	if err := os.Rename(temporary, canonical); err != nil {
		return "", fmt.Errorf("atomically install %s: %w", canonical, err)
	}
	if err := syncDirectory(binDir); err != nil {
		return "", fmt.Errorf("sync binary directory: %w", err)
	}
	if err := writeAtomic(filepath.Join(manDir, "change-ip.8"), manData, 0o644); err != nil {
		return latestVersion, fmt.Errorf("binary %s installed, but man page installation failed: %w", latestVersion, err)
	}
	if err := u.cleanCommandNames(); err != nil {
		return latestVersion, fmt.Errorf("binary %s installed, but legacy command cleanup failed: %w", latestVersion, err)
	}

	fmt.Fprintf(u.config.Out, "[OK] ChangeIP updated: %s -> %s\n", u.config.CurrentVersion, latestVersion)
	return latestVersion, nil
}

func (u *Updater) fetchVerified(ctx context.Context, name string, limit int64, checksums map[string]string) ([]byte, error) {
	expected, ok := checksums[name]
	if !ok {
		return nil, fmt.Errorf("release checksum missing for %s", name)
	}
	data, err := u.fetch(ctx, name, limit)
	if err != nil {
		return nil, err
	}
	actual := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), expected) {
		return nil, fmt.Errorf("checksum mismatch for %s", name)
	}
	return data, nil
}

func (u *Updater) fetch(ctx context.Context, name string, limit int64) ([]byte, error) {
	url := strings.TrimRight(u.config.BaseURL, "/") + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("prepare download %s: %w", name, err)
	}
	resp, err := u.config.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %s", name, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download %s exceeds %d bytes", name, limit)
	}
	return data, nil
}

func (u *Updater) cleanCommandNames() error {
	for _, path := range []string{
		filepath.Join(u.config.Prefix, "bin", "change_ip"),
		filepath.Join(u.config.Prefix, "sbin", "change_ip"),
	} {
		if err := removeIfExists(path); err != nil {
			return err
		}
	}
	if u.config.Prefix == "/usr/local" {
		for _, path := range []string{"/usr/bin/change_ip", "/usr/sbin/change_ip"} {
			if err := removeIfExists(path); err != nil {
				return err
			}
		}
	}
	sbinDir := filepath.Join(u.config.Prefix, "sbin")
	if err := os.MkdirAll(sbinDir, 0o755); err != nil {
		return fmt.Errorf("create sbin directory: %w", err)
	}
	link := filepath.Join(sbinDir, "change-ip")
	if err := removeIfExists(link); err != nil {
		return err
	}
	if err := os.Symlink("../bin/change-ip", link); err != nil {
		return fmt.Errorf("create %s: %w", link, err)
	}
	return nil
}

func parseChecksums(data string) (map[string]string, error) {
	result := make(map[string]string)
	for lineNumber, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("line %d: expected checksum and filename", lineNumber+1)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(name) != name || name == "." {
			return nil, fmt.Errorf("line %d: invalid filename", lineNumber+1)
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("line %d: invalid SHA-256", lineNumber+1)
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("line %d: duplicate filename %s", lineNumber+1, name)
		}
		result[name] = strings.ToLower(fields[0])
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("empty checksum file")
	}
	return result, nil
}

func releaseArch(arch string) (string, error) {
	switch arch {
	case "amd64":
		return "amd64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %s", arch)
	}
}

func validateBinary(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	magic := make([]byte, 4)
	_, readErr := io.ReadFull(file, magic)
	closeErr := file.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if string(magic) != "\x7fELF" {
		return "", fmt.Errorf("download is not an ELF binary")
	}
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run --version: %w: %s", err, strings.TrimSpace(string(out)))
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", fmt.Errorf("empty version response")
	}
	return version, nil
}

func requireMajorVersion(version string, minimum int) error {
	value := strings.TrimSpace(strings.TrimPrefix(version, "v"))
	majorText, _, _ := strings.Cut(value, ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || major < minimum {
		return fmt.Errorf("expected ChangeIP >=%d.0, got %q", minimum, version)
	}
	return nil
}

func writeTemporary(dir, pattern string, data []byte, mode os.FileMode) (path string, err error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path = file.Name()
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err = file.Chmod(mode); err != nil {
		return "", err
	}
	if _, err = file.Write(data); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	return path, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	temporary, err := writeTemporary(filepath.Dir(path), ".change-ip.man-*", data, mode)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
