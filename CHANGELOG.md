# Changelog

## 3.2.3 — 2026-11-13

- Restore the complete persisted address pool after network-manager start and
  restart, covered by a 30-address regression fixture and a real systemd
  lifecycle test on an isolated CI runner.
- Refuse to replace persistence when saved static addresses are missing from
  runtime, preventing a temporary manager reset from truncating desired state.
- Run persistence migration through the newly installed binary after built-in
  updates; document the required `update.sh` or manual migration path for
  updaters from 3.2.1 and older.
