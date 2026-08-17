# qbit-redownloader

Detects stale RuTracker torrents in qBittorrent and replaces them by resolving
the immutable RuTracker `topic_id`. Title text is never used for matching.

A torrent is considered stale when the RuTracker API reports a different
`info_hash`, or when the RuTracker announcer reports that it is no longer
registered. Because the public API may be disabled, the tool can load the exact
topic page through a configurable FlareSolverr endpoint and extract its magnet.

The replacement is staged under the same save path, category and tags. The old
torrent is deleted with `deleteFiles=false` only after qBittorrent exposes the
exact expected hash and has received its metadata. A failed or ambiguous
resolution leaves the old torrent untouched.

## Usage

```bash
qbit-redownloader -config config.yaml
qbit-redownloader -dry-run    # report stale torrents without updating
qbit-redownloader -topic-id 6875876 -dry-run  # canary one exact topic
qbit-redownloader -topic-id 6875876 -adopt-hash <40-hex-hash>
qbit-redownloader -debug      # verbose logging
```

Configuration via YAML or environment variables:

```yaml
qbit:
  url: http://localhost:8080
  api_key: qbt_your-api-key
rutracker:
  forum_url: https://rutracker.org/forum
  flaresolverr_url: http://localhost:8191
```

Env vars override YAML: `QBIT_URL`, `QBIT_API_KEY`, `QBIT_API_KEY_FILE`,
`RUTRACKER_FORUM_URL`, `FLARESOLVERR_URL`. `QBIT_API_KEY_FILE` may point either
to a file containing only the key or to qBittorrent's `qBittorrent.conf`; in
the latter case `WebUI\APIKey=...` is extracted.

## Build

```bash
go build -o qbit-redownloader .
# or with Nix
nix build .#default
```

The executable has no Kubernetes-specific discovery or API dependency. Both
qBittorrent and FlareSolverr are ordinary configured HTTP endpoints.

## License

MIT
