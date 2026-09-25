# ChangeIP 3.2.0: техническое руководство для ИИ-агента

## 1. Статус документа и источник истины

Этот документ описывает реализацию ChangeIP 3.2.0 в текущем рабочем дереве. На
момент составления последний Git-тег — `v3.1.0`, а встроенная версия в
`cmd/change-ip/main.go` — `3.2.0-dev`. В release-сборке значение версии
переопределяется через `-ldflags` из Git-тега.

При расхождении документа и кода источником истины является код. Особенно
важны:

- `cmd/change-ip/main.go` — CLI, запуск через `systemd-run`, lock и сигналы;
- `internal/app/app.go` — discovery, построение операций, persistence и rollback;
- `internal/app/policy.go` — несколько интерфейсов и source-policy routing;
- `internal/transaction/transaction.go` — порядок сетевых изменений, verify и
  обратный порядок отката;
- `internal/network/` — модель сети и реализация Linux netlink;
- `internal/persist/systemd.go` — состояние после перезагрузки;
- `internal/backup/manifest.go` — формат резервной копии;
- `integration/netns_test.go` — наиболее близкая к реальной сети спецификация.

## 2. Назначение и границы инструмента

ChangeIP — Linux-утилита на Go, которая делает выданный провайдером IPv4
явным preferred source у выбранного default IPv4 route. В версии 3.2.0 она
также умеет:

- добавлять один или несколько IPv4 без смены исходящего адреса;
- менять шлюз, сохраняя исходящий адрес;
- выбирать другой интерфейс основным для исходящего трафика;
- строить отдельный return path для каждого IPv4 на нескольких интерфейсах;
- сохранять желаемое состояние через собственный systemd oneshot;
- показывать состояние, диагностировать drift и выполнять откат;
- обновлять бинарник из GitHub Release с проверкой SHA-256.

ChangeIP намеренно не делает следующее:

- не удаляет существующие IPv4 только ради смены исходящего адреса;
- не меняет IPv6, firewall, NAT, Docker или конфигурацию провайдера;
- не переписывает Netplan, NetworkManager, systemd-networkd, ifupdown или
  cloud-init;
- не угадывает префикс нового IPv4;
- не перезагружает сервер.

Смысл инструмента не в «замене IP на интерфейсе», а в согласованной настройке
адреса, default route, policy routing, persistence и проверяемого отката.

## 3. Платформа и зависимости

- ОС: Linux; файлы с netlink, terminal и flock имеют build tag `linux`.
- Go: версия модуля `1.24`.
- Основная библиотека: `github.com/vishvananda/netlink`.
- Runtime-изменения сети требуют netlink-привилегий, а обычный CLI явно требует
  EUID 0 перед применением плана.
- Постоянная конфигурация требует активного systemd. Без него разрешён только
  `--runtime-only`.
- Поддерживаемые release-архитектуры: `amd64`, `arm64`.

## 4. Архитектура

Поток зависимостей:

```text
CLI или inline TUI
        |
        v
internal/app.Application
  discovery -> resolve -> transaction.Plan
        |                       |
        |                       v
        |               transaction.Transaction
        |                 apply/verify/rollback
        v                       |
network.Backend <---------------+
        |
        v
Linux netlink

Application -> backup.Manifest
            -> persist.Systemd -> apply-profile при загрузке ОС
            -> diagnostics
```

### Пакеты

| Путь | Ответственность |
| --- | --- |
| `cmd/change-ip` | Разбор команд, lifecycle процесса, сигналы, lock, updater |
| `internal/app` | Use cases, discovery, валидация, план, backup/persistence |
| `internal/network` | Доменные типы, выбор маршрута, netlink backend |
| `internal/transaction` | Атомарно-подобное применение, verify, компенсирующий rollback |
| `internal/persist` | JSON desired state и systemd unit |
| `internal/backup` | Manifest v2 и снимки управляемых файлов |
| `internal/diagnostics` | Runtime/persisted state и список проблем |
| `internal/ui` | Inline raw-terminal TUI, English/Russian |
| `internal/updater` | Проверенное и атомарное обновление release-бинарника |
| `internal/platform` | Неблокирующий process-wide `flock` |

### Главные модели

`network.State` содержит:

