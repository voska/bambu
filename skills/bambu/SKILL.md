---
name: bambu
description: Slices, safety-checks, sends and monitors Bambu Lab 3D printer jobs over LAN with the bambu CLI. Use when printing a model (STL/3MF) on a Bambu Lab printer (X1C, X1, X1E, P1S, P1P, A1, H2D…), checking printer status, AMS filament, HMS errors, the camera, or when slicing headlessly with Bambu Studio presets and recipes.
allowed-tools: Bash(bambu *)
---

# bambu — Bambu Lab LAN CLI

## Install

```bash
brew install voska/tap/bambu                       # macOS / Linux
go install github.com/voska/bambu/cmd/bambu@latest # any platform
# Binaries: https://github.com/voska/bambu/releases
```

You also need Bambu Studio (for `slice`) and ffmpeg (for camera snapshots).

## Printer prerequisites

The printer must be in **LAN Only mode with Developer Mode on** (printer: Settings → LAN Only). Developer Mode disconnects it from Bambu Cloud and Handy. The access code is shown on that screen and changes whenever those toggles change.

## Setup

```bash
bambu printer discover                                  # optional; same LAN/VLAN only
bambu printer add shop --host 192.168.1.50 --serial <serial> --model X1C --nozzle 0.4 --plate textured_plate
echo "<access code>" | bambu auth set shop              # stored in the OS keychain, verified read-only
bambu auth status --check --json && bambu slicer info --json
```

## Safety rules for agents (non-negotiable)

1. **Never pass `--confirm` on your own.** `print send --confirm` starts a real print. Only use it after a human explicitly approved **this exact file** and confirmed **the plate is clear**. The same goes for `print pause|resume|stop --confirm`, except that pausing on a visible failure is acceptable (tell the human).
2. **Always run `print send --dry-run` first.** It runs every preflight gate and shows the exact payload. Resolve FAILs and report WARNs.
3. **Don't touch a busy printer.** If `status` isn't IDLE/FINISH/FAILED, a job is running; it may be the human's.
4. `bambu` cannot and must not change printer settings. Ask the human for filament swaps, plate swaps and toggles.

## Workflow

```bash
bambu status --json                                            # state, AMS trays (type/filament_id/remain_g), errors, dev_mode
bambu recipe list                                              # prototype-pla | solid-pla | functional-pla | functional-petg
bambu slice part.3mf --recipe functional-petg --json           # → gcode_3mf, preview_png, time_s, weight_g, settings
bambu camera snapshot -o plate.jpg                             # current plate
bambu print send part-functional-petg.gcode.3mf --slot A2 --dry-run --json
# → show the human: time, grams, material/slot, key settings, preview PNG, snapshot; ask for approval + "plate clear"
bambu print send part-functional-petg.gcode.3mf --slot A2 --confirm --json   # only after approval
bambu monitor --snapshot-at-layer 2 --json                     # NDJSON events; look at the layer-2 snapshot
```

**Recipe choice:**
- `prototype-pla` for fit checks.
- `solid-pla` / `functional-pla` for stiff PLA parts that stay under about 50 °C.
- `functional-petg` for load-bearing, warm or outdoor parts.

**Overrides:**
- **Brims are opt-in:** `--set brim_type=outer_only --set brim_width=5`.
- **Match the loaded spool** with `--filament "Generic PLA"` (or `"Bambu PLA Matte"`, `"Generic PETG"`).
- **Any Bambu Studio key** works with `--set key=value`. Unknown keys fail with exit 2.

**Slots** are `1`–`4` (first AMS) or `A1`–`D4`. Preflight checks that the slot's filament type matches the sliced one.

## Reading results

- `status.errors.blocking` is the flag that matters. Informational HMS codes (e.g. `0C00_0300_0003_000B` "Inspecting first layer" around layer 2) are listed but don't block. Each HMS entry has a wiki `url`.
- `preflight.result` is `PASS` or `FAIL`. `gates[]` has `status`, `detail` and a `fix` for every non-PASS gate.
- **`monitor` exit codes:**
  - 0: finished
  - 12: FAILED
  - 13: PAUSED (e.g. a first-layer or spaghetti halt; needs a human)
  - 14: `--timeout` hit while still printing
- Calibration before layer 1 can take about 8 minutes (layer stays 0). That's normal.

## Agent introspection

```bash
bambu schema --json                 # full command tree with flags
bambu schema print send             # one command
bambu exit-codes --json
```

## Exit codes

0 ok · 1 error · 2 usage · 3 empty · 4 auth required/refused · 5 not found · 6 forbidden (Developer Mode off) · 8 retryable ·
9 gate failed (preflight FAIL / missing --confirm / busy) · 10 config · 11 slice failed · 12 print failed · 13 print paused · 14 timeout.
Errors in `--json` mode: `{"error":{"code","name","message","hint"}}`. The hint says what to do next.

## Reference

See [references/COMMANDS.md](references/COMMANDS.md) for every command and flag.
