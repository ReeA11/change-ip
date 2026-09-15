<p align="center"><img src="assets/readme-header.png" alt="ChangeIP"></p>

# ChangeIP 3.0

ChangeIP делает выданный провайдером IPv4 явным source-адресом default route Linux-сервера.

Существующие IPv4 сохраняются; IPv6, firewall, Docker и provider configuration не изменяются. ChangeIP не перезагружает сервер.

## Установка

```bash
curl -fsSL https://raw.githubusercontent.com/ReeA11/change-ip/master/install.sh | sudo sh
```

## Обновление

```bash
curl -fsSL https://raw.githubusercontent.com/ReeA11/change-ip/master/update.sh | sudo sh
```

## Использование

```bash
sudo change-ip
sudo change-ip 176.96.136.246/25 --gateway 176.96.136.129 --interface eth0
sudo change-ip --dry-run 176.96.136.246/25 --gateway 176.96.136.129
sudo change-ip status
sudo change-ip doctor
sudo change-ip rollback
```

Поддерживаются `--prefix`, `--profile`, `--runtime-only`, `--yes`, `--check-egress`, `--verbose` и positional interface старого CLI. Для нового адреса prefix обязателен: ChangeIP его не угадывает. Формат profile:

```text
# IP/PREFIX          GATEWAY
176.96.136.246/25    176.96.136.129
```

Профиль не может быть group/world-writable. Gateway вне prefix и `/32` поддерживаются через link-scope host route и `onlink`. Metric и routing table выбранного default route сохраняются. При нескольких default routes выбирается допустимый маршрут с лучшим metric; явно указанный interface ограничивает выбор.

Без `--runtime-only` создаются versioned JSON backup в `/root/change-ip-backup.*`, apply-config в `/usr/local/lib/change-ip/` и systemd oneshot в `/etc/systemd/system/`. Unit запускает сам Go-бинарник после поднятия сети — shell apply-script не используется. Ошибка runtime, persistence или critical verification запускает rollback. Pending manifest позволяет `doctor` обнаружить оборванную операцию.

Для SSH-запуска используется transient `systemd-run`, если он доступен. `--dry-run` выполняет только discovery, validation и planning: backup и системные файлы не создаются.

## Сборка и тесты

```bash
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/change-ip-linux-amd64 ./cmd/change-ip
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/change-ip-linux-arm64 ./cmd/change-ip
sudo unshare --mount --net bash -c 'mount -t tmpfs tmpfs /run && mkdir -p /run/netns && CGO_ENABLED=0 go test -count=1 -tags=integration ./integration'
```

Integration tests создают disposable network namespaces/veth и требуют root/CAP_NET_ADMIN. В репозитории остаётся только Go implementation; поведенческий inventory 2.6 сохранён как миграционная документация.

---

## English

ChangeIP makes a provider-assigned IPv4 the explicit source of a Linux server's default route. 

Existing IPv4 addresses are preserved. IPv6, firewall, Docker, provider configuration, and reboot are left alone. A prefix is mandatory for a new address and is never guessed. Off-subnet gateways and `/32` addresses use a link-scope host route plus `onlink`; the selected route's metric and table are preserved.

The commands and flags are shown above. With no arguments, an interactive wizard starts. Persistence is a systemd oneshot invoking the Go binary with strict JSON configuration. Changes are backed up under `/root/change-ip-backup.*`; failures trigger rollback. `status` reports runtime and desired boot state, and `doctor` reports drift, failed/missing units, and unfinished transactions.

The release installer selects amd64 or arm64 and verifies checksums. It never builds on the target host or changes the network.

To upgrade or migrate a pre-3.0 Bash installation, run `curl -fsSL https://raw.githubusercontent.com/ReeA11/change-ip/master/update.sh | sudo sh`. The verified Go binary is installed atomically before legacy commands are removed or replaced with compatibility symlinks.