- интерфейс и все его IPv4;
- вычисленный исходящий source;
- выбранный default route;
- отдельный link-scope `/32` до шлюза, если он нужен;
- все прочитанные routes;
- `ManagedRoutes` и `ManagedRules`, которые ChangeIP обязан восстановить после
  перезагрузки.

`transaction.Plan` отделяет планирование от мутации. В нём находятся исходное
и целевое состояния, добавляемые адреса, host route до шлюза, необходимость
замены default route, изменения конкурирующих routes, новые policy routes и
rules, флаг глобальной проверки и список старых persistence units для
отключения.

`transaction.Transaction` дополнительно считает только реально выполненные
шаги. Эти счётчики нужны, чтобы rollback удалял только объекты текущей
транзакции.

## 5. CLI и режимы работы

| Команда | Поведение |
| --- | --- |
| `change-ip` | TUI; stdin и stdout должны быть TTY |
| `change-ip NEW_IP[/PREFIX] [IFACE]` | Основная операция смены исходящего IPv4 |
| `change-ip apply ...` | Явная форма основной операции |
| `change-ip add-address IP/PREFIX ...` | Только добавляет адреса, default route не меняет |
| `change-ip set-gateway GATEWAY` | Меняет gateway, сохраняет текущий outbound source |
| `change-ip set-interface IFACE` | Делает интерфейс основным; `--source` выбирает его IPv4 |
| `change-ip status [IFACE]` | Runtime state и persistence state |
| `change-ip doctor [IFACE]` | Drift, policy routing, units, конфликты и pending backup |
| `change-ip rollback [BACKUP_DIR]` | Откат; без пути предлагает TTY-список backup |
| `change-ip update` | Проверенное обновление; требует root |
| `change-ip apply-profile --config FILE` | Внутренний boot-path systemd unit |

Общие флаги мутаций: `--interface/-i`, `--gateway/-g`, `--prefix/-p`,
`--dry-run`, `--runtime-only`, `--yes/-y`, `--check-egress`, `--verbose`.
Основная операция также использует `--profile`; для обычного CLI значение по
умолчанию — `/etc/change-ip-addresses.conf`.

В текущей реализации `--verbose` разбирается и сохраняется в `app.Options`, но
ещё не меняет вывод. Не считать его работающим диагностическим режимом без
отдельной реализации и тестов.

`--dry-run` выполняет discovery, validation и planning, но не требует root, не
берёт lock, не создаёт backup и ничего не меняет. `--runtime-only` меняет сеть,
но намеренно не создаёт ни persistence, ни дисковый backup.

## 6. Lifecycle процесса и защита SSH-сессии

Перед обычной мутацией CLI по возможности перезапускает себя так:

```text
systemd-run --quiet --wait --pty --same-dir \
  --setenv=CHANGE_IP_SYSTEMD_RUN=1 change-ip ...
```

Если команда была запущена через sudo, в transient unit передаётся
`SUDO_USER`. Это позволяет операции и автоматическому rollback продолжить
работу при обрыве SSH. Если нет TTY или `systemd-run`, CLI продолжает в текущем
процессе с предупреждением.

Все мутации и updater сериализованы lock-файлом `/run/change-ip.lock` через
`flock(LOCK_EX|LOCK_NB)`. Параллельная операция завершается ошибкой. CLI
перехватывает `SIGINT`, `SIGTERM`, `SIGHUP`; внутри активной транзакции сигнал
превращается в ошибку и запускает rollback. TUI также переводит Ctrl-C в этот
сигнальный канал и восстанавливает terminal mode/cursor при выходе.

## 7. Discovery и выбор интерфейса

`Application.interfaceFor` выбирает интерфейс в таком порядке:

1. Явный интерфейс: запрещён только `lo`, имя проверяется regexp
   `^[A-Za-z0-9_.:@-]{1,15}$`.
2. Интерфейс результата `RouteTo(1.1.1.1)`, если он не относится к служебным.
3. Лучший default route из `DefaultRoutes()`.

Из автоматического выбора, TUI и глобального policy scan исключаются `lo` и
имена с префиксами `docker`, `br-`, `veth`, `virbr`, `cni`, `flannel`, `cbr`,
`kube`, `wg`, `tun`, `utun`, `tap`, `ppp`, `nerdctl`. Явно указанный интерфейс,
кроме `lo`, этой фильтрацией не запрещён.

Если fallback-выбор видит несколько default routes, сортировка детерминирована:

