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
		return strings.Replace(strings.Replace(problem, "outbound source is ", "исходящий IP: ", 1), ", expected ", ", ожидается ", 1)
	}
	if strings.HasPrefix(problem, "potential persistence conflict:") {
		return strings.Replace(problem, "potential persistence conflict:", "возможен конфликт автозагрузки:", 1)
	}
	if strings.HasPrefix(problem, "unfinished transaction:") {
		return strings.Replace(problem, "unfinished transaction:", "незавершённая транзакция:", 1)
	}
	if strings.HasPrefix(problem, "return path for IP ") {
		return strings.Replace(problem, "return path for IP ", "не настроен ответный трафик для IP ", 1)
	}
	if strings.HasPrefix(problem, "IP ") && strings.Contains(problem, " is configured on multiple interfaces:") {
		return strings.Replace(problem, " is configured on multiple interfaces:", " настроен сразу на нескольких подключениях:", 1)
	}
	return problem
}

func (u *UI) localizeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if u.language != russian {
		return message
	}
	switch {
	case strings.HasPrefix(message, "no usable IPv4 address on "):
		return "На выбранном подключении нет IPv4-адреса. Сначала добавьте адрес от провайдера."
	case message == "no usable default IPv4 route":
		return "Не найдено активное подключение к интернету. Проверьте сетевые настройки провайдера."
	case strings.HasPrefix(message, "no gateway is configured for "):
		return "Не найден шлюз. Укажите шлюз из панели или инструкции провайдера."
	case strings.HasPrefix(message, "prefix required for new IP "):
		return "Для нового IP нужен префикс сети из панели провайдера, например /24 или /32."
	case strings.HasPrefix(message, "invalid gateway "):
		return "Шлюз указан неверно. Скопируйте IPv4-шлюз из панели провайдера."
	case strings.Contains(message, " exists as /") && strings.Contains(message, ", not /"):
		return "Этот IP уже настроен с другим префиксом. Используйте значение, выданное провайдером, или выберите IP из списка."
	case strings.HasPrefix(message, "IP ") && strings.Contains(message, " is configured on more than one interface"):
		parts := strings.SplitN(message, " is configured", 2)
		return parts[0] + " добавлен сразу на несколько подключений. Оставьте его только на подключении, назначенном провайдером."
	case strings.HasPrefix(message, "IP ") && strings.Contains(message, " is already configured on "):
		parts := strings.SplitN(message, " is already configured on ", 2)
		owner := strings.SplitN(parts[1], ";", 2)[0]
		return parts[0] + " уже настроен на " + owner + ". Выберите это подключение вместо повторного добавления IP."
	case message == "no usable network interfaces":
		return "Не найдено подходящих сетевых подключений."
	default:
		return message
	}
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
