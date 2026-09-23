# bambu — SPEC (v0.1)

`bambu` is a CLI for Bambu Lab printers in **LAN mode with Developer Mode enabled**. It slices headlessly with the
Bambu Studio CLI, runs safety checks, sends jobs, monitors them and grabs camera frames. It is meant for humans and
AI agents. This file is the build contract; README.md is the user-facing doc.

## 1. Scope

In v0.1:
- Multiple named printers in a TOML config.
- Access code stored in the OS keychain.
- Printer discovery over SSDP.
- Headless slicing (STL/3MF/OBJ) with Bambu Studio system presets, flattened by us, plus built-in and user recipes.
- Preflight safety gates.
- LAN send: implicit FTPS upload, read-back MD5, then the MQTT `project_file` command.
- Pause / resume / stop.
- Status, and a streaming `status --watch`.
- `monitor` with a layer-N snapshot and an exec hook.
- Camera snapshot for RTSPS models (X1/X1E/H2/P2S/X2D) via ffmpeg.
- Introspection: `schema`, `exit-codes`, `version`, `auth status`.

Out of scope for v0.1:
- Cloud API and Bambu Connect.
- Multi-material/multi-color send: preflight refuses jobs with more than one filament.
- STEP input (convert to STL/3MF first).
- The P1/A1 camera, which uses the port-6000 JPEG protocol: a clear error for now.
- Changing any printer setting. `bambu` never changes printer settings: no xcam/AI toggles, no lights, no temperatures.
- OrcaSlicer (see §8).

## 2. Settled decisions

| Topic | Decision | Why |
|---|---|---|
| Language/CLI | Go 1.25+, `alecthomas/kong` | same as voska/qbo-cli |
| Config | TOML at `$BAMBU_CONFIG`, else `$XDG_CONFIG_HOME/bambu/config.toml`, else `~/.config/bambu/config.toml` (macOS and Linux), `%AppData%\bambu\config.toml` (Windows) | `tobiasbischoff/bambu-cli` uses `~/.config/bambu/config.json`; we only ever read/write `config.toml`, so the two coexist |
| Secrets | `zalando/go-keyring`; service `bambu`, account `<serial>` | pure Go; on macOS it writes via `security -i` on stdin, so the secret is never in argv. Keyed by serial so renaming a printer keeps its code |
| Access code resolution | keychain → `BAMBU_ACCESS_CODE` → (macOS/Windows/Linux) Bambu Studio's `BambuStudio.conf` (`access_code`/`user_access_code` by serial), used only as a fallback | explicit, discoverable; source is reported by `auth status` |
| MQTT | `eclipse/paho.mqtt.golang` | stable, small |
| FTPS | small built-in implicit-TLS client (`internal/ftps`) | the printer requires TLS session reuse on data connections and implicit TLS on 990; we need STOR, RETR, NLST, SIZE only |
| Camera | shell out to `ffmpeg` (`-rtsp_transport tcp -tls_verify 0`) | ffmpeg ≥ 8 verifies TLS by default; the printer cert is self-signed |
| Slicer | Bambu Studio CLI, presets flattened by us (§8) | the CLI does not resolve `inherits`/`include` |
| Colors | `muesli/termenv`; honours `NO_COLOR` / `--no-color` | qbo-cli pattern |

## 3. Command tree

The tree is noun-verb where there is a real noun: `printer`, `auth`, `print`, `camera`, `recipe`, `slicer`.
Top-level verbs are kept for the high-frequency, single-purpose actions an operator types all the time: `status`, `slice`, `preflight`, `monitor`.

```
bambu status [--watch]                       # read-only; --watch streams NDJSON until Ctrl-C
bambu slice <model> --recipe R [--filament PRESET] [--set k=v]... [--name N] [--out DIR] [--auto-orient]
bambu preflight <file.gcode.3mf> --slot S [--no-ftp]
bambu print send <file.gcode.3mf> --slot S (--dry-run | --confirm) [--timelapse] [--wait 180s]
bambu print pause|resume|stop --confirm
bambu monitor [--snapshot-at-layer N] [--snapshot-dir D] [--exec CMD] [--timeout 2h]
bambu camera snapshot [-o file.jpg]
bambu printer add <name> --host H --serial S [--model X1C] [--nozzle 0.4] [--plate textured_plate] [--default]
bambu printer list | remove <name> --confirm | default <name> | discover [--timeout 10s]
bambu auth set <printer>           # access code from stdin (or TTY prompt)
bambu auth status [--check]        # per printer: source of the code; --check connects (read-only)
bambu auth remove <printer> --confirm
bambu recipe list | show <name>
bambu slicer info                  # discovered Bambu Studio binary/resources/version
bambu schema [command...]          # CLI tree as JSON
bambu exit-codes
bambu version
```

