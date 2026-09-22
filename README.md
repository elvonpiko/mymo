# mymo

mymo is a local-first, terminal-native tool for managing a small fleet of
Linux VPSes — and, on servers you choose, for running predictable Docker
HTTP applications behind Caddy.

One binary on your workstation. No mymo server, no cloud, no accounts.

## What mymo is

- A keyboard-first TUI (plus a matching CLI) that answers: *what servers do I
  have, are they reachable, and what is running on them?*
- A record of your fleet: node names instead of memorized IPs and SSH commands.
- A non-mutating **observe** mode: connect, inspect, and report on any node.
- An optional **app host** mode: prepare selected nodes with a pinned, versioned
  baseline (SSH hardening, Docker, Caddy), deploy containerized HTTP apps,
  gate releases behind health checks, and roll back cleanly.
- Local-first: all state lives in `~/.mymo` as readable JSON. mymo works fine
  even when you are not deploying anything.

## What mymo is not

- Not a PaaS, not Kubernetes, not an infrastructure-as-code language.
- Not a CI provider, monitoring stack, or generic firewall manager.
- Not required at runtime by your servers: plain SSH remains a first-class
  escape hatch at every step.

## Status

mymo is in early development. Implemented so far:

- Local state store (`~/.mymo`, JSON, restrictive permissions)
- Node model: connection metadata, observe mode, validation
- CLI: `mymo node list / add / inspect / rm`, `mymo version`
- TUI: a full-window workspace — fleet, node overview and actions,
  settings, add-node workflow, first-run intro, toasts, and per-screen
  key help

SSH discovery, node bootstrap, and application deployment land in the next
phases.

## Install

Requires Go 1.26 or newer.

    git clone https://github.com/elvonpiko/mymo.git
    cd mymo
    make build

Until tagged releases are published, build from source. `go install
github.com/elvonpiko/mymo@latest` will work once releases exist.

## Usage

Run `mymo` with no arguments for the interactive workspace TUI. Inside
it, `?` shows the key help for the current screen, and `q` quits:

    mymo

Non-interactive commands:

    mymo node list              # show all known nodes
    mymo node add               # add a node (interactive form or flags)
    mymo node inspect <name>    # show a node's stored record
    mymo node rm <name>         # remove a node from local state
    mymo version                # print the version

`mymo node add` also accepts flags for scripted use:

    mymo node add -name web-1 -host 203.0.113.10 -user root -auth key -key ~/.ssh/id_ed25519

## State and security

- State lives in `~/.mymo` (created with `0700`), files are written `0600`.
- State is plain JSON and safe to inspect.
- mymo never stores passwords. SSH keys are referenced by path, never copied.

## Development

    make build   # build ./mymo
    make test    # run all tests
    make vet     # go vet

## License

[MIT](LICENSE)
