package ui

import (
	"os"
	"path/filepath"
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
