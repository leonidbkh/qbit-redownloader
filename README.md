# qbit-redownloader

Detects stale rutracker torrents in qBittorrent and replaces them with fresh
copies via Prowlarr.

A torrent is considered stale when the rutracker API reports a different
`info_hash` for the same topic, or when the topic's `tor_status` indicates the
release is obsolete. The tool re-downloads the `.torrent` file through Prowlarr
(used purely as a download proxy) and re-adds it to qBittorrent under the same
save path, category and tags, then deletes the old torrent without removing
files.

## Usage

```bash
qbit-redownloader -config config.yaml
qbit-redownloader -dry-run    # report stale torrents without updating
qbit-redownloader -debug      # verbose logging
```

Configuration via YAML or environment variables:

```yaml
qbit:
  url: http://localhost:8080
  username: admin
  password: adminadmin
prowlarr:
  url: http://localhost:9696
  api_key: your-prowlarr-api-key
```

Env vars override YAML: `QBIT_URL`, `QBIT_USERNAME`, `QBIT_PASSWORD`,
`PROWLARR_URL`, `PROWLARR_API_KEY`.

## Build

```bash
go build -o qbit-redownloader .
# or with Nix
nix build .#default
```

## NixOS

The flake exposes `packages.default` for use as a flake input. See
[homelab-nix](https://github.com/leonidbkh/homelab-nix) for an example
systemd service + timer integration.

## License

MIT
