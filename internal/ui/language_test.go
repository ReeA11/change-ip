package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLanguageDefaultsToEnglishAndPersistsRussian(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "language")
	if got := loadLanguage(path); got != english {
		t.Fatalf("missing language = %q", got)
	}
	if err := saveLanguage(path, russian); err != nil {
		t.Fatal(err)
	}
	if got := loadLanguage(path); got != russian {
		t.Fatalf("saved language = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("language mode = %o", info.Mode().Perm())
	}
}

func TestInvalidLanguageFallsBackToEnglish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "language")
	if err := os.WriteFile(path, []byte("de\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadLanguage(path); got != english {
		t.Fatalf("invalid language = %q", got)
	}
}

func TestRussianNetworkErrorsAreActionable(t *testing.T) {
	u := &UI{language: russian}
	for _, tc := range []struct {
		err  string
		want string
	}{
		{"no gateway is configured for eth1; enter the gateway shown by your provider", "Укажите шлюз"},
		{"IP 104.167.197.74 is configured on more than one interface (eth0, eth1); keep it only on the provider-assigned interface", "несколько подключений"},
	} {
		if got := u.localizeError(errors.New(tc.err)); !strings.Contains(got, tc.want) {
			t.Fatalf("localized error %q does not contain %q", got, tc.want)
		}
	}
}
