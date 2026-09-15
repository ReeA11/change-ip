package ui

import (
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ReeA11/change-ip/internal/app"
	"github.com/ReeA11/change-ip/internal/backup"
	"github.com/ReeA11/change-ip/internal/diagnostics"
	"github.com/ReeA11/change-ip/internal/network"
	"github.com/ReeA11/change-ip/internal/transaction"
)

type UI struct {
	app      *app.Application
	term     *terminal
	version  string
	signals  <-chan os.Signal
	resize   chan os.Signal
	color    bool
	quit     bool
	language language
}

func CanRun(in, out *os.File) bool { return isTTY(in) && isTTY(out) }

func Run(a *app.Application, version string, signals <-chan os.Signal) error {
	if !CanRun(os.Stdin, os.Stdout) {
		return fmt.Errorf("interactive mode requires a TTY")
	}
	t, err := openTerminal(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	u := &UI{app: a, term: t, version: version, signals: signals, resize: make(chan os.Signal, 1), language: loadLanguage(languagePath)}
	_, noColor := os.LookupEnv("NO_COLOR")
	u.color = !noColor
	signal.Notify(u.resize, syscall.SIGWINCH)
	defer signal.Stop(u.resize)
	defer t.close()
	return u.home()
}

func (u *UI) home() error {
	selected := 0
	for {
		items := []string{
			u.t("Change address", "Сменить адрес"),
			u.t("Status", "Статус"),
			u.t("Doctor", "Диагностика"),
			u.t("Rollback", "Откат"),
			u.t("Language · English", "Язык · Русский"),
			u.t("Exit", "Выход"),
		}
		status, err := u.app.Collect("")
		if err != nil {
			return err
		}
		for {
			u.term.draw(u.renderHome(status, items, selected))
			k, quit := u.event()
			if quit {
				return nil
			}
			switch k {
			case keyUp:
				selected = previous(selected, len(items))
			case keyDown:
				selected = next(selected, len(items))
			case keyEnter:
				switch selected {
				case 0:
					if err := u.changeAddress(status); err != nil {
						u.message("ChangeIP", "✕ "+err.Error(), "")
					}
				case 1:
					u.detail(status, false)
				case 2:
					u.detail(status, true)
				case 3:
					u.rollback(status)
				case 4:
					if err := u.switchLanguage(); err != nil {
						u.message("ChangeIP", u.t("Could not save language", "Не удалось сохранить язык")+":\n"+err.Error(), "")
					}
				case 5:
					return nil
				}
				if u.quit {
					return nil
				}
				goto refresh
			case keyQuit:
				return nil
			}
		}
	refresh:
	}
}

func (u *UI) renderHome(s diagnostics.Status, items []string, selected int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n\n", u.title(), u.t("Network", "Сеть"))
	u.writeNetwork(&b, s.State)
	fmt.Fprintf(&b, "\n%s\n\n", u.t("IPv4 addresses", "IPv4-адреса"))
	u.writeAddresses(&b, s.State)
	persistence := u.t("not configured", "не настроена")
	if s.Desired != nil && s.UnitEnabled == "enabled" && s.UnitActive != "failed" {
		persistence = u.t("enabled", "включена")
	} else if s.Desired != nil || s.UnitEnabled != "missing" {
		persistence = u.t("warning", "предупреждение")
	}
	problems := u.problems(s)
	health := u.green(u.t("✓ healthy", "✓ исправно"))
	if len(problems) > 0 {
		health = u.yellow(fmt.Sprintf(u.t("! %d problems", "! проблем: %d"), len(problems)))
	}
	fmt.Fprintf(&b, "\n%-17s%s\n%-17s%s\n\n", u.t("Persistence", "Автозагрузка"), persistence, u.t("Health", "Состояние"), health)
	writeMenu(&b, items, selected, u)
	fmt.Fprintln(&b, "\n"+u.gray(u.t("↑↓ navigate   enter select   q quit", "↑↓ навигация   enter выбрать   q выход")))
	return b.String()
}

func (u *UI) changeAddress(s diagnostics.Status) error {
	items := make([]string, 0, len(s.State.Addresses)+1)
	for _, address := range s.State.Addresses {
		label := address.Prefix.String()
		if address.Prefix.Addr() == s.State.OutboundSource {
			label += u.t("    current", "    текущий")
		}
		items = append(items, label)
	}
	items = append(items, u.t("+ Add another IPv4", "+ Добавить другой IPv4"))
	selected, ok := u.choose("ChangeIP\n\n"+u.t("Select IPv4 for ", "Выберите IPv4 для ")+s.State.Interface, items, u.t("↑↓ navigate   enter select   esc back", "↑↓ навигация   enter выбрать   esc назад"))
	if !ok {
		return nil
	}
	o := app.Options{Interface: s.State.Interface, Profile: "/etc/change-ip-addresses.conf"}
	var plan transaction.Plan
	planned := false
	if selected < len(s.State.Addresses) {
		o.Target = s.State.Addresses[selected].Prefix.String()
	} else {
		address, ok := u.input(u.t("Add IPv4", "Добавление IPv4"), u.t("Address", "Адрес"), "")
		if !ok {
			return nil
		}
		o.Target = address
		if inferred, resolveErr := u.app.Resolve(o); resolveErr == nil {
			plan, planned = inferred, true
		} else {
			prefix, ok := u.input(u.t("Add IPv4", "Добавление IPv4"), u.t("Prefix", "Префикс"), "")
			if !ok {
				return nil
			}
			o.Prefix = prefix
			if inferred, resolveErr = u.app.Resolve(o); resolveErr == nil {
				plan, planned = inferred, true
			} else if strings.Contains(resolveErr.Error(), "no IPv4 gateway") {
				gateway, gatewayOK := u.input(u.t("Add IPv4", "Добавление IPv4"), u.t("Gateway", "Шлюз"), "")
				if !gatewayOK {
					return nil
				}
				o.Gateway = gateway
			}
		}
	}
	if !planned {
		var err error
		plan, err = u.app.Resolve(o)
		if err != nil {
			return err
		}
	}
	if !u.review(plan) {
		return nil
	}
	completed := make(map[string]bool)
	u.term.draw(u.renderProgress(plan, completed))
	oldOut, oldErr := u.app.Out, u.app.Err
	var log strings.Builder
	u.app.Out, u.app.Err = &log, &log
	o.Yes = true
	o.Progress = func(stage string) {
		completed[stage] = true
		u.term.draw(u.renderProgress(plan, completed))
	}
	applySignals := make(chan os.Signal, 1)
	applyDone := make(chan struct{})
	interrupted := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case k, ok := <-u.term.keys:
				if !ok || k == keyInterrupt {
					interrupted <- struct{}{}
					applySignals <- syscall.SIGINT
					return
				}
			case sig := <-u.signals:
				interrupted <- struct{}{}
				applySignals <- sig
				return
			case <-applyDone:
				return
			}
		}
	}()
	backupDir, applyErr := u.app.Apply(o, applySignals)
	close(applyDone)
	u.app.Out, u.app.Err = oldOut, oldErr
	select {
	case <-interrupted:
		u.quit = true
		return nil
	default:
	}
	if applyErr != nil {
		rollback := u.t("– not required", "– не требовался")
		if backupDir != "" {
			rollback = u.t("✓ completed automatically", "✓ выполнен автоматически")
		}
		if strings.Contains(applyErr.Error(), "rollback:") || strings.Contains(applyErr.Error(), "rollback persistence") {
			rollback = u.t("✕ incomplete", "✕ выполнен не полностью")
		}
		u.message("ChangeIP\n\n"+u.t("✕ Operation failed", "✕ Операция завершилась ошибкой"), applyErr.Error(), u.t("Rollback", "Откат")+"\n  "+rollback)
		return nil
	}
	fresh, err := u.app.Collect(plan.Target.Interface)
	if err != nil {
		return err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "%s\n\n%s → %s\n\n%s\n\n", u.t("✓ IP changed successfully", "✓ IP успешно изменён"), plan.Before.OutboundSource, fresh.State.OutboundSource, u.t("Network", "Сеть"))
	u.writeNetwork(&body, fresh.State)
	if backupDir != "" {
		fmt.Fprintf(&body, "\n%-17s%s\n", u.t("Backup", "Резервная копия"), filepath.Base(backupDir))
	}
	u.message("ChangeIP", body.String(), "")
	return nil
}

