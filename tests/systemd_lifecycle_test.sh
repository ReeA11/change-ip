#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d /tmp/change-ip-systemd.XXXXXX)
unit=change-ip-ci-u39.service
manager=change-ip-test-manager.service

cleanup() {
	status=$?
	if [ "$status" -ne 0 ]; then
		systemctl status "$manager" "$unit" --no-pager || true
		journalctl -u "$manager" -u "$unit" --no-pager -n 80 || true
		ls -la "$tmp" || true
	fi
  systemctl stop "$manager" "$unit" >/dev/null 2>&1 || true
  rm -f "/run/systemd/system/$unit" "/run/systemd/system/$manager" "/run/systemd/system/$manager.wants/$unit"
  rmdir "/run/systemd/system/$manager.wants" 2>/dev/null || true
  systemctl daemon-reload >/dev/null 2>&1 || true
  rm -rf -- "$tmp"
	exit "$status"
}
trap cleanup EXIT HUP INT TERM

desired="$tmp/desired"
runtime="$tmp/runtime"
ready="$tmp/ready"
calls="$tmp/calls"
metadata="$tmp/metadata"

for last in $(seq 129 158); do printf '178.83.112.%s/32\n' "$last"; done > "$desired"
printf '%s\n' 'egress=45.94.222.26' 'gateway=45.94.222.1' 'rule=from-178.83.112.130/32-table-10130' > "$metadata"
metadata_before=$(sha256sum "$metadata" | awk '{print $1}')

cat > "$tmp/change-ip-fixture" <<EOF
#!/bin/sh
test "\$1" = apply-profile
cp "$desired" "$runtime"
printf 'apply\n' >> "$calls"
EOF
chmod 0755 "$tmp/change-ip-fixture"

cat > "$tmp/wait-ready" <<EOF
#!/bin/sh
i=0
while [ ! -e "$ready" ]; do
  i=\$((i + 1))
  test "\$i" -lt 100
  sleep 0.05
done
EOF
chmod 0755 "$tmp/wait-ready"

CHANGE_IP_SYSTEMD_FIXTURE="$tmp" CHANGE_IP_SYSTEMD_FIXTURE_BINARY="$tmp/change-ip-fixture" \
  GOCACHE="$tmp/go-cache" go test ./internal/persist -run TestWriteLifecycleFixture -count=1

sed \
  -e "s/systemd-networkd.service/$manager/g" \
  -e "s#ExecStartPre=.*#ExecStartPre=$tmp/wait-ready#" \
  "$tmp/units/$unit" > "/run/systemd/system/$unit"

cat > "/run/systemd/system/$manager" <<EOF
[Unit]
Description=ChangeIP isolated lifecycle manager

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c "printf '178.83.112.129/32\\n' > '$runtime'"
EOF

mkdir -p "/run/systemd/system/$manager.wants"
ln -s "../$unit" "/run/systemd/system/$manager.wants/$unit"
systemctl daemon-reload

restore_after_manager_start() {
  rm -f "$ready"
  (sleep 1; touch "$ready") &
  "$@"
  cmp "$desired" "$runtime"
}

restore_after_manager_start systemctl start "$manager"
restore_after_manager_start systemctl restart "$manager"
systemctl stop "$manager"
test "$(systemctl is-active "$unit" || true)" = inactive
restore_after_manager_start systemctl start "$manager"

before=$(wc -l < "$calls")
systemctl restart "$unit"
after=$(wc -l < "$calls")
test "$after" -eq $((before + 1))
cmp "$desired" "$runtime"
test "$(sha256sum "$metadata" | awk '{print $1}')" = "$metadata_before"
test "$(wc -l < "$runtime")" -eq 30

echo "generated systemd lifecycle test: OK"