1. меньшая metric; metric `0` считается наивысшим приоритетом;
2. меньший номер routing table;
3. имя интерфейса.

`NetlinkBackend.Snapshot` читает IPv4-адреса и все IPv4 routes интерфейса.
Активный default уточняется через kernel `RouteGet(1.1.1.1)`, чтобы не спутать
main table с policy table. Интерфейс без default route всё равно возвращает
валидный snapshot: это обязательное поведение 3.2.0 для дополнительных NIC
провайдера.

## 8. Разбор адреса, профиля и шлюза

Новый адрес принимается только как usable unicast IPv4. Запрещены unspecified,
loopback, multicast и `255.255.255.255`.

Префикс определяется только из одного из источников:

1. `IP/PREFIX` в аргументе;
2. `--prefix`;
3. совпадающая запись профиля;
4. уже существующий адрес на выбранном интерфейсе.

Если адрес новый и ни один источник не дал prefix, операция завершается
ошибкой: префикс никогда не угадывается. Несовпадение уже настроенного и
запрошенного prefix также является ошибкой.

Формат profile:

```text
# IP/PREFIX          GATEWAY
176.96.136.246/25    176.96.136.129
198.51.100.10/32     -
```

Profile должен быть обычным файлом и не может быть group/world writable.
Повторный IP запрещён даже при идентичной записи.

Для основной операции gateway выбирается так:

1. явный `--gateway`;
2. gateway default route выбранного интерфейса;
3. gateway из profile;
4. gateway активного подключения, только если он входит в prefix целевого или
   другого адреса выбранного интерфейса.

Если безопасного gateway нет, пользователь должен указать значение провайдера.
Для интерфейса вообще без default route целевой default создаётся в таблице
активного глобального route, обычно `main`/254.

## 9. Построение планов операций

### Смена исходящего IPv4 (`Resolve`)

1. Выбирается интерфейс и снимается `before`.
2. Разбираются target/prefix/profile.
3. Находится активный глобальный default route и gateway.
4. `transaction.BuildPlan` формирует целевой default route с явным `src`.
5. При смене интерфейса `prepareDefaultSwitch` настраивает приоритет routes.
6. `attachSourcePolicies` строит return paths для адресов всех допустимых NIC.

Существующие адреса сохраняются. Если target ещё отсутствует, он добавляется.

### Добавление адресов (`ResolveAddAddresses`)

Строится add-only plan через `BuildAddressPlan`. Он не меняет default route и
outbound source и не добавляет source-policy tables. Операция проверяет
дубликаты IP на других интерфейсах.

### Смена gateway (`ResolveGateway`)

Текущий outbound source обязан реально присутствовать на интерфейсе. Для его
prefix строится обычный route plan с новым gateway, после чего обновляются
source policies.

### Смена основного интерфейса (`ResolveDefaultInterface`)

Source выбирается в порядке:

1. явный `--source`;
2. `DefaultRoute.Source`;
3. вычисленный `OutboundSource`;
4. первый пригодный статический IPv4;
5. первый пригодный IPv4.

Если у интерфейса нет gateway, можно переиспользовать gateway активного
подключения только когда он входит в prefix одного из адресов этого интерфейса;
иначе gateway обязан ввести пользователь.

Старый default route обычно остаётся fallback-маршрутом. Новый получает metric
на единицу лучше активного, если активная metric больше нуля. Если активная
metric равна нулю, другие default routes с metric 0 в той же таблице временно
понижаются до metric 1 и записываются в `RouteChanges` для точного отката.
Persistence units других интерфейсов помечаются для отключения после успешной
записи нового unit.

## 10. Off-subnet и `/32` gateway

Если gateway не покрыт ни одним адресным prefix и ни одним прямым route
интерфейса, план добавляет:

```text
GATEWAY/32 dev IFACE scope link table TABLE
default via GATEWAY dev IFACE src TARGET onlink table TABLE
```

Host route создаётся раньше default route. При rollback он удаляется только
если был создан этой транзакцией. Это позволяет работать с типичной provider
схемой «адрес `/32`, gateway вне подсети».

## 11. Source-policy routing для нескольких интерфейсов

Цель `attachSourcePolicies` — чтобы ответы с каждого локального IPv4 уходили
через NIC, которому принадлежит этот IPv4, независимо от глобального default.

