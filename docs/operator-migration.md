## Moving from bambu-op to bambu

`bambu` covers the Python operator's printer controls, slicing and monitoring. The Python source can be retained for
rollback; new printer features belong in this repository. CAD dependencies and models remain separate from the CLI.

| Python command | Go command |
|---|---|
| `status --json` / `status --raw --json` | Same |
| `recipes` | `recipe list` |
| `slice MODEL --recipe R` | `slice MODEL --recipe R --strict` |
| `slice --slot A4` | Same suggested-slot metadata; send still requires `--slot` |
| `snapshot --light -o image.jpg` | `camera snapshot --light --output image.jpg` |
| `light status`, `light on`, `light off` | Same; on/off must be verified by telemetry |
| `preflight FILE --slot A4` | `preflight FILE --slot A4 --strict` |
| `send FILE --slot A4 --dry-run` | `print send FILE --slot A4 --strict --dry-run` |
| `send … --approved` | `print send … --strict --confirm` |
| `pause`, `resume --yes`, `stop --yes` | `print pause/resume/stop --confirm` |
| `filament load --slot A1 --yes` | `filament load --slot A1 --confirm` |
| `monitor --until-done --snapshot-at-layer 2 --light -o DIR` | `monitor --snapshot-at-layer 2 --light --snapshot-dir DIR` |
| `monitor` without `--until-done` | `monitor --watch` |
| `monitor --timeout 45` (minutes) | `monitor --timeout 45m` |
| `monitor --interval 10 --pushall-every 300` (seconds) | `monitor --interval 10s --pushall-every 5m` |
| `monitor --notify` | Same, with optional `[ntfy]` configuration |
| `auth set` | `auth set PRINTER` (stdin) |
| `auth set --ntfy` | Same (stdin, no printer argument) |

The repeated `--filament`, `--color` (`--colour` alias), `--filament-change-z`, and send/preflight `--slot` flags retain
filament order. A two-colour height-change slice embeds the AMS switch in G-code; remote filament loading is the
separate control for an approved paused job. The stricter Go guards against empty/unknown paused toolhead material
remain in place. A multi-filament `slice --slot` records one suggested slot; actual mapping is always passed to send.

Use `--strict` for operator slice/preflight/send. Default Go gate behavior stays compatible, including same-family
material warnings and omitted temperature bounds. Strict mode fails unequal material types and unknown or malformed
temperature bounds. A different product/profile remains a WARN in both tools. The strict slice requires a preview.

Go's slice JSON is the summary directly, without Python's `slice` wrapper. It includes
`objects[].footprint_incl_brim_mm`, the first plate's dimensions, and `suggested_slot`. `status` keeps a `status` wrapper
and adds its `lights` map. Access-code source is available through `auth status`.

Monitor emits NDJSON with `event`. The final event adds the complete `final` status, the last saved `snapshot`, and
`exit_code`. Interruption emits `interrupted: true`; it retains the existing successful interruption exit rather than
claiming the print finished. Continuous watch reports terminal transitions and continues; its deadline still returns
the current print outcome. Exit 12 means FAILED, 13 PAUSE, and 14 an active-job timeout. Slice failures use 11, gates 9,
usage 2. Existing `--exec` error hooks retain the blocking-error transition trigger; ntfy can notify new fault codes.
Notification failures are warnings and message events; they never change a print's outcome. Missing ntfy configuration
warns and disables notifications, matching the Python tool.

## Configuration and secrets

Go uses `~/.config/bambu/config.toml`, with named `[printers.NAME]` entries. Preserve the printer host, serial, installed
plate, output directory, and any custom Bambu Studio binary path when migrating the Python `[printer]` configuration.
Back up the old configuration first. Access codes use Keychain service `bambu`, account printer serial; ntfy tokens use
service `bambu-ntfy`, account `token`, or `BAMBU_NTFY_TOKEN`. Never put either token in configuration, output, or argv.

```toml
output_dir = "~/prints/out"

[slicer]
step_python = "/path/to/cadquery/venv/bin/python"

[ntfy]
url = "https://ntfy.example.com"
topic = "printer"
```

STEP/STP conversion uses the same CadQuery tolerances as Python: linear 0.01 mm and angular 0.1. The interpreter is
optional for STL/OBJ/AMF/3MF input; STEP requires CadQuery. A converter or slicer failure cannot reuse old output.

The migration changes tooling, not print approval. Starting a print, loading filament, pausing, resuming, and stopping
remain gated by the operator's existing approval policy. A dry run uploads and publishes nothing. Real print outcomes
and filament changes still need hardware verification on an approved job.
