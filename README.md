<p align="center">
  <img src="assets/readme-header.png" alt="ChangeIP">
</p>

<h1 align="center">ChangeIP</h1>

<p align="center">
  Делает выданный провайдером IPv4 основным адресом исходящего трафика
</p>

<p align="center">Русский · <a href="#english-version">English</a></p>

ChangeIP переключает IPv4, с которого уходит исходящий трафик. Адрес уже может висеть на интерфейсе — или его нужно сначала добавить локально.

Скрипт не заказывает адреса у провайдера, не открывает порты, не настраивает Docker и не трогает firewall. Уже существующие адреса не удаляет.

## Установка из локальной копии

```bash
sudo bash ./install.sh
sudo change_ip
```

## Установка на новый VDS из raw


```bash
curl -fsSL https://raw.githubusercontent.com/ReeA11/change-ip/master/install.sh | sudo bash
```

Инсталлятор ничего не меняет в сети. После установки запустите `sudo change_ip`.

## Использование

Интерактивный мастер:

```bash
sudo change_ip
```

Предпросмотр и применение из командной строки:

```bash
sudo change_ip --dry-run 111.222.111.222/27 --gateway 111.222.111.222 -i enp1s0f0
sudo change_ip 111.222.111.222/27 --gateway 111.222.111.222 -i enp1s0f0
```

Для нового адреса префикс и шлюз берите из панели провайдера. Скрипт их сам не угадывает.

Полезные команды:

```bash
sudo change_ip status
sudo change_ip doctor
sudo change_ip rollback
```

На Debian и Ubuntu постоянство обеспечивается сгенерированным systemd-сервисом. После поднятия сети он добавляет выбранный адрес и явно выставляет маршрут по умолчанию, шлюз и source IP. Машину скрипт сам не перезагружает.

Поддерживается только IPv4. IPv6 не затрагивается.

---

## English version

ChangeIP makes a provider-assigned IPv4 address the default source for outgoing traffic. The address may already be present on the interface, or it may need to be added locally first.

The script does not request addresses from the provider, open ports, configure Docker, or change the firewall. Existing addresses are not removed.

### Install from a local copy

```bash
sudo bash ./install.sh
sudo change_ip
```

### Install on a new VDS from raw


```bash
curl -fsSL https://raw.githubusercontent.com/ReeA11/change-ip/master/install.sh | sudo bash
```

The installer does not change the network. Run `sudo change_ip` after installation.

### Usage

Interactive wizard:

```bash
sudo change_ip
```

Preview and apply from the command line:

```bash
sudo change_ip --dry-run 111.222.111.222/27 --gateway 111.222.111.222 -i enp1s0f0
sudo change_ip 111.222.111.222 --gateway 111.222.111.222 -i enp1s0f0
```

For a new address, get the prefix and gateway from the provider panel. The script does not guess them.

Useful commands:

```bash
sudo change_ip status
sudo change_ip doctor
sudo change_ip rollback
```

On Debian and Ubuntu, persistence is provided by a generated systemd service. After the network is up, it adds the selected address and explicitly restores the default route, gateway, and source IP. The script never reboots the machine.

Only IPv4 is supported. IPv6 is left untouched.
