# Remote Relay Demo (Two Processes)

This demo is split into **two independent processes**:

- `store/`: EventStore remote server process
- `relay/`: Nostr relay process that calls remote EventStore via RPC

Each process has its **own config file**.

## Directory Layout

- `demos/remote-relay/store`
  - `main.go`
  - `config.yaml` (event-store config)
- `demos/remote-relay/relay`
  - `main.go`
  - `remote_event_storage.go`
  - `config.yaml` (relay config)

## Quick Start

1) Start EventStore process:

```bash
cd demos/remote-relay/store
go mod tidy
go run . --config ./config.yaml
```

2) Start Relay process in another terminal:

```bash
cd demos/remote-relay/relay
go mod tidy
go run . --config ./config.yaml --port 7447
```

3) Connect Nostr client:

```text
ws://localhost:7447
```

## Config Separation

- `store/config.yaml`: storage/index/wal/remote server settings
- `relay/config.yaml`: remote client target and relay query behavior

Both default to `localhost:50051` + `api_key: demo-quick-start-key-2026`.