func (u *UI) review(p transaction.Plan) bool {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s  →  %s\n\n", u.title(), p.Before.OutboundSource, p.Target.OutboundSource)
	fmt.Fprintf(&b, "%-15s%s\n%-15s/%d\n%-15s%s", u.t("Interface", "Интерфейс"), p.Target.Interface, u.t("Prefix", "Префикс"), targetPrefix(p).Bits(), u.t("Gateway", "Шлюз"), p.Target.DefaultRoute.Gateway)
	if p.Target.DefaultRoute.OnLink {
		fmt.Fprint(&b, " · on-link")
	}
	fmt.Fprintf(&b, "\n%-15s%s\n\n%s\n\n", u.t("Route", "Маршрут"), u.routeLabel(p.Target.DefaultRoute), u.t("Changes", "Изменения"))
	fmt.Fprintln(&b, u.t("  ✓ Existing addresses preserved", "  ✓ Существующие адреса будут сохранены"))
	if p.AddressToAdd != nil {
		fmt.Fprintf(&b, u.t("  • Address %s will be added\n", "  • Будет добавлен адрес %s\n"), p.AddressToAdd)
	}
	if p.Before.DefaultRoute.Gateway == p.Target.DefaultRoute.Gateway {
		fmt.Fprintln(&b, u.t("  ✓ Gateway unchanged", "  ✓ Шлюз не изменится"))
	} else {
		fmt.Fprintf(&b, "\n%s\n"+u.t("  Gateway will change: %s → %s\n  An incorrect gateway can disconnect this server.\n", "  Шлюз изменится: %s → %s\n  Неверный шлюз может отключить сервер от сети.\n"), u.yellow(u.t("Warning", "Предупреждение")), p.Before.DefaultRoute.Gateway, p.Target.DefaultRoute.Gateway)
	}
	if p.HostRouteToAdd != nil {
		fmt.Fprintf(&b, u.t("  • /32 gateway route will be created for %s\n", "  • Для шлюза %s будет создан маршрут /32\n"), p.Target.DefaultRoute.Gateway)
	}
	if targetPrefix(p).Bits() == 32 {
		fmt.Fprintln(&b, u.t("  ! Target address uses /32", "  ! Целевой адрес использует /32"))
	}
	if p.Before.OutboundSource != p.Target.OutboundSource {
		fmt.Fprintln(&b, u.t("  • Default source will change", "  • Исходящий source-адрес изменится"))
	} else {
		fmt.Fprintln(&b, u.t("  ✓ Default source already selected", "  ✓ Этот source-адрес уже выбран"))
	}
	fmt.Fprintln(&b, u.t("  • Persistence will be updated", "  • Автозагрузка будет обновлена"))
	choice, ok := u.choose(b.String(), []string{u.t("Apply", "Применить"), u.t("Cancel", "Отмена")}, u.t("↑↓ navigate   enter select   esc cancel", "↑↓ навигация   enter выбрать   esc отмена"))
	return ok && choice == 0
}

