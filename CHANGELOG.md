# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/).
JSON field names and exit codes are part of the public API.

## [Unreleased]

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