Перед планированием собираются snapshots всех разрешённых интерфейсов. Один и
тот же usable IPv4 на двух интерфейсах запрещён до первой мутации.

Gateway каждого интерфейса ищется в следующем порядке:

1. target gateway для выбранного интерфейса;
2. собственный default gateway интерфейса;
3. gateway уже существующей source-policy table;
4. общий fallback gateway, но только если он входит в prefix адреса этого NIC.

Для каждого usable IPv4 создаётся или переиспользуется rule:

```text
from IP/32 priority PRIORITY lookup TABLE
```

Существующая non-system rule для этого exact source переиспользуется. Иначе
table детерминированно выделяется из `10000..59999`, priority — из
`10000..29999`; начальная позиция вычисляется из 32 бит IPv4, конфликты
разрешаются линейным поиском. Таблицы 253/254/255 и priorities 0/32766/32767
зарезервированы.

В source table находятся:

```text
CONNECTED_PREFIX dev IFACE src IP scope link table TABLE
default via GATEWAY dev IFACE src IP table TABLE
```

Если gateway не входит ни в один prefix интерфейса, вместо connected prefix
используется `GATEWAY/32`. Новые rules/routes входят в план транзакции и в
`Target.ManagedRules`/`Target.ManagedRoutes`, поэтому участвуют в verify,
rollback и boot persistence.

## 12. Порядок транзакции

`Transaction.Apply` выполняет только netlink-часть и строго соблюдает порядок:

1. добавить отсутствующие addresses;
2. добавить host route до gateway;
3. заменить конкурирующие routes из `RouteChanges`;
4. заменить default route;
5. добавить/заменить source-policy routes;
6. добавить source rules.

Каждый успешный шаг учитывается счётчиком. Ошибка любого шага вызывает
немедленный `Transaction.Rollback`.

Обратный порядок rollback:

1. удалить добавленные rules;
2. удалить добавленные policy routes;
3. восстановить старый default route или удалить новый, если старого не было;
4. восстановить изменённые конкурирующие routes;
5. удалить добавленный gateway host route;
6. удалить только addresses, добавленные данной транзакцией.

Ошибки основной операции и rollback объединяются через `errors.Join`, то есть
нельзя терять первопричину при сообщении об ошибке отката.

## 13. Полный apply: backup, persistence, verify

`Application.applyPlan` окружает netlink-транзакцию следующими фазами:

```text
print plan
  -> dry-run stop
  -> root/confirmation/systemd checks
  -> capture managed files + write pending manifest
  -> netlink Apply
  -> write desired JSON + systemd unit
  -> enable new unit, disable previous ChangeIP units
  -> Verify
  -> optional advisory egress check
  -> mark manifest committed
```

При ошибке после изменения persistence сначала восстанавливаются управляемые
файлы и состояния units, затем выполняется сетевой rollback. Если запись
`committed` не удалась, операция тоже откатывается.

`Transaction.Verify` проверяет:

- наличие каждого добавляемого адреса;
- для add-only операции — неизменность default route и outbound source;
- наличие target source на интерфейсе;
- kernel route к `1.1.1.1` через ожидаемые interface/source;
- доступность route до gateway;
- наличие созданного host route;
- точное совпадение gateway/source/table/metric default route;
- глобальный interface/source при смене основного подключения;
- наличие всех managed source rules.

`--check-egress` дополнительно проверяет route к `8.8.8.8` и делает HTTPS
запрос к `ifconfig.me/ip` с bind на выбранный source. Ошибка внешнего сервиса,
NAT или несовпадение публичного IP — предупреждение, а не причина rollback.

## 14. Persistence после перезагрузки

Постоянный desired state записывается в:

```text
/usr/local/lib/change-ip/apply-IFACE.json   mode 0600
/etc/systemd/system/change-ip-IFACE.service mode 0644
```

JSON имеет `version: 1` и сериализованный `network.State`. Unit — oneshot с
`RemainAfterExit=yes`, запускаемый после `network-online.target`,
`networking.service` и `cloud-init.service`:

```text
change-ip apply-profile --config /usr/local/lib/change-ip/apply-IFACE.json
```

`apply-profile` принимает только очищенный путь вида
`/usr/local/lib/change-ip/apply-*.json`. До 30 секунд он ждёт появления target
NIC и всех интерфейсов, нужных managed routes; для сторонних NIC он также ждёт
наличия source address.

