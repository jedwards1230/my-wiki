# Metrics

`wiki-server` exposes Prometheus metrics on `GET /metrics` (HTTP server only —
`serve` / `serve http`; the standalone `serve mcp http` listener has no metrics
endpoint). The endpoint bypasses the readiness gate and gzip, so a scraper gets
the raw exposition format even before the first render completes.

Alerting *rules* live in the deploying cluster's monitoring stack, not here.
This document describes the signals the server emits.

## Vault freshness (dead-man's switch)

The sync path fails silently: when the Obsidian-Sync sidecar dies, clippings
simply stop arriving. Downstream automation only fires on change, so "sync is
dead" and "quiet week" produce identical observable behaviour — nothing
happens. There is no positive signal to miss. These metrics *are* that positive
signal.

Emitted by `internal/freshness`, which walks the vault on an interval
(`WIKI_VAULT_FRESHNESS_INTERVAL`, default `5m`) wherever `/metrics` is served —
`serve` / `serve http`, including when MCP runs alongside via `--mcp-port`.
Crucially it is **not** gated on the webhook dispatch pipeline: gating it the
way the inbox poller is gated would leave a default deployment with no signal,
which is the exact problem these metrics exist to solve.

| Metric | Type | Meaning |
|---|---|---|
| `wiki_vault_last_modified_timestamp_seconds` | Gauge | Newest file mtime observed in the vault, Unix seconds. |
| `wiki_vault_last_scan_timestamp_seconds` | Gauge | Unix time the last **successful** scan completed. |
| `wiki_vault_files` | Gauge | Files counted in the last successful scan (all files, not just `.md`). |
| `wiki_vault_scan_duration_seconds` | Gauge | Duration of the last successful scan. |
| `wiki_vault_scan_errors_total` | Counter | Scan cycles that failed. Emitted from zero. |
| `wiki_sync_state_last_modified_timestamp_seconds` | Gauge | Newest mtime under `WIKI_SYNC_STATE_DIR`. Only registered when that variable is set. |

Scan rules: files only (directory mtimes are inconsistent across filesystems);
top-level `.obsidian/` excluded (`vault.DefaultExcludedDirs`); every file
counts, because a `raw/` attachment is real vault content.

### Absence is deliberate

Snapshot-derived metrics are **absent until the first successful scan**, not
zero. A registered-but-never-`Set` gauge reports `0`, which reads as "last
modified at the Unix epoch" — ~56 years stale — and would fire a false alarm on
every startup, permanently so for a CrashLooping pod. That is worse than no
metric, so the exporter is a custom collector that emits nothing it has not
measured. Two corollaries:

- An **empty vault** exports `wiki_vault_files 0` and a fresh
  `wiki_vault_last_scan_timestamp_seconds`, but no
  `wiki_vault_last_modified_timestamp_seconds` — there is no mtime to report.
- A **failed scan** increments `wiki_vault_scan_errors_total` and keeps the
  previous good snapshot. A transient NFS hiccup must not regress a good
  observation to "never scanned".

`wiki_vault_scan_errors_total` is exported from zero, because 0 errors is a
true and meaningful value.

### Alerting

Both alerts are needed. The first alone cannot distinguish a dead sync from a
quiet week: if the observer (or the whole server) is dead, its gauges freeze or
disappear, and a stale `last_modified` proves nothing. `last_scan` is the
liveness proof that makes `last_modified` interpretable.

**Pair every staleness alert with an `absent()` alert.** Because these metrics
are absent rather than zero (see above), `(time() - metric) > threshold`
evaluates over an empty vector and **never fires** when the metric disappears.
Staleness and absence are different queries; you need both, or the exact
failure the absence design protects against becomes invisible instead of noisy.

```promql
# 1. The vault has not changed in 7 days.
#    Only trustworthy while alerts 2 and 3 are quiet.
(time() - wiki_vault_last_modified_timestamp_seconds) > 7 * 86400

# 2. The observer itself is dead, or every scan is failing.
#    900s comfortably exceeds the 5m default scan interval.
(time() - wiki_vault_last_scan_timestamp_seconds) > 900
absent(wiki_vault_last_scan_timestamp_seconds)

# 3. The vault reports zero files — an empty or unmounted volume. The
#    last_modified gauge vanishes here, so alert 1 goes quiet exactly when
#    something is most wrong. This is the alert that catches it.
absent(wiki_vault_last_modified_timestamp_seconds)
wiki_vault_files == 0

# Optional: scans are running but erroring (unreadable vault or sync-state dir).
rate(wiki_vault_scan_errors_total[15m]) > 0
```

With `WIKI_SYNC_STATE_DIR` set, a third alert is a strictly better dead-man's
switch than vault mtime alone: a sync process that touches its state on every
tick keeps this fresh even when the vault is quiet, so staleness means "sync
dead" rather than "vault quiet".

```promql
(time() - wiki_sync_state_last_modified_timestamp_seconds) > 3600
absent(wiki_sync_state_last_modified_timestamp_seconds)
```

This matters more than it looks. Any write to the vault refreshes
`wiki_vault_last_modified_timestamp_seconds` — including agent/API writes, which
also touch `meta/activity/*.md`. In a vault that agents write to regularly, a
dead sync sidecar therefore leaves alert 1 **quiet**. The sync-state gauge is
the one that isolates "the sync process stopped" from "the vault is quiet", so
set `WIKI_SYNC_STATE_DIR` in any deployment that runs a sync sidecar.

### Configuration

Both variables are declared in
[`internal/cli/envvars.go`](../internal/cli/envvars.go) (the canonical
inventory).

| Variable | Format | Default | Effect |
|---|---|---|---|
| `WIKI_VAULT_FRESHNESS_INTERVAL` | Go duration | `5m` | Scan cadence. Non-positive disables the observer entirely (no metrics registered). Coarser than the 60s inbox poll on purpose: a "N days" dead-man's switch needs no finer granularity, and a full vault walk over NFS is expensive. |
| `WIKI_SYNC_STATE_DIR` | path | empty | When set, also export `wiki_sync_state_last_modified_timestamp_seconds` (newest mtime under that directory, no exclusions). Unset ⇒ the gauge is neither registered nor emitted. |

## HTTP

Emitted by `internal/middleware`. `pattern` is the Go 1.22+ `ServeMux` route
pattern (`unknown` when unmatched), which keeps label cardinality bounded.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wiki_http_requests_total` | Counter | `method`, `pattern`, `status` | Total HTTP requests. |
| `wiki_http_request_duration_seconds` | Histogram | `method`, `pattern` | Request duration. |

## Webhook dispatch

Emitted by `internal/dispatch`, only when the pipeline is enabled
(`WIKI_WEBHOOKS_CONFIG`).

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `wiki_webhook_dispatch_total` | Counter | `event`, `consumer`, `outcome` | Deliveries by terminal outcome (`success`/`dropped`/`circuit_open`). |
| `wiki_webhook_dispatch_duration_seconds` | Histogram | `event`, `consumer` | Duration of a single dispatch attempt. |
| `wiki_webhook_retry_total` | Counter | `consumer` | Retry attempts. |
| `wiki_webhook_queue_depth` | Gauge | `consumer` | Current per-consumer queue depth. |

The default Go runtime and process collectors (`go_*`, `process_*`) are also
exported.
