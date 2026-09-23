<p align="center">
  <a href="https://github.com/voska/bambu/actions/workflows/ci.yml"><img src="https://github.com/voska/bambu/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/voska/bambu/releases"><img src="https://img.shields.io/github/v/release/voska/bambu" alt="Release" /></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/github/go-mod/go-version/voska/bambu" alt="Go" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License: MIT" /></a>
</p>

# bambu

Slice, check, send and monitor **Bambu Lab** prints from the terminal over your LAN. It's built for humans and for AI agents.

- **Headless slicing:** it drives the Bambu Studio CLI with the correct system presets for your printer and nozzle, plus recipes and `--set` overrides.
- **Safety first:**
  - Preflight gates check that the printer is idle, the right filament is in the slot, the plate and nozzle match, Developer Mode is on, and there's enough filament.
  - `print send` refuses to run without `--confirm`.
  - No command ever changes printer settings.
- **LAN only:** MQTT and FTPS go directly to the printer. No cloud account, no Bambu Connect.
- **Agent-friendly:** `--json` everywhere (NDJSON for streams), stable exit codes, `schema` introspection, and no prompts with `--no-input`.
- **Single static binary** for macOS, Linux and Windows.

```console
$ bambu status
workshop (192.168.1.50)  IDLE / idle   dev_mode=true  sdcard=true  liveview=true
  temps    nozzle 26/0  bed 24/0   nozzle 0.4 mm hardened_steel
  errors   none
  AMS A1   PLA    PLA Matte    #FF6A13  33% (~330 g)
  AMS A2   PETG   PETG Basic   #FFFFFF  80% (~800 g)
  protect  first_layer=true spaghetti=true halt=true (medium)

$ bambu slice bracket.stl --recipe functional-petg
sliced  bracket-functional-petg
  time      35m 38s  (2138 s)
  filament  13.22 g PETG (GFG00)   layers 113
  temps     bed 70 C (textured_plate)   nozzle 252 C, first layer 252 C
  3mf       ./bracket-functional-petg.gcode.3mf

$ bambu print send bracket-functional-petg.gcode.3mf --slot A2 --dry-run   # preflight + exact payload, sends nothing
$ bambu print send bracket-functional-petg.gcode.3mf --slot A2 --confirm   # after you've checked the plate is clear
$ bambu monitor --snapshot-at-layer 2
```

## Requirements

- **A Bambu Lab printer in LAN Only mode with Developer Mode enabled.** Since the January 2025 "Authorization Control" firmware, the printer rejects unsigned third-party control commands otherwise.
  - On the printer, go to **Settings → LAN Only**, enable **LAN Only Mode**, then **Developer Mode**. Note the **access code** shown there.
  - Developer Mode disconnects the printer from Bambu Cloud and the Handy app. That's Bambu's tradeoff, not ours.
  - Status (`bambu status`) is read-only and works without Developer Mode. FTPS upload and control need it.
