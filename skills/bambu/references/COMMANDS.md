# bambu command reference

Generated from `bambu schema`. Run `bambu <command> --help` for the authoritative, current flags.

## Global flags

| flag | help |
|---|---|
| `--json, -j` | JSON on stdout (NDJSON for `status --watch` and `monitor`). Also `BAMBU_JSON=1`. |
| `--plain, -p` | Tab-separated output, no color. |
| `--quiet, -q` | Only the primary value (state, path, PASS/FAIL). |
| `--no-color` | Disable colors (also `NO_COLOR`). |
| `--no-input` | Never prompt. |
| `--printer, -P` | Printer name (`BAMBU_PRINTER`; default: `default_printer`, or the only one). |
| `--config` | Config file (`BAMBU_CONFIG`; default `~/.config/bambu/config.toml`). |

## Commands

### `bambu auth remove <name>`

Delete a printer's access code from the keychain (requires --confirm).

| flag | type | default | help |
|---|---|---|---|
| `--confirm` | bool |  | Required. |

### `bambu auth set <name>`

Store a printer's LAN access code in the OS keychain (read from stdin).

| flag | type | default | help |
|---|---|---|---|
| `--no-check` | bool |  | Don't verify the code against the printer. |

### `bambu auth status`

Show where each printer's access code comes from; --check connects (read-only).

| flag | type | default | help |
|---|---|---|---|
| `--check` | bool |  | Connect to each printer (read-only) to verify the code. |

### `bambu camera snapshot`

Save one camera frame as JPEG (X1/H2/P2S series; needs ffmpeg and LAN Mode Liveview).

| flag | type | default | help |
|---|---|---|---|
| `--output, -o` | string |  | Output .jpg (default ./<printer>-<time>.jpg). |

### `bambu exit-codes`

Print the exit code table.

### `bambu monitor`

Follow the current job until it ends; exit code reflects the outcome.

| flag | type | default | help |
|---|---|---|---|
| `--snapshot-at-layer` | int |  | Save one camera frame when this layer is reached (0 = off). |
| `--snapshot-dir` | string | . | Directory for snapshots. |
| `--exec` | string |  | Shell command run on events (snapshot, error, final) with BAMBU_* env vars. |
| `--timeout` | time.Duration |  | Give up after this long (exit 14 if the job is still active). 0 = no limit. |
| `--start-grace` | time.Duration | 120s | If no job is active, how long to wait for one to start. |

### `bambu preflight <file>`

Run the safety gates for a sliced file (read-only).

| flag | type | default | help |
|---|---|---|---|
| `--slot, -s` | string |  | AMS slot: 1-4 (first AMS) or A1-D4. (required) |
| `--no-ftp` | bool |  | Skip the FTPS login check. |

### `bambu print pause`

Pause the running job (requires --confirm).

| flag | type | default | help |
|---|---|---|---|
| `--confirm` | bool |  | Required: this acts on a real print. |

### `bambu print resume`

Resume a paused job (requires --confirm).

| flag | type | default | help |
|---|---|---|---|
| `--confirm` | bool |  | Required: only resume once the cause of the pause is fixed. |

### `bambu print send <file>`

Upload a sliced file and start it (requires --confirm and a passing preflight).

| flag | type | default | help |
|---|---|---|---|
| `--slot, -s` | string |  | AMS slot: 1-4 (first AMS) or A1-D4. (required) |
| `--confirm` | bool |  | Required to actually print. Only pass it after a human approved this exact file and confirmed the plate is clear. |
| `--dry-run, -n` | bool |  | Run preflight and show the exact upload path and MQTT payload; send nothing. |
| `--timelapse` | bool |  | Record a timelapse. |
| `--wait` | time.Duration | 180s | How long to wait for the job to start. |

### `bambu print stop`

Abort the running job, irreversibly (requires --confirm).

| flag | type | default | help |
|---|---|---|---|
| `--confirm` | bool |  | Required: stopping is irreversible. |

### `bambu printer add <name>`

Add or update a printer (idempotent).

| flag | type | default | help |
|---|---|---|---|
| `--host` | string |  | IP address or hostname. (required) |
| `--serial` | string |  | Serial number (printer Settings > Device, or bambu printer discover). (required) |
| `--model` | string | X1C | Model: X1C, X1, X1E, P1P, P1S, A1M, A1, P2S, A2L, X2D, H2D, H2DP, H2S, H2C. |
| `--nozzle` | float64 | 0.4 | Installed nozzle diameter (mm). |
| `--plate` | string | textured_plate | Installed build plate. |
| `--default` | bool |  | Make this the default printer. |

### `bambu printer default <name>`

Set the default printer.

### `bambu printer discover`

Listen for printers announcing themselves on the LAN (SSDP, UDP 2021).

| flag | type | default | help |
|---|---|---|---|
| `--timeout` | time.Duration | 10s | How long to listen (printers announce every few seconds). |

### `bambu printer list`

List configured printers.

### `bambu printer remove <name>`

Remove a printer from the config (requires --confirm).

| flag | type | default | help |
|---|---|---|---|
| `--confirm` | bool |  | Required. |

### `bambu recipe list`

List recipes (built-in and user).

### `bambu recipe show <name>`

Show one recipe.

### `bambu schema [command]`

Dump the command tree as JSON for agents.

### `bambu slice <model>`

Slice a model headlessly with Bambu Studio and a recipe.

| flag | type | default | help |
|---|---|---|---|
| `--recipe, -r` | string |  | Recipe name (see: bambu recipe list). (required) |
| `--filament, -f` | string |  | Filament preset to use instead of the recipe's (e.g. "Generic PLA", "Bambu PLA Matte"). |
| `--set, -s` | []string |  | Override a Bambu Studio setting: key=value (repeatable). |
| `--name, -n` | string |  | Output base name (default <model>-<recipe>). |
| `--out, -o` | string |  | Output directory (default: config output_dir, else current dir). |
| `--auto-orient` | bool |  | Let the slicer re-orient the model (default: keep the model's orientation). |

### `bambu slicer info`

Show the discovered Bambu Studio and whether the printer's presets resolve.

### `bambu status`

Printer status (read-only). --watch streams changes.

| flag | type | default | help |
|---|---|---|---|
| `--watch, -w` | bool |  | Stream changes (NDJSON with --json) until interrupted. |
| `--raw` | bool |  | Include the raw merged push_status object (--json only). |

### `bambu version`

Print version information.