func (u *UI) renderProgress(p transaction.Plan, complete map[string]bool) string {
	mark := func(stage string) string {
		if complete[stage] {
			return "✓"
		}
		return "•"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n\n", u.title(), u.t("Applying configuration", "Применение конфигурации"))
	if p.AddressToAdd != nil {
		fmt.Fprintf(&b, "%s %s %s\n", mark(app.ProgressAddress), u.t("Address", "Адрес"), p.AddressToAdd)
	} else {
		fmt.Fprintln(&b, u.t("– Address already exists", "– Адрес уже существует"))
	}
	if p.HostRouteToAdd != nil {
		fmt.Fprintf(&b, "%s %s\n", mark(app.ProgressGateway), u.t("Gateway route", "Маршрут до шлюза"))
	}
	fmt.Fprintf(&b, "%s %s\n%s %s\n%s %s\n", mark(app.ProgressDefault), u.t("Default route", "Маршрут по умолчанию"), mark(app.ProgressPersistence), u.t("Persistence", "Автозагрузка"), mark(app.ProgressVerify), u.t("Source verification", "Проверка source-адреса"))
	return b.String()
}

func (u *UI) detail(s diagnostics.Status, doctor bool) {
	var b strings.Builder
	if doctor {
		fmt.Fprintf(&b, "ChangeIP %s\n\n", u.t("doctor", "диагностика"))
		problems := u.problems(s)
		checks := []struct {
			name    string
			matches []string
		}{
			{u.t("Address", "Адрес"), []string{"target IP"}},
			{u.t("Outbound source", "Исходящий source"), []string{"outbound source"}},
			{u.t("Gateway", "Шлюз"), []string{"gateway"}},
			{u.t("Default route", "Маршрут по умолчанию"), []string{"default route"}},
			{u.t("Persistence", "Автозагрузка"), []string{"persistence", "apply configuration", "unfinished transaction"}},
			{"systemd", []string{"unit"}},
		}
		for _, check := range checks {
			mark := "✓"
			for _, problem := range problems {
				for _, match := range check.matches {
					if strings.Contains(problem, match) {
						mark = "✕"
					}
				}
			}
			fmt.Fprintf(&b, "%s %s\n", mark, check.name)
		}
		if len(problems) == 0 {
			fmt.Fprintln(&b, "\n"+u.t("No problems found.", "Проблем не обнаружено."))
		} else {
			fmt.Fprintf(&b, "\n%s\n\n", u.t("Problems", "Проблемы"))
			for i, problem := range problems {
				fmt.Fprintf(&b, "%d. %s\n", i+1, u.localizeProblem(problem))
			}
		}
	} else {
		fmt.Fprintf(&b, "ChangeIP %s\n\n%s\n\n", u.t("status", "статус"), u.t("Network", "Сеть"))
		u.writeNetwork(&b, s.State)
		fmt.Fprintf(&b, "\n%s\n\n", u.t("IPv4 addresses", "IPv4-адреса"))
		u.writeAddresses(&b, s.State)
		fmt.Fprintf(&b, "\n%s\n\n", u.t("Persistence", "Автозагрузка"))
		desired := u.t("not configured", "не настроена")
		if s.Desired != nil {
			desired = s.Desired.OutboundSource.String()
		}
		fmt.Fprintf(&b, "  %-15s%s\n  %-15s%s · %s\n", u.t("Desired IP", "Желаемый IP"), desired, u.t("Unit", "Unit systemd"), u.localizeState(s.UnitEnabled), u.localizeState(s.UnitActive))
	}
	u.waitBack(b.String())
}

func (u *UI) rollback(current diagnostics.Status) {
	dirs, err := u.app.Backups()
	if err != nil || len(dirs) == 0 {
		u.message(u.t("Rollback", "Откат"), u.t("No backups found.", "Резервные копии не найдены."), "")
		return
	}
	labels := make([]string, 0, len(dirs))
	manifests := make([]backup.Manifest, 0, len(dirs))
	validDirs := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		m, readErr := backup.Read(filepath.Join(dir, "manifest.json"))
		if readErr != nil {
			continue
		}
		labels = append(labels, fmt.Sprintf("%s   %s   %s", m.Created.Local().Format("2006-01-02 15:04"), m.Interface, m.Before.OutboundSource))
		manifests = append(manifests, m)
		validDirs = append(validDirs, dir)
	}
	selected, ok := u.choose(u.t("Rollback\n\nSelect backup", "Откат\n\nВыберите резервную копию"), labels, u.t("↑↓ navigate   enter select   esc back", "↑↓ навигация   enter выбрать   esc назад"))
	if !ok || len(labels) == 0 {
		return
	}
	m := manifests[selected]
	header := fmt.Sprintf(u.t("Rollback\n\nCurrent\n%s\n\nRestore\n%s\n\nInterface\n%s", "Откат\n\nТекущий\n%s\n\nВосстановить\n%s\n\nИнтерфейс\n%s"), current.State.OutboundSource, m.Before.OutboundSource, m.Interface)
	choice, ok := u.choose(header, []string{u.t("Restore", "Восстановить"), u.t("Cancel", "Отмена")}, u.t("↑↓ navigate   enter select   esc cancel", "↑↓ навигация   enter выбрать   esc отмена"))
	if !ok || choice != 0 {
		return
	}
	u.term.draw(u.t("Rollback\n\nRestoring configuration...\n", "Откат\n\nВосстановление конфигурации...\n"))
	if err := u.app.Rollback(validDirs[selected]); err != nil {
		u.message(u.t("Rollback\n\n✕ incomplete", "Откат\n\n✕ выполнен не полностью"), err.Error(), "")
		return
	}
	u.message(u.t("Rollback", "Откат"), u.t("✓ completed\n\nNetwork configuration was restored.", "✓ выполнен\n\nСетевая конфигурация восстановлена."), "")
}

