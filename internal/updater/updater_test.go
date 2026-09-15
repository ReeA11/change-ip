package updater

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateInstallsLatestAndRemovesUnderscoreCommands(t *testing.T) {
	binary := []byte("verified release binary")
	man := []byte("release manual")
	checksums := checksumLine("change-ip-linux-amd64", binary) + checksumLine("change-ip.8", man)
	client := releaseClient(map[string][]byte{
		"/checksums.txt":         []byte(checksums),
		"/change-ip-linux-amd64": binary,
		"/change-ip.8":           man,
	})

	prefix := t.TempDir()
	for _, old := range []string{filepath.Join(prefix, "bin", "change_ip"), filepath.Join(prefix, "sbin", "change_ip")} {
		if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(old, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	u := New(Config{
		BaseURL:        "https://release.invalid",
		Prefix:         prefix,
		Arch:           "amd64",
		CurrentVersion: "3.0.1",
		Client:         client,
		ValidateBinary: func(path string) (string, error) {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(binary) {
				return "", fmt.Errorf("unexpected candidate")
			}
			return "3.0.2", nil
		},
	})
	version, err := u.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != "3.0.2" {
		t.Fatalf("version = %q", version)
	}
	installed, err := os.ReadFile(filepath.Join(prefix, "bin", "change-ip"))
	if err != nil || string(installed) != string(binary) {
		t.Fatalf("installed binary = %q, %v", installed, err)
	}
	for _, old := range []string{filepath.Join(prefix, "bin", "change_ip"), filepath.Join(prefix, "sbin", "change_ip")} {
		if _, err := os.Lstat(old); !os.IsNotExist(err) {
			t.Fatalf("deprecated command still exists: %s", old)
		}
	}
	link, err := os.Readlink(filepath.Join(prefix, "sbin", "change-ip"))
	if err != nil || link != "../bin/change-ip" {
		t.Fatalf("sbin link = %q, %v", link, err)
	}
}

func TestUpdateRejectsChecksumMismatchBeforeInstall(t *testing.T) {
	binary := []byte("tampered")
	man := []byte("manual")
	checksums := checksumLine("change-ip-linux-amd64", []byte("expected")) + checksumLine("change-ip.8", man)
	client := releaseClient(map[string][]byte{
		"/checksums.txt":         []byte(checksums),
		"/change-ip-linux-amd64": binary,
		"/change-ip.8":           man,
	})

	prefix := t.TempDir()
	u := New(Config{BaseURL: "https://release.invalid", Prefix: prefix, Arch: "amd64", Client: client})
	_, err := u.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(prefix, "bin", "change-ip")); !os.IsNotExist(statErr) {
		t.Fatalf("binary changed after failed verification: %v", statErr)
	}
}

func TestParseChecksumsRejectsDuplicatesAndPaths(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, input := range []string{
		hash + "  ../change-ip\n",
		hash + "  change-ip\n" + hash + "  change-ip\n",
		"invalid  change-ip\n",
	} {
		if _, err := parseChecksums(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func checksumLine(name string, data []byte) string {
	return fmt.Sprintf("%x  %s\n", sha256.Sum256(data), name)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func releaseClient(files map[string][]byte) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		data, ok := files[r.URL.Path]
		if !ok {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(bytes.NewReader(data)),
			Header:     make(http.Header),
		}, nil
	})}
}
