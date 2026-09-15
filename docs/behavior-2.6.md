# ChangeIP 2.6 behavioural inventory

Extracted from the former Bash 2.6 reference before implementing 3.0. The Bash source was removed after the Go implementation and migration tests were verified.

- Commands: wizard/no args, `apply`, direct `NEW_IP[/PREFIX]`, `status`, `doctor`, `rollback`, legacy `--rollback DIR`, help.
- Flags: runtime-only, dry-run, yes, check-egress, profile, prefix, gateway, interface.
- Discovery: default-route interface unless explicit; auto-selection excludes loopback, container, tunnel, WireGuard, TAP/TUN, and PPP. Ethernet, bond, bridge, and VLAN remain eligible.
- Addresses: usable unicast IPv4 only; never guess a new prefix; reuse an existing address's prefix; preserve all addresses; reject conflicting prefix.
- Routes: preserve gateway unless overridden, metric, table, and onlink requirement; create gateway `/32` link route if direct reachability is absent; set explicit source.
- Transaction: snapshot, plan, confirm, runtime, persistence, verify; failure restores old route and deletes only objects created by the transaction.
- Verification: target address, route/interface/source to 1.1.1.1, default source; optional 8.8.8.8/gateway/external-IP checks are advisory.
- Persistence: active systemd unless runtime-only; backup generated files; oneshot after network-online/networking/cloud-init restores address, gateway route, and source route; never reboot.
- Backup/rollback: old and target route/address facts, prior generated files, unit state. Never delete an address present before the transaction.
- Safety: global lock, confirmation, systemd-run for SSH, signal rollback, strict root profile permissions.
- Diagnostics: runtime source/gateway/address, desired persistence, unit state/journal, legacy Netplan artifacts.

Intentional 3.0 changes: netlink replaces parsing `ip`; JSON replaces the shell manifest; the boot unit invokes Go directly rather than a generated shell script; selection among multiple defaults is deterministic; critical verification includes gateway, table, and metric.
