# Security Policy

## Supported versions

Only the latest released version of `bambu` receives security fixes. Please upgrade (`brew upgrade bambu` or
`go install` the latest tag) before reporting.

## Reporting a vulnerability

Please report security issues **privately** — do not open a public issue.

- Preferred: GitHub's [private vulnerability reporting](https://github.com/voska/bambu/security/advisories/new)
  (the "Report a vulnerability" button on the repository's **Security** tab).

We aim to acknowledge reports within 5 business days and to ship a fix or mitigation for confirmed issues as quickly
as is practical, crediting reporters who wish to be named.

## Handling credentials and the printer

`bambu` controls a machine that heats to 300 °C, so:

- The printer's LAN access code is stored in the OS keychain (service `bambu`, account = serial). It is never
  printed, logged or written to the config file. `BAMBU_ACCESS_CODE` exists for CI/headless use — keep it out of shell
  history and shared logs.
- The code is passed to `ffmpeg` in the RTSPS URL for camera snapshots and is therefore briefly visible to local
  process listings; errors are redacted.
- Printers use self-signed TLS certificates, so `bambu` does not verify them. Only use it on networks you trust.
- `print send`, `print pause|resume|stop` require `--confirm`; `print send` also requires a passing preflight.
  `bambu` never changes printer settings. Never let an agent pass `--confirm` without a human's approval.
