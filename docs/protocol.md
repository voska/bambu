# Bambu LAN protocol notes

What `bambu` relies on, and what has been verified against real hardware (X1 Carbon, firmware 01.12, Developer Mode,
September 2026).

## MQTT (status and control)

- **Connection:** `mqtts://<host>:8883`, user `bblp`, password = the LAN access code. The printer uses a self-signed cert.
- **Topics:** subscribe to `device/<serial>/report`, publish to `device/<serial>/request`.
- **Status:** `{"pushing":{"sequence_id":"0","command":"pushall"}}` requests the full status. X1 printers also push
  about one partial `print.push_status` per second while printing; merge them. (verified)
- **Developer Mode:** `print.fun` is a hex bitfield. Bit `0x20000000` means signed commands are required, i.e. Developer Mode is **off**. (verified: `…1A30…` off, `…1830…` on)
- **Control:** `{"print":{"sequence_id":"<n>","command":"pause"|"resume"|"stop","param":""}}` at QoS 1. (not yet verified)

## Start a job: `print.project_file` (verified)

```json
{"print":{"sequence_id":"<n>","command":"project_file","param":"Metadata/plate_1.gcode",
 "url":"file:///sdcard/<name>.gcode.3mf","subtask_name":"<name>","md5":"<md5 of Metadata/plate_1.gcode>",
 "project_id":"0","profile_id":"0","task_id":"0","subtask_id":"0","bed_type":"auto",
 "timelapse":false,"bed_leveling":true,"flow_cali":true,"vibration_cali":true,"layer_inspect":true,
 "use_ams":true,"ams_mapping":[3]}}
```

- **Reply:** `{"print":{"command":"project_file","result":"SUCCESS",…}}`, and `gcode_state` goes PREPARE/RUNNING within seconds.
- **`ams_mapping`:** Bambu Studio's "v0" format (`SelectMachine.cpp::get_ams_mapping_result`). One entry per project filament; the value is the global tray id (`ams_index*4 + slot`, 0-based); `-1` = unused. `[3]` = first AMS, slot 4 (verified).
- **Without Developer Mode**, the reply is a failure mentioning verification and HMS `0500_0500_0001_0007`. `bambu` maps that to exit 6.

## FTPS (upload)

- **Connection:** implicit TLS on `<host>:990`, user `bblp`. The sequence is `PBSZ 0` → `PROT P` → `TYPE I` → `PASV`.
- **Data connections must resume the control connection's TLS session.** `bambu` shares one `tls.Config` with a session cache and an explicit `ServerName`, and does the data-channel TLS handshake after sending the transfer command. (verified: `NLST`, `SIZE`, `RETR` against the printer; `STOR` shares the same code path)
- **Upload location:** jobs go to the SD root, `/<name>.gcode.3mf`, the same place Bambu Studio puts them.

## Camera

- **X1/H2/P2S series:** `rtsps://bblp:<code>@<host>:322/streaming/live/1`. It needs "LAN Mode Liveview" on (`ipcam.rtsp_url` ≠ `disable`). ffmpeg ≥ 8 needs `-tls_verify 0`. (verified: 1760×1080 JPEG in about 2.5 s)
- **P1/A1 series:** a proprietary JPEG stream on port 6000. Not implemented yet.

## HMS

- **Code format:** `%04X_%04X_%04X_%04X` of `attr>>16, attr&0xFFFF, code>>16, code&0xFFFF`.
- **Module and severity:** module = `attr>>24` (`03` motion, `05` mainboard, `07` AMS, `08` toolhead, `0C` xcam); severity = `code>>16` (1 fatal, 2 serious, 3 common, 4 info).
- **Wiki link:** `https://wiki.bambulab.com/en/x1/troubleshooting/hmscode/<code>`.
- **Informational codes don't block.** `0C00_0300_0003_000B` ("Inspecting first layer") appears around layer 2 while the lidar scans, and severity `info` codes are also treated as informational. (verified during a real print)

## Discovery

Printers broadcast SSDP `NOTIFY` to UDP 2021 with these headers:
- `Location` (IP)
- `USN` (serial)
- `DevModel.bambu.com` (model id, e.g. `BL-P001` = X1C)
- `DevName.bambu.com`
- `DevConnect.bambu.com`

Bambu Studio binds UDP 2021 exclusively while it runs. Broadcasts don't cross VLANs.

## Slicing gotchas (Bambu Studio 02.08, macOS)

- **Presets must be flattened.** The CLI does not resolve `inherits`/`include` in `--load-settings`/`--load-filaments` files. Unflattened system presets slice with `printable_height` 100, a 200×200 bed, generic start G-code and zero filament density.
- **Use absolute paths.** The CLI changes directory into its bundle (`Contents/Resources`), so relative `--outputdir`/`--export-3mf` paths land inside the app bundle, or fail with `-13`.
- **`--export-png` can't be combined with `--slice`.** `bambu` uses the 3MF's `Metadata/plate_1.png` as the preview instead.
- **Exit codes:** the shell sees `256 + return_code`. `result.json` holds `return_code`/`error_string`; untimestamped stderr lines hold the reason.
- **100% sparse infill** only accepts patterns that are also valid top-surface patterns (e.g. `zig-zag`, `monotonic`, `concentric`). Otherwise it fails with `-18`.
- **STEP input** fails with `-6`.