`ApplyDesiredState` затем:

1. добавляет отсутствующие сохранённые addresses;
2. пропускает dynamic addresses, кроме выбранного outbound source;
3. восстанавливает gateway host route;
4. восстанавливает managed routes;
5. восстанавливает managed rules;
6. заменяет default route.

Операция рассчитана на повторный запуск. Она не удаляет чужие или устаревшие
network-manager объекты и не объявляет владение всей конфигурацией интерфейса.

## 15. Backup и ручной rollback

Backup root выбирается как:

```text
HOME invoking SUDO_USER, если он известен
иначе os.UserHomeDir()
иначе /root

<home>/.local/state/change-ip/backups/change-ip-backup.*
```

Manifest имеет версию 2 и status `pending`, `committed` или `rolled_back`. Он
хранит before/target state, реально добавленные addresses, host route,
изменённые и добавленные routes/rules, snapshots двух управляемых файлов и
предыдущее состояние systemd units.

Чтение и восстановление файлов ограничены двумя каталогами и шаблонами:

- `/etc/systemd/system/change-ip-*.service`;
- `/usr/local/lib/change-ip/apply-*.json`.

Ручной rollback принимает только непосредственный дочерний каталог backup root
с префиксом `change-ip-backup.`. Сначала восстанавливаются файлы и units, затем
сеть откатывается тем же компенсирующим механизмом транзакции. Повторный
rollback может вернуть `ENOENT`-эквивалентные сетевые удаления как успех, но
manifest и фактическое состояние всё равно следует проверять через `doctor`.

## 16. Диагностика

`status` показывает runtime addresses, outbound source, gateway, table,
metric, onlink, путь persistence, состояние unit и desired state после reboot.

`doctor` дополнительно обнаруживает:

- отсутствующий apply JSON или systemd unit;
- не enabled или failed unit;
- отсутствие target IP;
- drift outbound source, gateway, table или metric;
- недоступный gateway route на выбранном интерфейсе;
- отсутствующий source rule для любого managed IP;
- один IP на нескольких интерфейсах;
- несколько одновременно активных persistence managers;
- backup manifest со статусом `pending`;
- последние 40 строк journal failed unit в plain CLI.

Важно: после осознанной операции `--runtime-only` отсутствие persistence будет
показано как проблема — это ожидаемая семантика `doctor`.

## 17. TUI

TUI не использует внешний framework. Он переводит terminal в raw mode,
скрывает cursor и перерисовывает один сохранённый ANSI-блок. Поддерживаются
стрелки или `j/k`, Enter, Escape, `q`, Ctrl-C. `NO_COLOR` отключает цвета.

Главное меню: смена outbound IP, добавление адресов, gateway, основное
подключение, status, doctor, rollback, язык, выход. Язык хранится в
`/etc/change-ip/language`; допустимы `en` и `ru`.

TUI использует те же `Application.Resolve*` и `Application.*` операции, а не
отдельную сетевую логику. До apply он строит plan для review, во время apply
автоматически ставит `Yes=true` и показывает progress callbacks.

## 18. Установка и обновление

`install.sh` устанавливает ELF атомарным rename в
`/usr/local/bin/change-ip`, создаёт совместимую ссылку
`/usr/local/sbin/change-ip -> ../bin/change-ip`, ставит man page и удаляет
устаревшие команды с underscore. Скрипт не меняет сеть. `install-en.sh` и
`install-ru.sh` только загружают основной installer и передают язык.

Встроенный `change-ip update`:

1. загружает `checksums.txt` из latest GitHub Release;
2. загружает бинарник нужной архитектуры и `change-ip.8` с лимитом размера;
3. сверяет SHA-256;
4. проверяет ELF и результат `--version`, major должен быть не ниже 3;
5. атомарно заменяет бинарник и синхронизирует каталог;
6. атомарно пишет man page;
7. удаляет deprecated `change_ip` и пересоздаёт same-name sbin symlink.

`update.sh` — recovery/bootstrap-путь, который дополнительно проверяет checksum
самого `install.sh` перед его запуском.

## 19. Известные ограничения и важные нюансы 3.2.0

- Основная смена IP и смена основного NIC поддерживают target-интерфейс без
  default route, но в системе всё ещё должен существовать активный глобальный
  default route: он нужен для выбора таблицы и безопасного fallback gateway.