func (u *UI) choose(header string, items []string, footer string) (int, bool) {
	selected := 0
	for {
		var b strings.Builder
		fmt.Fprintln(&b, header+"\n")
		writeMenu(&b, items, selected, u)
		fmt.Fprintln(&b, "\n"+u.gray(footer))
		u.term.draw(b.String())
		k, quit := u.event()
		if quit || k == keyEscape {
			return 0, false
		}
		switch k {
		case keyUp:
			selected = previous(selected, len(items))
		case keyDown:
			selected = next(selected, len(items))
		case keyEnter:
			return selected, true
		}
	}
}

func (u *UI) input(title, label, initial string) (string, bool) {
	value := initial
	for {
		u.term.draw(fmt.Sprintf("%s\n\n%s\n› %s_\n\n%s", title, label, value, u.gray(u.t("Enter continue   esc cancel", "Enter продолжить   esc отмена"))))
		k, quit := u.event()
		if quit || k == keyEscape {
			return "", false
		}
		switch k {
		case keyEnter:
			return strings.TrimSpace(value), true
		case keyBackspace:
			if len(value) > 0 {
				value = value[:len(value)-1]
			}
		default:
			if r, ok := typedRune(k); ok && len(value) < 64 && r >= 32 && r < 127 {
				value += string(r)
			}
		}
	}
}