Global flags:
- `--json/-j` (env `BAMBU_JSON=1`), `--plain/-p`, `--quiet/-q`, `--no-color`, `--no-input`
- `--printer/-P NAME` (env `BAMBU_PRINTER`; otherwise the config default; otherwise the only printer)
- `--config PATH` (env `BAMBU_CONFIG`)

Slots: `1`–`4` (first AMS) or `A1`…`D4`, plus `ext` for the external spool (tray 254).
- tray id = ams_index × 4 + slot_index (A1 = 0).
- For the external spool, `use_ams` is false and `ams_mapping` is `[254]`. Documented, unverified.

## 4. Output contract

- **stdout is data and stderr is for humans.** `--json` prints one JSON object. `--watch`/`monitor` print NDJSON, one event per line.
- **`--plain`** prints TSV of a command's key fields.
- **`--quiet`** prints the primary value only:
  - status: state
  - slice: the path of the .gcode.3mf
  - snapshot: the path of the image
  - send: state
  - preflight: PASS/FAIL
- **Errors in `--json` mode** print `{"error":{"code":N,"name":"...","message":"...","hint":"..."}}` to stdout **and** a human line to stderr.
- **Field names** are snake_case and stable. This is an API contract under semver.

## 5. Exit codes

| code | name | meaning |
|---|---|---|
| 0 | success | |
| 1 | error | unexpected |
| 2 | usage | bad flags/args, unknown setting key |
| 3 | empty | nothing found (e.g. `printer discover` saw none, `printer list` empty) |
| 4 | auth_required | no access code, or MQTT/FTPS refused the code |
| 5 | not_found | file, printer name, preset or recipe not found |
| 6 | forbidden | printer rejected the command (Developer Mode off / signature required) |
| 8 | retryable | printer unreachable / timeout |
| 9 | gate_failed | preflight FAIL, missing `--confirm`, printer busy |
| 10 | config_error | bad/missing config, slicer not found |
| 11 | slice_failed | Bambu Studio returned non-zero (`return_code` in JSON) |
| 12 | print_failed | monitor: job ended FAILED |
| 13 | print_paused | monitor: job PAUSED (e.g. first-layer or spaghetti halt); needs a human |
| 14 | timeout | monitor `--timeout` reached while the job is still active |

## 6. Config

```toml
default_printer = "workshop"
output_dir = "."                 # where slice writes <name>.gcode.3mf/.png/.summary.json
recipes_dir = ""                 # extra recipes (*.json); default <config dir>/recipes

[slicer]
path = ""                        # Bambu Studio binary; env BAMBU_STUDIO_PATH wins
resources = ""                   # resources dir containing profiles/BBL.json (auto-detected)

[printers.workshop]
host = "192.168.1.50"
serial = "01S00A000000000"
model = "X1C"                    # alias → Bambu Studio printer_model (see internal/printer/models.go)
nozzle = 0.4
plate = "textured_plate"         # installed plate: textured_plate|cool_plate|eng_plate|hot_plate|supertack_plate
```

Precedence:
- Printer selection: `--printer` > `BAMBU_PRINTER` > `default_printer` > the single configured printer.
- Output dir: `--out` > `output_dir` > cwd.

## 7. Printer protocol (see docs/protocol.md)

**MQTT:**
- `mqtts://host:8883`, user `bblp`, password = access code, TLS without verification (the printer cert is self-signed).
- Subscribe to `device/<serial>/report`; publish to `device/<serial>/request`.
- `{"pushing":{"sequence_id":"0","command":"pushall"}}` returns the full status.
- Developer Mode is detected from `print.fun`: bit `0x20000000` set means signatures are required, so Developer Mode is off.