- **[Bambu Studio](https://bambulab.com/en/download/studio)** installed, for `slice`.
- **`ffmpeg`**, for camera snapshots.
- **Tested models:** X1 Carbon. The X1/X1E/P1/A1/H2/P2S series use the same LAN protocol and should work, but they are untested. Camera snapshots are X1/H2/P2S-series only for now; P1/A1 use a different camera protocol.

## Install

```bash
brew install voska/tap/bambu            # macOS / Linux
go install github.com/voska/bambu/cmd/bambu@latest
```

Binaries for macOS, Linux and Windows are on [Releases](https://github.com/voska/bambu/releases).

## Setup

```bash
# 1. Add the printer (IP + serial from the printer's Settings, or use discovery)
bambu printer discover                       # listens for the printer's SSDP broadcast (UDP 2021)
bambu printer add workshop --host 192.168.1.50 --serial 00M00A000000000 --model X1C --nozzle 0.4 --plate textured_plate

# 2. Store the access code in the OS keychain (read from stdin; verified against the printer, read-only)
bambu auth set workshop                      # prompts; or: echo CODE | bambu auth set workshop

# 3. Check
bambu auth status --check
bambu slicer info
bambu status
```

**Config:** `~/.config/bambu/config.toml` (`%AppData%\bambu\config.toml` on Windows, or `$BAMBU_CONFIG`). Printers are named, and `--printer`/`BAMBU_PRINTER` picks one. `bambu` only reads and writes `config.toml`, so it coexists with other tools that use `~/.config/bambu/config.json`.

**Access code:** looked up in this order:
1. The OS keychain (service `bambu`, account = the printer serial)
2. `BAMBU_ACCESS_CODE`
3. Bambu Studio's `BambuStudio.conf`, as a last-resort fallback

It is never printed, and never written to the config.

## Workflow

```bash
bambu recipe list                                   # prototype-pla, solid-pla, functional-pla, functional-petg
bambu slice part.3mf --recipe solid-pla             # STL / 3MF / OBJ
bambu slice part.stl --recipe prototype-pla --set wall_loops=3 --filament "Bambu PLA Matte"
bambu camera snapshot -o plate.jpg                  # look at the plate
bambu preflight part-solid-pla.gcode.3mf --slot 4   # PASS/WARN/FAIL gates
bambu print send part-solid-pla.gcode.3mf --slot 4 --confirm
bambu monitor --snapshot-at-layer 2 --exec 'notify-send "$BAMBU_EVENT $BAMBU_STATE"'
```

### Recipes

Recipes name presets generically (`"0.20mm Standard"`, `"Bambu PLA Basic"`), and `bambu` resolves the right system preset for your printer model and nozzle.

| recipe | for | settings |
|---|---|---|
| `prototype-pla` | fit/form checks | PLA Basic, 0.20 Standard, 2 walls, 15% gyroid |
| `solid-pla` | solid PLA parts | Generic PLA, 6 walls (inner 0.45), 100% zig-zag |
| `functional-pla` | strong PLA parts | 6 walls @ 0.45, 100% zig-zag, 5 top/bottom, elephant-foot 0.15 |
| `functional-petg` | strong/warm/outdoor parts | as functional-pla in PETG Basic; 252 °C, 70 °C bed, fan ≤ 30% |

- **No recipe adds a brim.** Brims are opt-in: `--set brim_type=outer_only --set brim_width=5`.
- **Custom recipes:** drop JSON files in `~/.config/bambu/recipes/`. The same name overrides a built-in. See `bambu recipe show solid-pla`.
- **`--set key=value`** takes any Bambu Studio setting key. Unknown keys are rejected before slicing. At 100% infill, patterns the slicer rejects (grid, gyroid, …) switch to zig-zag automatically.

### Safety model

1. **`preflight` checks before anything is sent:**
   - the printer is idle, with no blocking errors (informational HMS codes like "Inspecting first layer" are ignored)
   - Developer Mode is on and an SD card is present
   - the 3MF is intact and sliced for this printer model, with a single filament
   - the nozzle diameter and plate match
   - the slot is loaded with the same filament type (a different product of that type is a WARN)
   - there's enough filament (unknown remaining is a WARN), and the temperatures are sane
   - the printer's AI protections are on (WARN only) and FTPS login works
2. **`print send`** re-runs preflight and refuses on any FAIL. It requires `--confirm`, uploads over FTPS, reads the file back to verify its MD5, then starts the job with bed leveling, flow calibration, vibration compensation and first-layer inspection **on**.
3. **`print pause|resume|stop`** require `--confirm`. `bambu` has no command that changes printer settings.

### Exit codes

`bambu exit-codes`

| code | meaning |
|---|---|
| 0 | success |
| 1 | error |
| 2 | usage |
| 3 | empty |
| 4 | auth required / code refused |
| 5 | not found |
| 6 | forbidden (Developer Mode off) |
| 8 | retryable (unreachable/timeout) |
| 9 | gate failed (preflight FAIL, missing `--confirm`, busy) |
| 10 | config error / slicer not found |
| 11 | slice failed |
| 12 | print failed |
| 13 | print paused, needs a human |
| 14 | monitor timeout |

## For AI agents

- `bambu schema --json` returns the full command tree with flags. `bambu schema print send` returns one command.
- Every command takes `--json`. Errors come back as `{"error":{"code","name","message","hint"}}`, and the `hint` says what to do next.
- `status --watch --json` and `monitor --json` stream NDJSON events.
- There's an agent skill in [`skills/bambu/SKILL.md`](skills/bambu/SKILL.md). The recommended flow: status → slice → dry-run → show the human the summary, preview and snapshot → `--confirm` only after explicit approval.

## Slicer support

- **Bambu Studio** is discovered in this order:
  1. `BAMBU_STUDIO_PATH` / `[slicer] path`
  2. `/Applications/BambuStudio.app` (macOS)
  3. `bambu-studio` in the usual Linux paths, Flatpak, or an extracted AppImage
  4. `Program Files\Bambu Studio` (Windows)

  Only macOS is tested. A packed AppImage can't be read, so extract it (`--appimage-extract`) or set `[slicer] resources`.
- **Presets are flattened by `bambu`.** The Bambu Studio CLI does not resolve `inherits`/`include` in preset files. Unflattened presets slice "successfully" with a generic 200×200 bed and no printer start G-code.
- **STEP is not supported by the Bambu Studio CLI.** Export STL or 3MF instead.
- **OrcaSlicer** isn't supported yet. Its 2.4.x CLI has the same `inherits` problem (fixed upstream after 2.4.2).

## Credits

The LAN protocol details come from these open projects' public documentation and code. No code was copied.
- [OpenBambuAPI](https://github.com/Doridian/OpenBambuAPI) (GFDL docs): MQTT, FTPS and video.
- [ha-bambulab](https://github.com/greghesp/ha-bambulab) (MIT): HMS decoding, stage names, `print.fun` Developer Mode bit, `project_file` usage.
- [bambulabs_api](https://github.com/BambuTools/bambulabs_api) (MIT): implicit FTPS with TLS session reuse.
- [Bambu Studio](https://github.com/bambulab/BambuStudio) (AGPL-3.0): preset resolution order and AMS mapping format were read from its source; `bambu` invokes the installed app and ships none of its files.

Not affiliated with Bambu Lab. "Bambu Lab" is a trademark of its owner.

## License

MIT
