package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const languagePath = "/etc/change-ip/language"

type language string

const (
	english language = "en"
	russian language = "ru"
)

func loadLanguage(path string) language {
	data, err := os.ReadFile(path)
	if err != nil {
		return english
	}
	if language(strings.TrimSpace(string(data))) == russian {
		return russian
	}
	return english
}

func saveLanguage(path string, lang language) error {
	if lang != english && lang != russian {
		return fmt.Errorf("unsupported language %q", lang)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create language configuration directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".language-*")
	if err != nil {
		return fmt.Errorf("create language configuration: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := fmt.Fprintln(tmp, lang); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install language configuration: %w", err)
	}
	return nil
}

func (u *UI) t(en, ru string) string {
	if u.language == russian {
		return ru
	}
	return en
}

func (u *UI) switchLanguage() error {
	next := russian
	if u.language == russian {
		next = english
	}
	if err := saveLanguage(languagePath, next); err != nil {
		return err
	}
	u.language = next
	return nil
}

func (u *UI) localizeProblem(problem string) string {
	if u.language != russian {
		return problem
	}
	switch problem {
	case "apply configuration is missing":
		return "конфигурация автозагрузки отсутствует"
	case "gateway differs from desired state":
		return "шлюз отличается от сохранённого состояния"
	case "default route table/metric differs from desired state":
		return "таблица или метрика default route отличается от сохранённой"
	case "persistence unit is missing":
		return "systemd unit автозагрузки отсутствует"
	case "persistence unit is not enabled":
		return "systemd unit автозагрузки не включён"
	case "persistence unit failed":
		return "systemd unit автозагрузки завершился с ошибкой"
	case "gateway route is missing or uses another interface":
		return "маршрут до шлюза отсутствует или использует другой интерфейс"
	}
	if strings.HasPrefix(problem, "target IP ") {
		return "целевой IP отсутствует: " + strings.TrimSuffix(strings.TrimPrefix(problem, "target IP "), " is absent")
	}
	if strings.HasPrefix(problem, "outbound source is ") {
		return strings.Replace(strings.Replace(problem, "outbound source is ", "исходящий source: ", 1), ", expected ", ", ожидается ", 1)
	}
	if strings.HasPrefix(problem, "potential persistence conflict:") {
		return strings.Replace(problem, "potential persistence conflict:", "возможен конфликт автозагрузки:", 1)
	}
	if strings.HasPrefix(problem, "unfinished transaction:") {
		return strings.Replace(problem, "unfinished transaction:", "незавершённая транзакция:", 1)
	}
	return problem
}

func (u *UI) localizeState(state string) string {
	if u.language != russian {
		return state
	}
	switch state {
	case "enabled":
		return "включён"
	case "disabled":
		return "отключён"
	case "active":
		return "активен"
	case "inactive":
		return "неактивен"
	case "failed":
		return "ошибка"
	case "missing":
		return "отсутствует"
	case "unavailable":
		return "недоступен"
	default:
		return state
	}
}