**FTPS:**
- Implicit TLS on port 990, user `bblp`. Sequence: `PBSZ 0`, `PROT P`, `TYPE I`, `PASV`.
- Data connections resume the control TLS session. Connect to the configured host, not the PASV IP.
- Upload to `/<name>.gcode.3mf`, then read it back (`RETR`) and compare MD5.

**`project_file` payload** (verified against X1C firmware 01.12 on 2026-09-23 → `result: SUCCESS`, then RUNNING):

```json
{"print":{"sequence_id":"<n>","command":"project_file","param":"Metadata/plate_1.gcode",
 "url":"file:///sdcard/<name>.gcode.3mf","subtask_name":"<name>","md5":"<md5 of Metadata/plate_1.gcode>",
 "project_id":"0","profile_id":"0","task_id":"0","subtask_id":"0","bed_type":"auto",
 "timelapse":false,"bed_leveling":true,"flow_cali":true,"vibration_cali":true,"layer_inspect":true,
 "use_ams":true,"ams_mapping":[<tray id per project filament, -1 unused>]}}
```

**Control:** `{"print":{"sequence_id":"<n>","command":"pause"|"resume"|"stop","param":""}}` at QoS 1.

**HMS:**
- Code = `%04X_%04X_%04X_%04X` of attr>>16, attr&0xFFFF, code>>16, code&0xFFFF.
- Module = attr>>24; severity = code>>16 (1 fatal, 2 serious, 3 common, 4 info).
- Informational codes are an allowlist (`0C00_0300_0003_000B` "Inspecting first layer") plus severity `info`. `errors.blocking` is true when there is a `print_error` or any non-informational HMS code.

## 8. Slicing

**Slicer discovery**, in order:
1. `BAMBU_STUDIO_PATH` / `slicer.path`
2. macOS: `/Applications/BambuStudio.app`, `~/Applications/BambuStudio.app`
3. Linux: `/usr/bin/bambu-studio`, `/opt/bambu-studio/bin/bambu-studio`, the Flatpak export (`/var/lib/flatpak/app/com.bambulab.BambuStudio/current/active/files/bin/bambu-studio`, same under `~/.local/share/flatpak`), and an extracted AppImage (`squashfs-root/AppRun`). A packed AppImage has no readable profiles: extract it (`--appimage-extract`), or set `slicer.resources`.
4. Windows: `%ProgramFiles%\Bambu Studio\bambu-studio.exe`

Resources are the first of `../Resources`, `../resources`, `../share/BambuStudio`, `./resources` (relative to the binary) that contains `profiles/BBL.json`.

**Preset flattening** mirrors `PresetBundle::load_vendor_configs_from_json`:
- Resolution order is resolved parent (`inherits`), then each `include` (minus name/type/from/setting_id), then own keys.
- Names are resolved via `BBL.json` `{machine,process,filament}_list` (nameless include templates are keyed by list name).
- Drop `inherits`, `include` and `instantiation`; set `from = "system"`.

**Recipe resolution:** recipes name presets **generically** (`"process": "0.20mm Standard"`, `"filament": "Bambu PLA Basic"`), and we pick the variant for the printer. Candidates are presets with the exact name or `name + " @…"`, with `instantiation` true, whose `compatible_printers` contains the machine preset (`<printer_model> <nozzle> nozzle`). Exact name wins, then the shortest. So `bambu` works for any model and nozzle Bambu ships presets for.

**Bed type:** `curr_bed_type` comes from the printer's configured plate unless the recipe or `--set` overrides it.

**`--set key=value`:**
- The key must exist in a flattened config (or be `curr_bed_type`), otherwise exit 2.
- Array settings broadcast a scalar to the existing length; a JSON array is accepted as-is.

**100% infill:** if `sparse_infill_density` is 100% and the pattern isn't in the solid-OK set, switch to `zig-zag` and add a note.

**Invocation:**
- Absolute `--outputdir` (a work dir under the user cache). Bambu Studio chdirs into its bundle, so relative paths break.
- `--slice 0 --arrange 1 --orient 0|1 --load-settings "m.json;p.json" --load-filaments f.json --export-3mf <name>.gcode.3mf <model>`
- Parse `result.json` (`return_code`, `error_string`, `sliced_plates`) and capture untimestamped stderr lines as `details`.

