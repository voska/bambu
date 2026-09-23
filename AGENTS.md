# bambu — Bambu Lab LAN CLI

CLI for humans and AI agents: slice (Bambu Studio CLI), preflight, send, monitor and snapshot Bambu Lab printers
over LAN (Developer Mode). Data goes to stdout (parseable); hints/progress to stderr. SPEC.md is the build contract.

## Build & Test

- `make build`: `bin/bambu` (CGO_ENABLED=0, version via ldflags)
- `make fmt` / `make fmt-check`: goimports (local prefix) + gofumpt, tools pinned in `.tools/`
- `make lint`: golangci-lint v2 (`.golangci.yml`)
- `make test`: unit tests with `-race` (fakes for MQTT, FTPS, Bambu Studio, ffmpeg; no printer needed)
- `make ci`: fmt-check + lint + vet + test + build (the local gate; CI runs the same)
- `make live-test`: READ-ONLY checks against a real printer (`scripts/live-test.sh`)

## Project Structure

- `cmd/bambu/` — thin entrypoint (ldflags version) → `internal/cmd.Execute`
- `internal/cmd/` — kong commands (`Run(*Globals)`), output views, exit mapping
- `internal/config/` — TOML config, printer selection
- `internal/auth/` — keychain (go-keyring) + access-code resolution (keychain → env → Bambu Studio conf)
- `internal/printer/` — MQTT client (`Conn` interface), status summary, HMS decoding, model table
- `internal/ftps/` — implicit-TLS FTPS client with TLS session reuse
- `internal/job/` — 3MF inspection, verified `project_file` payload, AMS mapping
- `internal/preflight/` — safety gates
- `internal/slicer/` — Bambu Studio discovery, preset flattening, recipe resolution, CLI invocation
- `internal/recipe/` — embedded recipes (`recipes/*.json`) + user recipes
- `internal/camera/`, `internal/discover/` — ffmpeg snapshot, SSDP discovery
- `internal/output/`, `internal/errfmt/` — output modes, exit codes
- `skills/bambu/` — agent skill; `site/` — GitHub Pages; `docs/` — protocol notes

## Output & Exit Codes

`--json` (NDJSON for `status --watch`/`monitor`), `--plain` (TSV), `--quiet` (primary value), `--no-color`.
0 ok · 1 error · 2 usage · 3 empty · 4 auth · 5 not found · 6 forbidden · 8 retryable · 9 gate failed · 10 config ·
11 slice failed · 12 print failed · 13 print paused · 14 timeout. Field names are an API contract (semver).

## Safety Rules (do not weaken)

- `print send` needs `--confirm` and a passing preflight; `--dry-run` never uploads or publishes.
- `print pause|resume|stop` need `--confirm`. No command changes printer settings.
- Never print/log access codes; never commit IPs, serials or codes (tests use 192.0.2.x and fake serials).
- Never run live tests that write to a printer; `scripts/live-test.sh` is read-only.

## Commits

Conventional Commits (`feat:`, `fix:`, `docs:`, `test:`, `chore:`), imperative summary; update CHANGELOG.md.
