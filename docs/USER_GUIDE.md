# simwifi User Guide

[日本語版](USER_GUIDE.ja.md)

This is the detailed version of the [Usage](../README.md#usage) section of the README. It covers every command and option, how `connect` behaves, and command examples for common use cases. For installation and requirements, see the [README](../README.md).

## Contents

- [1. Getting to the first connection](#1-getting-to-the-first-connection)
- [2. Common options](#2-common-options)
- [3. status — check prerequisites](#3-status--check-prerequisites)
- [4. probe — check modem capabilities](#4-probe--check-modem-capabilities)
- [5. identity — print the identity (NAI)](#5-identity--print-the-identity-nai)
- [6. connect — connect to a network](#6-connect--connect-to-a-network)
- [7. Examples](#7-examples)
- [8. Running as a systemd service](#8-running-as-a-systemd-service)
- [9. Exit codes](#9-exit-codes)
- [10. Logging](#10-logging)
- [11. Troubleshooting](#11-troubleshooting)

## 1. Getting to the first connection

Every command except `version` must run as root (`mbim-proxy` accepts only root).

1. **Check the prerequisites.** Pass your actual wireless interface name with `--iface` (the default is `wlan0`).
   ```bash
   sudo simwifi status --iface wlan0
   ```
   If any line is marked `✘`, fix it as its message says.

2. **Check what the modem can do.** Run this once when you first use a modem. It uses one SIM authentication.
   ```bash
   sudo simwifi probe
   ```
   If either `MBIM AKA` or `UICC LLA` is `supported`, the modem works with simwifi.

3. **Check the identity.** Make sure it matches the subscriber registered on the authentication server.
   ```bash
   sudo simwifi identity --method aka
   ```

4. **Connect.**
   ```bash
   sudo simwifi connect --iface wlan0 --ssid corp-wifi --method aka
   ```
   The `connected` log line means you are connected. `connect` stays in the foreground; press Ctrl-C to disconnect and exit. simwifi does not obtain an IP address (see [7.2](#72-get-an-ip-address-with-dhcp)).

## 2. Common options

Available for `status`, `probe`, `identity` and `connect`.

| Option | Default | Description |
|---|---|---|
| `--modem N` | none | Modem to use: the ModemManager modem index (the trailing number in `mmcli -L`) or its D-Bus object path (`/org/freedesktop/ModemManager1/Modem/0`). Optional with one modem, required with several |
| `--iface IFACE` | `wlan0` | Wireless interface |
| `--json` | off | Print the result as JSON on stdout (`status` and `probe` only) |
| `--log-file PATH` | none | Also append JSON logs to PATH. The file is created with mode 0600 |
| `-v` | off | Debug logging: RAND / AUTN, wpa_supplicant state changes, EAP events and so on |
| `-vv` | off | Trace logging: also the contents of MBIM and D-Bus messages |
| `--log-imsi` | off | Do not mask the IMSI in logs and output |

> ModemManager may renumber modems (after the modem is replugged or switches SIM slots). If you pin a modem in a multi-modem setup, check `mmcli -L` first.

## 3. status — check prerequisites

```
simwifi status [--iface wlan0] [--modem N] [--method aka|akap] [--realm REALM] [--json]
```

Checks, one by one, what simwifi needs in order to work. It only reads; it does not authenticate with the SIM.

| Option | Default | Description |
|---|---|---|
| `--method aka\|akap` | `aka` | The EAP method to check wpa_supplicant for, and the kind of identity to show |
| `--realm REALM` | from the SIM | Check the identity with this realm instead (see [5](#5-identity--print-the-identity-nai)) |

`✔` means satisfied, `✘` means not satisfied (fatal), and `·` is for information only.

| Check | What it checks |
|---|---|
| `root` | Running as root |
| `ModemManager` | ModemManager is running |
| `modem` | Exactly one modem is selected (`--modem` is needed if there are several), and it is not in the `failed` state |
| `MBIM port` | The modem has an MBIM port (`/dev/cdc-wdm*`). If not, the modem is not in MBIM mode |
| `SIM slots` | The SIM in each slot and which slot is active (information) |
| `SIM lock` | The SIM is not PIN-locked (PIN2 is not a problem) |
| `SIM` | The active slot has a SIM, and its IMSI and MCC/MNC can be read |
| `identity` | The identity (NAI) can be generated |
| `mbim-proxy` | simwifi can talk to the modem through `mbim-proxy` |
| `probe` | The saved `probe` result (information; fatal if neither path is supported) |
| `wlan` | The interface exists and is an nl80211 wireless interface |
| `NetworkManager` | NetworkManager does not manage the interface |
| `wpa_supplicant` | wpa_supplicant is available on D-Bus |
| `EAP methods` | wpa_supplicant supports the EAP method given by `--method` |
| `Interworking/HS20` | Interworking / Hotspot 2.0 support in wpa_supplicant (information) |
| `wpa interface` | No other client is using the interface through wpa_supplicant |
| `lock` | No other simwifi is running on the same interface |

The exit code is 2 if any check is fatal, and 0 otherwise. With `--json`, the output looks like this (the exit code is the same):

```json
{
  "ok": true,
  "checks": [
    { "name": "root", "ok": true, "detail": "running as root" },
    { "name": "SIM slots", "ok": true, "info": true, "detail": "1: 44010…3421 (active)" }
  ]
}
```

A check that is not satisfied has `"fatal": true`.

## 4. probe — check modem capabilities

```
simwifi probe [--modem N] [--json]
```

Checks whether the modem supports the two paths simwifi can use to reach the USIM:

- **MBIM AKA**: the `AKA` command of the MBIM Auth service. This is the main path
- **UICC LLA**: MS UICC Low-Level Access. Opens a logical channel and sends APDUs to the USIM directly

For MBIM AKA, probe sends one deliberately invalid AUTN and looks at the response. The USIM just rejects it, so the SIM's state (such as SQN) does not change, but it does use one SIM authentication.

```
modem:     PIX-MT200e (PIX-MT200e_MB_V1.01)
device:    /dev/cdc-wdm0
MBIM AKA:  supported (auth-incorrect-autn)
UICC LLA:  unsupported (ms-select-failed)
  ATR:              3b9f…
  APPLICATION_LIST: false
  USIM AID:         a0000000871002
```

- If either path is `supported`, the modem can be used. If both are `unsupported`, the output ends with "This modem cannot be used by simwifi"
- The value in parentheses is the modem's response (the MBIM status) that the verdict is based on
- The result is saved in `/run/simwifi/probe-<IMEI>.json`. `status` shows it, and `connect` starts with the UICC path if MBIM AKA was recorded as unsupported. `/run` is cleared on reboot, so run probe again after a reboot if you need it
- probe only looks at the response to dummy values. Whether the modem answers a real challenge correctly can only be seen by actually connecting

## 5. identity — print the identity (NAI)

```
simwifi identity [--modem N] [--method aka|akap] [--realm REALM]
```

Builds the EAP permanent identity (the NAI in 3GPP TS 23.003) from the SIM's IMSI and prints it on stdout. This is the same value `connect` sends in the EAP-Response/Identity. The IMSI is not masked in this output.

```
<prefix><IMSI>@wlan.mnc<3-digit MNC>.mcc<MCC>.3gppnetwork.org
```

| Option | Default | Description |
|---|---|---|
| `--method aka\|akap` | `aka` | Selects the prefix: `0` for EAP-AKA, `6` for EAP-AKA' |
| `--realm REALM` | from the SIM | Replace everything after `@` with REALM (the prefix and IMSI are kept) |

- The MCC and MNC come from the operator code (MCC + MNC) that ModemManager reads from the SIM. A 2-digit MNC is zero-padded to 3 digits (`10` → `010`)
- If the operator code cannot be read (for example, the SIM is still loading) or does not match the IMSI, simwifi exits and asks for `--realm`. The IMSI alone cannot tell whether the MNC has 2 or 3 digits
- The permanent identity is sent in the clear. Pseudonyms and fast re-authentication identities are handled by wpa_supplicant; simwifi does not store them (it always starts from the permanent identity)

## 6. connect — connect to a network

```
simwifi connect --ssid SSID [--iface wlan0] [options]
```

### 6.1 Options

| Option | Default | Description |
|---|---|---|
| `--ssid SSID` | (required) | SSID to connect to |
| `--method aka\|akap` | `aka` | EAP-AKA or EAP-AKA' |
| `--realm REALM` | from the SIM | Override the identity's realm ([5](#5-identity--print-the-identity-nai)) |
| `--auth-path auto\|aka\|uicc` | `auto` | USIM access path ([6.3](#63---auth-path)) |
| `--wpa3` | off | Use `WPA-EAP-SHA256` key management with PMF required ([6.4](#64---wpa3)) |
| `--timeout SECONDS` | 60 | Seconds to wait for the first connection. Exits with code 4 when it passes |
| `--max-auth-failures N` | 3 | Exit with code 3 after N consecutive authentication failures |
| `--exec-up CMD` | none | Shell command to run when connected ([6.5](#65-hooks---exec-up----exec-down)) |
| `--exec-down CMD` | none | Shell command to run when disconnected |
| `--sim-slot N` | current slot | Require SIM slot N to be active ([6.6](#66-sim-slots)) |
| `--switch-slot` | off | Switch to slot N if it is not active. Use with `--sim-slot` |
| `--daemon` | — | Not implemented; gives an error. Use systemd to run in the background ([8](#8-running-as-a-systemd-service)) |

The common options ([2](#2-common-options)) are also available. `--json` has no effect on `connect`.

### 6.2 How connect works

1. Takes a per-interface lock (`/run/simwifi/<iface>.lock`). A second simwifi cannot start on the same interface
2. Runs the same checks as `status`. If any check fails, logs `prerequisite check failed` for each (with `check` and `detail`) and exits with code 2
3. If `--sim-slot` is given, checks the slot (and switches if requested)
4. Has wpa_supplicant create the interface (with the configuration file `/run/simwifi/wpa-<iface>.conf`), adds the network and starts connecting. An interface left behind by a simwifi that crashed is reclaimed. An interface used by another client is left alone, and simwifi exits with code 2
5. Each time wpa_supplicant requests a USIM authentication, has the modem's SIM answer it
6. When the connection completes, logs `connected` and runs `--exec-up`

simwifi keeps running after it connects.

- If the connection drops (for example, the signal is lost), it logs `connection lost` and runs `--exec-down`. wpa_supplicant reconnects by itself, and `--exec-up` runs again when it does
- Re-authentication and rekeying while connected are not treated as disconnections (the hooks do not run)
- `--timeout` applies only until the first connection. Once connected, simwifi never exits on a timeout, however long it stays disconnected
- Authentication failures are counted once per EAP exchange. The count is reset when a connection succeeds

simwifi exits in these cases:

| Trigger | Exit code |
|---|---|
| Ctrl-C (SIGINT) or SIGTERM | 0 |
| The first connection does not complete within `--timeout` seconds | 4 |
| Authentication failed `--max-auth-failures` times in a row | 3 |
| The modem was removed | 2 |
| wpa_supplicant exited | 1 |

On exit, simwifi runs `--exec-down` if connected, then removes the network it added and the wpa_supplicant interface it created. It does not stop wpa_supplicant itself.

### 6.3 --auth-path

| Value | Behavior |
|---|---|
| `auto` | Use MBIM AKA, and switch to the UICC path if MBIM AKA does not work. Recommended |
| `aka` | Use MBIM AKA only |
| `uicc` | Use the UICC path only |

How `auto` works in detail:

- If MBIM AKA reports that it is not supported, the same challenge is sent again over the UICC path
- If MBIM AKA rejects a challenge, the same challenge is checked over the UICC path. If the UICC path accepts it, MBIM AKA is considered unreliable and the UICC path is used from then on (some modems reject even valid challenges over MBIM AKA)
- Once switched, the UICC path is used until the process exits
- If the `probe` result says MBIM AKA is not supported, the UICC path is used from the start

Qualcomm-based modems handle MBIM AKA values in reversed byte order. simwifi detects which order works on the first authentication and uses it from then on (the `MBIM AKA byte order detected` log line). No configuration is needed either way.

Use `aka` or `uicc` to isolate problems with one path.

### 6.4 --wpa3

Uses `WPA-EAP-SHA256` key management and requires PMF (802.11w). Use it for WPA3-Enterprise access points (not the 192-bit mode).

- The connection fails if the access point does not offer `WPA-EAP-SHA256`
- If wpa_supplicant or the driver does not report `WPA-EAP-SHA256` support, simwifi logs a warning and tries anyway
- The 192-bit mode (`WPA-EAP-SUITE-B-192`) is not supported

### 6.5 Hooks (--exec-up / --exec-down)

`--exec-up` runs when connected and `--exec-down` when disconnected (including the disconnection on exit), with `/bin/sh -c`.

- These environment variables are passed:

  | Variable | Value |
  |---|---|
  | `SIMWIFI_EVENT` | `up` or `down` |
  | `SIMWIFI_IFACE` | Interface name |
  | `SIMWIFI_SSID` | SSID |

- `$PPID` is the process ID of simwifi
- Hooks run one at a time, in order. They do not block the handling of SIM requests
- Each run is limited to 30 seconds and is stopped after that. For long-running programs, use one that backgrounds itself, or start it with `&`
- The hook's stdout and stderr go to simwifi's stderr
- If a hook fails, simwifi logs it and keeps running
- On exit, simwifi waits for running and pending hooks to finish

### 6.6 SIM slots

MBIM authentication has no way to select a slot; the SIM in the active slot is always used.

- `--sim-slot N` requires slot N to be active. If it is not, simwifi exits with code 2
- With `--switch-slot`, simwifi asks ModemManager to switch to slot N. This is equivalent to restarting the modem, and drops any cellular data connection. simwifi waits up to 60 seconds for the modem to come back
- If ModemManager reports slot N as empty, simwifi does not switch even with `--switch-slot`. Switching to an empty slot has left a modem unusable until it was power-cycled
- On a modem with a single slot, only `--sim-slot 1` is accepted
- You cannot use one slot for cellular data and another for Wi-Fi authentication at the same time

## 7. Examples

The examples use the interface `wlan0` and the SSID `corp-wifi`.

### 7.1 Just check that you can connect

Send yourself SIGTERM as soon as you connect. The exit code is 0 on success, 3 on authentication failure and 4 on timeout.

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --timeout 30 --exec-up 'kill -TERM $PPID'
```

`$PPID` is the parent of sh, which is simwifi. Use single quotes so that your shell does not expand it.

### 7.2 Get an IP address with DHCP

simwifi does not configure IP addresses, so run a DHCP client from the hooks. The standard DHCP client on Debian 13 is `dhcpcd` (the `dhcpcd-base` package).

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi \
  --exec-up 'dhcpcd -b wlan0' \
  --exec-down 'dhcpcd -k wlan0'
```

- `dhcpcd -b` goes to the background immediately, so it stays within the 30-second hook limit
- `dhcpcd -k` releases the address and stops dhcpcd
- If you have `isc-dhcp-client` installed, you can use `--exec-up 'dhclient -1 -nw wlan0' --exec-down 'dhclient -r wlan0'`

### 7.3 Use a static IP address

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi \
  --exec-up 'ip addr add 192.0.2.10/24 dev wlan0 && ip route add default via 192.0.2.1 dev wlan0 metric 600' \
  --exec-down 'ip addr flush dev wlan0'
```

Use a larger `metric` if you want other routes (such as wired) to take priority.

### 7.4 Put the hooks in a script

Branch on `SIMWIFI_EVENT` to use one script for both hooks.

```sh
#!/bin/sh
# /usr/local/sbin/simwifi-hook
case "$SIMWIFI_EVENT" in
up)
	logger -t simwifi "connected to $SIMWIFI_SSID on $SIMWIFI_IFACE"
	dhcpcd -b "$SIMWIFI_IFACE"
	;;
down)
	logger -t simwifi "disconnected from $SIMWIFI_SSID on $SIMWIFI_IFACE"
	dhcpcd -k "$SIMWIFI_IFACE"
	;;
esac
```

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi \
  --exec-up /usr/local/sbin/simwifi-hook --exec-down /usr/local/sbin/simwifi-hook
```

### 7.5 Connect with EAP-AKA' or WPA3-Enterprise

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --method akap
```

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --method akap --wpa3
```

With EAP-AKA', wpa_supplicant rejects the authentication unless the AMF in the server's AUTN has the separation bit (0x8000) set ([11](#11-troubleshooting)).

### 7.6 Specify the realm

Use this when the authentication server expects a realm other than the 3GPP format, or when the MCC/MNC cannot be read from the SIM.

```bash
sudo simwifi identity --method aka --realm wlan.example.net
```

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --realm wlan.example.net
```

Pass the same `--realm` to `status` to check that an identity can be generated with it.

### 7.7 With several modems

If there are several modems and you omit `--modem`, `status` lists their indices and models, like `multiple modems found; specify --modem (0: EG25-G, 1: PIX-MT200e)`. You can also check with `mmcli -L`.

```bash
mmcli -L
```

```bash
sudo simwifi status --iface wlan0 --modem 1
```

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --modem 1
```

### 7.8 Use the second SIM slot

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi --sim-slot 2 --switch-slot
```

Switching drops the cellular data connection. Some modems become unstable when switching slots; check the per-model notes in [docs/COMPAT.md](COMPAT.md) (in Japanese).

### 7.9 Check prerequisites from a script

`status` exits with code 2 if any check is fatal.

```bash
sudo simwifi status --iface wlan0 --json | jq -r '.checks[] | select(.fatal) | "\(.name): \(.detail)"'
```

```bash
if sudo simwifi status --iface wlan0 >/dev/null; then echo ready; fi
```

### 7.10 When something goes wrong

Connect with debug logging, also saved to a file:

```bash
sudo simwifi connect --iface wlan0 --ssid corp-wifi -v --log-file /tmp/simwifi.log
```

The details of the EAP exchange are in wpa_supplicant's log. For example, the EAP-AKA' AMF problem shows up only there.

```bash
sudo journalctl -u wpa_supplicant -f
```

Use `-vv` to see the messages exchanged with the modem as well. If you share logs with others, capture them without `--log-imsi` (so that the IMSI is masked).

## 8. Running as a systemd service

Use the files in `contrib/` included in the tar.gz.

| File | Location |
|---|---|
| `simwifi@.service` | `/etc/systemd/system/simwifi@.service` |
| `simwifi.env.example` | Edit and save as `/etc/simwifi/<iface>.env` |
| `simwifi.logrotate` | `/etc/logrotate.d/simwifi` |

Example `/etc/simwifi/wlan0.env`:

```sh
SSID=corp-wifi
METHOD=akap
EXEC_UP=dhcpcd -b wlan0
EXEC_DOWN=dhcpcd -k wlan0
SIMWIFI_OPTS=--wpa3 --timeout 120
```

- `SSID`, `EXEC_UP` and `EXEC_DOWN` are each passed as one word, so they may contain spaces (do not add quotes)
- `SIMWIFI_OPTS` is split on spaces, so options whose values contain spaces cannot go there

```bash
sudo systemctl enable --now simwifi@wlan0
```

```bash
journalctl -u simwifi@wlan0 -f
```

- The instance name (after `@`) is the interface name. With one env file per interface, you can run several at once (use a separate modem for each and select it with `--modem`)
- Logs go to journald and also to `/var/log/simwifi/<iface>.log` (JSON)
- With `Restart=on-failure`, the service restarts 10 seconds after exiting because of an authentication failure, a removed modem and so on. It does not restart after `systemctl stop` (exit code 0)
- With `ProtectSystem=strict`, the file system is read-only except for `/run/simwifi` and `/var/log/simwifi`. If your hooks write files (DHCP leases, rewriting `/etc/resolv.conf` and so on), add `ReadWritePaths=` to `[Service]` with `sudo systemctl edit simwifi@wlan0`. Which paths are needed depends on your DHCP client and DNS setup
- Processes started by hooks (such as dhcpcd) are stopped together with the service

## 9. Exit codes

| Code | Meaning |
|---|---|
| 0 | Success (including exit by Ctrl-C / SIGTERM during `connect`) |
| 1 | Usage error, internal error, or wpa_supplicant disappeared |
| 2 | Prerequisites not met, another simwifi is running on the same interface, or the modem was removed |
| 3 | Authentication failed |
| 4 | Timeout |

## 10. Logging

- Logs go to stderr. stdout is used only for the results of `status`, `probe` and `identity`
- With `--log-file`, the same logs are also appended to the file as JSON. Rotate it with `contrib/simwifi.logrotate` (`copytruncate`)
- Each line has a `component` (`cli`, `modem`, `mbim`, `simauth` or `supplicant`)
- Messages are in English

| Value | Handling |
|---|---|
| RES / CK / IK / AUTS | Never logged (even with `-vv`, only their lengths) |
| RAND / AUTN | Logged with `-v` and above (they are not secret) |
| IMSI | Masked (`44010…3421`). `--log-imsi` shows all digits |
| ICCID | Only the first 4 and last 4 digits |
| NAI | The IMSI part is masked |

## 11. Troubleshooting

In addition to [Troubleshooting](../README.md#troubleshooting) in the README:

| Symptom | What to check |
|---|---|
| `multiple modems found; specify --modem (…)` | Select the modem with `--modem` ([7.7](#77-with-several-modems)) |
| `modem has no MBIM port (is the modem in MBIM mode?)` | Switch the modem to MBIM mode (how depends on the model) |
| `another simwifi instance is already running on this interface` | Whether simwifi is already running on the same interface (including as a systemd service) |
| `no SIM in the active slot` | Whether a SIM is inserted. On a dual-SIM modem, which slot is active (`SIM slots` in `status`) |
| `IMSI not available (SIM not ready?)` | Right after the modem is plugged in, the SIM takes a while to load. Wait a little and try again |
| `not connected within 1m0s` | Whether the SSID is correct and in range, and whether responses are delayed on the way to the authentication server. Increase `--timeout` if needed |
| `authentication failed 3 times in a row` | Whether the identity matches the registered subscriber (`simwifi identity`). For EAP-AKA', the AMF separation bit. wpa_supplicant's log ([7.10](#710-when-something-goes-wrong)) |
| `USIM rejected AUTN (network authentication failed)` | The SIM did not accept the network as genuine. Whether the server's keys (K / OPc) match the SIM |
| `USIM synchronization failure; sending AUTS` | The SQN of the SIM and the server are out of sync. simwifi returns AUTS, so if the server supports resynchronization, the connection continues |
| `SIM slot N is not active (active: M); add --switch-slot to switch` | Add `--switch-slot`, or remove `--sim-slot` |
| `SIM slot N is empty according to ModemManager; not switching` | Whether a SIM is inserted in slot N |
| `modem was removed` | The modem was unplugged or restarted. Under systemd, the service restarts automatically |
