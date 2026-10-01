# simwifi

[日本語版はこちら / Japanese](README.ja.md)

A CLI tool that connects a Linux PC to WPA2/WPA3-Enterprise Wi-Fi with EAP-AKA / EAP-AKA', using the SIM card in a USB cellular modem running in MBIM mode.

wpa_supplicant does the actual EAP-AKA work. simwifi receives the USIM AUTHENTICATE requests (RAND / AUTN) that wpa_supplicant emits over D-Bus, has the SIM in the MBIM modem answer them, and passes the results back. It also takes care of the surrounding steps in a single command: modem discovery, building the identity (NAI), configuring wpa_supplicant, and reporting status.

> **Status: proof of concept.** Verified on Debian 13 with real hardware, a real SIM and a real EAP-AKA server: EAP-AKA / AKA' connection, re-authentication and re-synchronization (AUTS). See [docs/COMPAT.md](docs/COMPAT.md).

## How it works

```
AAA ─ AP ─ wlan0 ─ wpa_supplicant ─(D-Bus: NetworkRequest "SIM")─ simwifi ─ mbim-proxy ─ MBIM modem ─ USIM
```

- The primary path to the USIM is the MBIM Auth service `AKA` command. Qualcomm-based modems treat the AKA values as 128-bit little-endian integers (RAND / AUTN in, RES / CK / IK / AUTS out, all byte-reversed), so simwifi detects the byte order per modem automatically.
- If a modem does not support the `AKA` command, simwifi falls back to MS UICC Low-Level Access (APDUs on a logical channel).
- Design notes: [docs/DESIGN.md](docs/DESIGN.md) (in Japanese).

## Tested hardware

| Device | Module | MBIM AKA | UICC Low-Level Access | Result |
|---|---|---|---|---|
| Soracom Onyx | Quectel EG25-G | Yes (byte-reversed) | Yes | EAP-AKA / AKA', re-authentication, re-synchronization |
| M.2 USB adapter | Sierra Wireless EM7455 | Yes (byte-reversed) | No | EAP-AKA / AKA' |

Details and quirks: [docs/COMPAT.md](docs/COMPAT.md) (in Japanese).

## Requirements

- Debian 13 (trixie) or later
- ModemManager 1.18 or later, libmbim 1.32 or later (`mbim-proxy`)
- wpa_supplicant 2.10 or later, running with D-Bus enabled (`wpa_supplicant.service`) and built with EAP-AKA / AKA'
- A wireless interface with an nl80211 driver. If NetworkManager is running, mark the interface as unmanaged: `nmcli device set wlan0 managed no`
- Run as root (`mbim-proxy` only accepts root)
- The SIM PIN must be disabled or already unlocked (simwifi does not unlock it)

## Installation

Download the tarball for your architecture and `SHA256SUMS` from [GitHub Releases](https://github.com/oyaguma3/simwifi/releases).

```bash
sha256sum -c --ignore-missing SHA256SUMS
```

```bash
tar -xzf simwifi-<version>-linux-amd64.tar.gz
```

```bash
sudo install -m 0755 simwifi-<version>-linux-amd64/simwifi /usr/local/bin/simwifi
```

To run it as a service, use the files in `contrib/` from the tarball: `simwifi@.service` goes to `/etc/systemd/system/`, the environment file to `/etc/simwifi/<iface>.env` (see `simwifi.env.example`), and the logrotate config to `/etc/logrotate.d/simwifi`. Then:

```bash
sudo systemctl enable --now simwifi@wlan0
```

## Usage

```bash
sudo simwifi status                     # check prerequisites (read-only)
sudo simwifi probe                      # probe modem capabilities (uses one SIM authentication)
sudo simwifi identity --method akap     # print the permanent identity (NAI) built from the SIM
sudo simwifi connect --iface wlan0 --ssid corp-wifi --method aka --exec-up "dhclient -1 -nw wlan0"
```

`connect` stays in the foreground. Press Ctrl-C (or send SIGTERM) to disconnect and clean up.

Main `connect` options:

| Option | Default | Description |
|---|---|---|
| `--ssid` | (required) | SSID to connect to |
| `--method aka\|akap` | `aka` | EAP-AKA or EAP-AKA' |
| `--iface` | `wlan0` | Wireless interface |
| `--realm` | from the SIM | Override the whole NAI realm |
| `--auth-path auto\|aka\|uicc` | `auto` | USIM access path (`auto` picks a working one) |
| `--wpa3` | off | Use `WPA-EAP-SHA256` with PMF required |
| `--timeout` | 60 | Seconds to wait for the connection |
| `--max-auth-failures` | 3 | Exit after this many consecutive authentication failures |
| `--exec-up` / `--exec-down` | none | Shell commands to run when connected / disconnected |
| `--sim-slot N` / `--switch-slot` | none | Require SIM slot N to be active / switch to it if needed |
| `--log-file`, `-v`, `-vv` | stderr, Info | JSON log file, debug / trace logging |

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Success (including Ctrl-C / SIGTERM during `connect`) |
| 1 | Usage error, internal error, or wpa_supplicant disappeared |
| 2 | Prerequisites not met (see `simwifi status`) |
| 3 | Authentication failed |
| 4 | Timed out |

Logs never contain RES / CK / IK / AUTS. The IMSI is masked unless `--log-imsi` is given.

## Troubleshooting

| Symptom | What to check |
|---|---|
| `mbim-proxy closed the connection immediately` | Run as root. |
| `wireless interface is already managed by another wpa_supplicant client` | Make sure NetworkManager does not manage the interface. |
| `SIM is locked` | Unlock the SIM with ModemManager (`mmcli -i <sim> --pin=...`) or disable the PIN. |
| `cannot determine MCC/MNC` | Give the realm explicitly with `--realm`. |
| Only EAP-AKA' fails | The network must set the AMF separation bit (0x8000) in AUTN; wpa_supplicant checks it. |
| A Lenovo-branded EM7455 (USB ID `1199:9079`) cannot turn its radio on | It is FCC-locked. simwifi does not need the radio for EAP-AKA, so this does not prevent authentication. |

## Building from source

Go 1.27 or later. The only external dependency is `github.com/godbus/dbus/v5`; the result is a static binary.

```bash
make build        # bin/simwifi
make dist         # dist/simwifi-<version>-linux-{amd64,arm64}.tar.gz and SHA256SUMS
make test         # unit and integration tests (D-Bus fakes need dbus-daemon)
make lint         # go vet and golangci-lint
```

Releases are built by GitHub Actions when a tag like `v1.2.3` is pushed (tags with a hyphen become pre-releases).

## Testing

- Unit and integration tests: `make test`. They use fake implementations of mbim-proxy, ModemManager and wpa_supplicant.
- End-to-end tests on real Linux without a SIM (mac80211_hwsim + hostapd): [test/e2e/README.md](test/e2e/README.md) (in Japanese).

## License

MIT
