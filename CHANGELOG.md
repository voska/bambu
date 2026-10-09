# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).
JSON field names and exit codes are part of the public API.

## [Unreleased]

### Added
- Operator parity with `bambu-op`: verified `light status|on|off`, chamber-light telemetry, and `--light` snapshots that restore the previous light mode even on failure or interruption.
- Monitor `--notify` with optional `[ntfy]` configuration and keychain/stdin token storage (`auth set --ntfy`), `--watch` for continuous monitoring, configurable `--interval`/`--pushall-every`, and full final status/snapshot/exit-code data.
- STEP/STP slicing through a CadQuery interpreter (`[slicer] step_python`), object footprints including brim, and `slice --slot` suggested-slot metadata.
- Opt-in `--strict` preflight/send gates for exact filament material and valid known temperature bounds; `slice --strict` requires a preview. The JSON preflight result records which gate mode ran.

- Two-filament jobs with an automatic AMS colour change at a layer height:
  - `slice --filament P1 --color C1 --filament P2 --color C2 --filament-change-z Z` (`--filament` and `--color` repeat once per filament; `--colour` is an alias). Filament 2 starts on the first layer above Z, which must be a layer top. `slice` reads the change back from the G-code and exits 11 without writing to the output dir if Bambu Studio didn't place it.
  - `preflight`/`print send` take one `--slot` per filament, in filament order. Each filament's type, profile, amount and temperature gates run against its own slot. `ams_mapping` maps each filament to its slot (e.g. `[3, 0]`). The same slot given twice exits 2.
- `filament load --slot S --confirm [--timeout 5m]`: unload the toolhead and load AMS slot S, e.g. a remote colour change at a pause. It runs only while paused or IDLE/FINISH/FAILED. When paused, the toolhead must hold an AMS filament and the slot must hold the same material. It exits 0 only once the printer reports S in the toolhead with the change finished. It exits 14 if that's unconfirmed and 1 if a new HMS code or print error appears during the change.
- JSON fields (additive):
  - `status.ams_status`
  - slice summary: `filament_changes`, `prime_tower`, `filament_presets`, and per-filament `model_g`/`waste_g`/`nozzle_temp`/`nozzle_temp_range`
  - preflight: `slots`/`tray_ids`, and `filament` on per-filament gate rows of multi-filament jobs
  - job: `filament_changes`

### Changed
- `preflight` `single_filament` now fails only when the number of `--slot`s differs from the number of filaments in the plate. Single-filament jobs send the same payload as before.

### Fixed
- `slice --set` now preserves JSON list values, including commas inside quoted entries; comma-separated batches and repeated overrides remain supported.
- `print pause` and `print stop` now verify PAUSE or an idle state before exiting 0; `--wait` defaults to 30s and must be positive.
- A failed retry cannot reuse a prior successful slice. Every slice verifies its requested filament changes, including an empty change list.
- Continuous monitoring preserves FAILED/PAUSE outcome codes on timeout and reports final state on interruption. Footprints correspond to plate 1, matching the inspected job and preview.
- `print resume --confirm` exited 0 even when the printer was still paused 3 s later. It now exits 0 only once the printer reports RUNNING (`--wait`, default 30s, must be positive; otherwise exit 14). It also refuses (exit 9) during a filament change, and on X1-series printers when the AMS reports an empty toolhead.
- Per-filament temperatures in multi-filament 3MFs are read through `filament_self_index`/`filament_extruder_variant` instead of plain indexing, which reads the wrong row whenever an earlier filament has more than one nozzle-variant row.

### Security
- Upgrade transitive `golang.org/x/net` to v0.55.0 to address the HTML parser CPU denial of service vulnerability (CVE-2026-25680 / GHSA-5cv4-jp36-h3mw).

## [0.2.0] - 2026-10-06

### Changed
- `status` JSON `camera_lan_liveview` is now nullable: absent, null, empty or malformed `ipcam.rtsp_url` reports produce `null` (human output: `unknown`), not a false disabled-setting claim. Explicit `disable` remains `false`; a nonempty advertised route remains `true`. Consumers must handle the new unknown state. No printer settings are changed. Fixes #1.

## [0.1.0] - 2026-09-23

### Added
- `status` (with `--watch` NDJSON streaming), `monitor` (layer-N snapshot, `--exec` hook, outcome exit codes).
- `slice` via the Bambu Studio CLI with flattened system presets, generic recipes (`prototype-pla`, `solid-pla`,
  `functional-pla`, `functional-petg`; no brims by default), `--filament`, validated `--set`, automatic zig-zag at 100% infill.
- `preflight` safety gates and `print send` (implicit FTPS upload + MD5 read-back + verified `project_file` payload),
  `print pause|resume|stop` behind `--confirm`.
- `camera snapshot` (RTSPS via ffmpeg) for X1/H2/P2S-series printers.
- Multi-printer TOML config, `printer add|list|remove|default|discover`, OS-keychain access codes (`auth set|status|remove`).
- `schema`, `exit-codes`, `version`, `recipe list|show`, `slicer info`; `--json`, `--plain`, `--quiet`, `--no-color`, `--no-input`.