func (u *UI) message(title, body, extra string) {
	text := title + "\n\n" + body
	if extra != "" {
		text += "\n\n" + extra
	}
	u.waitBack(text)
}

func (u *UI) waitBack(content string) {
	for {
		u.term.draw(content + "\n\n" + u.gray(u.t("Press Enter or esc to return", "Нажмите Enter или esc, чтобы вернуться")))
		k, quit := u.event()
		if quit || k == keyEnter || k == keyEscape {
			return
		}
	}
}

func (u *UI) event() (key, bool) {
	for {
		select {
		case k, ok := <-u.term.keys:
			if !ok || k == keyInterrupt {
				u.quit = true
				return keyQuit, true
			}
			return k, false
		case <-u.resize:
			return keyUnknown, false
		case <-u.signals:
			u.quit = true
			return keyQuit, true
		}
	}
}

func (u *UI) problems(s diagnostics.Status) []string {
	problems := diagnostics.Problems(s)
	dirs, err := u.app.Backups()
	if err != nil {
		return problems
	}
	for _, dir := range dirs {
		manifest, readErr := backup.Read(filepath.Join(dir, "manifest.json"))
		if readErr == nil && manifest.Status == "pending" {
			problems = append(problems, "unfinished transaction: "+filepath.Base(dir))
		}
	}
	return problems
}

func (u *UI) title() string          { return fmt.Sprintf("ChangeIP %s", u.version) }
func (u *UI) gray(s string) string   { return u.paint("\x1b[90m", s) }
func (u *UI) green(s string) string  { return u.paint("\x1b[32m", s) }
func (u *UI) yellow(s string) string { return u.paint("\x1b[33m", s) }
func (u *UI) accent(s string) string { return u.paint("\x1b[36m", s) }
func (u *UI) paint(code, s string) string {
	if !u.color {
		return s
	}
	return code + s + "\x1b[0m"
}

func writeMenu(b *strings.Builder, items []string, selected int, u *UI) {
	for i, item := range items {
		if i == selected {
			fmt.Fprintf(b, "%s %s\n", u.accent("›"), u.accent(item))
		} else {
			fmt.Fprintf(b, "  %s\n", item)
		}
	}
}

func (u *UI) writeNetwork(b *strings.Builder, s network.State) {
	fmt.Fprintf(b, "  %-15s%s\n  %-15s%s\n  %-15s%s\n  %-15s%s\n", u.t("Interface", "Интерфейс"), s.Interface, u.t("Outbound", "Исходящий IP"), s.OutboundSource, u.t("Gateway", "Шлюз"), s.DefaultRoute.Gateway, u.t("Route", "Маршрут"), u.routeLabel(s.DefaultRoute))
}

func (u *UI) writeAddresses(b *strings.Builder, s network.State) {
	for _, address := range s.Addresses {
		mark, suffix := " ", ""
		if address.Prefix.Addr() == s.OutboundSource {
			mark, suffix = "●", u.t("    current outbound", "    текущий исходящий")
		}
		if address.Dynamic {
			suffix += u.t(" · dynamic", " · динамический")
		}
		fmt.Fprintf(b, "  %s %-18s%s\n", mark, address.Prefix, suffix)
	}
}

func routeLabel(r network.Route) string {
	table := "table " + strconv.Itoa(r.Table)
	if r.Table == 254 {
		table = "main"
	}
	label := fmt.Sprintf("%s · metric %d", table, r.Metric)
	if r.OnLink {
		label += " · on-link"
	}
	return label
}

func (u *UI) routeLabel(r network.Route) string {
	if u.language != russian {
		return routeLabel(r)
	}
	table := "таблица " + strconv.Itoa(r.Table)
	if r.Table == 254 {
		table = "main"
	}
	label := fmt.Sprintf("%s · метрика %d", table, r.Metric)
	if r.OnLink {
		label += " · on-link"
	}
	return label
}

func targetPrefix(p transaction.Plan) netip.Prefix {
	if p.AddressToAdd != nil {
		return *p.AddressToAdd
	}
	for _, address := range p.Target.Addresses {
		if address.Prefix.Addr() == p.Target.OutboundSource {
			return address.Prefix
		}
	}
	return netip.PrefixFrom(p.Target.OutboundSource, 32)
}

func previous(i, size int) int {
	if size == 0 {
		return 0
	}
	return (i + size - 1) % size
}
func next(i, size int) int {
	if size == 0 {
		return 0
	}
	return (i + 1) % size
}