- `add-address` намеренно не строит policy tables/rules. Они будут согласованы
  следующей основной операцией, сменой gateway или NIC.
- Verify точно проверяет managed rules, но не сравнивает каждый managed policy
  route как отдельный объект; работоспособность части routes косвенно
  проверяется через kernel route lookup.
- Boot apply добавляет/заменяет desired объекты, но не чистит старые
  source-policy routes/rules, которых уже нет в текущем JSON.
- `--runtime-only` не оставляет дискового backup для ручного rollback.
- Изменение сети не является одной атомарной операцией ядра. Безопасность
  достигается порядком шагов и компенсирующим rollback.
- Фильтр служебных интерфейсов применяется при автоматическом выборе и scan,
  но явный CLI-интерфейс после проверки имени может обойти этот фильтр.
- Manifest v2 читается строго: manifest другой версии отвергается, автоматической
  миграции старого backup format нет.

## 20. Инварианты безопасности при изменении кода

Будущий агент обязан сохранять следующие свойства:

1. Никогда не угадывать prefix нового IP.
2. Не размещать один IP одновременно на двух интерфейсах.
3. Не удалять адрес, существовавший до текущей транзакции.
4. Не менять IPv6, firewall, Docker и конфиги сторонних network managers.
5. Сначала строить полный plan и только затем выполнять мутации.
6. Любой новый тип мутации должен иметь симметричный rollback и быть отражён в
   manifest, persistence и verify.
7. Обратный порядок rollback должен учитывать зависимости route/rule/address.
8. `--dry-run` не должен вызывать ни одной мутации.
9. Add-only операция не должна менять default route или outbound source.
10. Интерфейс без default route должен оставаться доступным для настройки.
11. Off-subnet gateway и `/32` должны сохранять host-route/onlink поведение.
12. Смена основного NIC не должна ломать ответы со старых IP/NIC.
13. Нельзя ослаблять проверку управляемых путей, profile permissions или
    release checksums.
14. Нельзя переносить сетевую логику в TUI: UI должен вызывать слой app.

При расширении `network.Backend` необходимо обновить все fake backends в unit
tests. При изменении `network.State`, `transaction.Plan` или порядка apply нужно
одновременно проверить JSON persistence, manifest version/compatibility,
ручной rollback, boot path и netns integration tests.

## 21. Проверка изменений

Минимальная проверка без root:

```bash
env GOCACHE=/tmp/change-ip-go-cache go test ./...
bash -n install.sh install-en.sh install-ru.sh update.sh
sh tests/install_test.sh
```

`tests/install_test.sh` ожидает предварительно собранный
`dist/change-ip-linux-amd64`.

Реальные netlink-сценарии запускаются отдельно и требуют root, `iproute2` и
network namespaces:

```bash
sudo env GOCACHE=/tmp/change-ip-go-cache \
  go test -tags=integration ./integration -v
```

Интеграционная матрица проверяет существующий/новый адрес, `/32` с off-subnet
gateway, несколько default routes и non-main table, идемпотентность boot apply,
rollback, dry-run, смену NIC и сохранение return path старого интерфейса.

Для изменений критической сетевой логики unit tests недостаточно: обязательно
добавлять или обновлять netns-сценарий. Не тестировать опасные изменения на
рабочем SSH-интерфейсе разработчика.

## 22. Практическая карта изменений

- Новый CLI use case: parser/switch в `cmd/change-ip`, `Resolve*` и операция в
  `internal/app`, тест parser + app.
- Новое netlink-действие: метод `network.Backend`, Linux backend, fake
  backends, поля plan/transaction, apply/rollback/verify.
- Новое persisted поле: `network.State`, JSON boot path, diagnostics, backup и
  тест повторного `ApplyDesiredState`.
- Изменение выбора route/NIC: unit tests `internal/network`, multi-backend tests
  `internal/app`, затем integration netns.
- Изменение TUI: сначала поведение в app, затем только представление и
  локализация в `internal/ui`.
- Изменение release/install: updater unit tests, shell syntax и installer
  migration test.

До редактирования всегда проверять `git status`: рабочее дерево может содержать
незакоммиченные пользовательские изменения версии 3.2.0. Не перезаписывать и не
откатывать их.