**Outputs:**
- `<out>/<name>.gcode.3mf`
- `<out>/<name>.png`: `Metadata/plate_1.png`, because `--export-png` can't be combined with `--slice`
- `<out>/<name>.summary.json`

**OrcaSlicer:** unsupported for now. 2.4.x CLI ignores `inherits` (#14718, fixed in #15438 after 2.4.2). Documented.

## 9. Recipes (embedded, `internal/recipe/recipes/*.json`; user recipes override by name)

All recipes use `brim_type = no_brim`. **Brims are opt-in** (`--set brim_type=outer_only --set brim_width=5`).

| name | process | filament | overrides |
|---|---|---|---|
| prototype-pla | 0.20mm Standard | Bambu PLA Basic | 2 walls, 15% gyroid |
| solid-pla | 0.20mm Standard | Generic PLA | 6 walls, inner wall 0.45, 100% zig-zag |
| functional-pla | 0.20mm Standard | Bambu PLA Basic | 6 walls, all widths 0.45, 100% zig-zag, 5 top/5 bottom, EFC 0.15 |
| functional-petg | 0.20mm Standard | Bambu PETG Basic | as functional-pla; nozzle 252 (both), textured-plate bed 70, fan max 30 (overhang fan left at preset 50) |

## 10. Preflight gates (FAIL blocks send; WARN informs)

| gate | FAIL when | WARN when |
|---|---|---|
| printer_idle | gcode_state not IDLE/FINISH/FAILED | |
| no_errors | `errors.blocking` | |
| developer_mode | fun bit set / unknown | |
| sdcard | no SD card | |
| file_integrity | plate gcode md5 ≠ embedded .md5 | |
| sliced_for_printer | 3MF `printer_model` ≠ configured model | |
| single_filament | > 1 filament used | |
| nozzle_diameter | ≠ printer | |
| | | nozzle_type: material differs |
| bed_type | 3MF plate ≠ configured plate | |
| tray | slot empty, or type ≠ file type | filament_id differs (e.g. GFA01 vs GFA00) |
| filament_amount | remain_g < used_g × 1.15 + 5 | remaining unknown |
| nozzle_temp | outside filament range | |
| | | ai_protections: first_layer_inspector / spaghetti_detector / print_halt off |
| ftps_login | login + NLST failed | |

## 11. Package layout

```
cmd/bambu/main.go            entrypoint, version ldflags, error → exit code
internal/cmd/                kong command structs (Run(*Globals))
internal/config/             TOML config, printer selection
internal/auth/               keyring interface + access-code resolution (keyring/env/Studio conf)
internal/printer/            status model + summary, HMS, models table, MQTT client (interface Transport)
internal/ftps/               implicit-TLS FTPS client
internal/job/                3MF inspection, project_file payload, AMS mapping
internal/preflight/          gates
internal/slicer/             discovery, preset index/flatten, recipe resolution, invocation, result parsing
internal/recipe/             embedded + user recipes
internal/camera/             ffmpeg snapshot
internal/discover/           SSDP listener
internal/output/             modes, JSON/plain/human writers, colors
internal/errfmt/             exit codes, typed errors with hints
```

## 12. Safety rules (non-negotiable)

- `print send` requires `--confirm` and a PASS from preflight, re-run at send time. `--dry-run` never connects FTPS for writing and never publishes.
- `print pause|resume|stop` require `--confirm`.
- No command changes printer settings.
- The access code is never printed, never logged, and never written to config. `ffmpeg` errors are redacted.
- `--no-input` or a non-TTY stdin never prompts.

## 13. Milestones

1. Scaffold (Makefile, lint, CI, AGENTS.md), errfmt/output/config/auth.
2. Printer status + HMS + MQTT; `status`, `auth`, `printer`.
3. Slicer discovery + flatten + recipes + slice.
4. 3MF inspection + preflight + payload; `print send --dry-run`.
5. FTPS client; `print send`, control commands; `monitor`, `camera`.
6. Discovery, schema, docs, site, skill, release config.
