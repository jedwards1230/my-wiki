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
| `wiki_vault_scan_errors_total` | Counter | Vault scan cycles that failed. Emitted from zero. |
| `wiki_sync_state_last_modified_timestamp_seconds` | Gauge | Newest mtime at `WIKI_SYNC_STATE_PATH`. Only registered when that variable is set; reports `0` while the path does not exist. |
| `wiki_sync_state_read_errors_total` | Counter | Sync-state reads that failed for a reason **other than** the path not existing. Only registered when `WIKI_SYNC_STATE_PATH` is set. |

The two error counters are deliberately separate: a climbing `wiki_vault_scan_errors_total` unambiguously means the vault is unreadable, with no need to guess whether it was really the sync-state path.

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
- A **missing sync-state path** is the deliberate exception: it reports the
  sentinel `0` rather than going absent, so the staleness alert fires. See
  [Configured-but-missing reports `0`](#configured-but-missing-reports-0) — the
  short version is that a sync which *never started* would otherwise be silent.
  It is still not an *error*: no counter moves, because "has not written its
  heartbeat yet" is a legitimate state. Only a real read failure (permissions,
  I/O) increments `wiki_sync_state_read_errors_total`, and that case keeps the
  previous value. A heartbeat that is *deleted* returns to the sentinel rather
  than carrying its last value forward — otherwise a dead sync would look alive
  indefinitely.

Both error counters are exported from zero, because 0 errors is a true and
meaningful value — unlike a timestamp, where 0 means the epoch.

### Alerting

Both alerts are needed. The first alone cannot distinguish a dead sync from a
quiet week: if the observer (or the whole server) is dead, its gauges freeze or
disappear, and a stale `last_modified` proves nothing. `last_scan` is the
liveness proof that makes `last_modified` interpretable.

**Pair every staleness alert with an `absent()` alert.** Because the vault
metrics are absent rather than zero (see above), `(time() - metric) > threshold`
evaluates over an empty vector and **never fires** when the metric disappears.
Staleness and absence are different queries; you need both, or the exact
failure the absence design protects against becomes invisible instead of noisy.

(The sync-state gauge is the one exception — it uses a sentinel precisely so its
staleness query fires unaided. `absent()` is still worth adding there for
defence in depth.)

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

# Optional: the vault is unreadable (this counter is vault-only).
rate(wiki_vault_scan_errors_total[15m]) > 0
```

### The sync heartbeat — the alert that actually catches a dead sync

**Alert 1 is not sufficient on its own.** Any write to the vault refreshes
`wiki_vault_last_modified_timestamp_seconds` — including agent/API writes, which
also touch `meta/activity/*.md`. In a vault that agents write to regularly, a
dead sync sidecar leaves alert 1 **quiet**. Vault mtime measures "did anything
change", not "is the sync process alive".

`WIKI_SYNC_STATE_PATH` closes that gap. It points at something the sync process
touches **every tick, whether or not anything changed** — so staleness means
"sync dead" rather than "vault quiet". The path may be:

- **a heartbeat file** (recommended) — e.g. `/data/.sync-heartbeat`. This is the
  form that works when the sync process keeps its own state on a separate
  ReadWriteOnce volume the server cannot mount: the two sides agree on a file on
  a *shared* volume instead. Keep it **outside the vault directory**, or it gets
  indexed as wiki content and synced back to Obsidian.
- **a directory** — newest mtime among the sync **state DB** files within
  (`state.db`, `state.db-wal`), matched at any depth. Only usable where the
  server can actually read the sync tool's state directory.

> **The directory form reads only the state DB files, by design.** Scanning the
> whole tree would be actively harmful: obsidian-headless tees its log to
> `sync.log` *including the errors it catches and swallows on each failed poll*,
> so a wedged, error-looping sync keeps that file perpetually fresh — the gauge
> would stay alive through exactly the outage it exists to catch. (A 113MB
> `sync.log` against a 22MB vault was observed in the incident; in-place log
> rotation bumps its mtime too.) `state.db-shm` is excluded for the same reason:
> it is a 3-byte stub recreated on every crash, so its freshness proves the
> process is *crashing*, not syncing.

> This is an **allowlist**, not an exclusion list, because a blocklist fails
> open — any newly added log or lock file would silently re-contaminate the
> gauge. An allowlist fails closed: an unrecognised file is ignored, and if
> nothing matches, the gauge goes *absent*, which `absent()` alerts on. The set
> is configurable (`freshness.Config.SyncStateFiles`) so the package stays
> generic.

```promql
(time() - wiki_sync_state_last_modified_timestamp_seconds) > 3600
rate(wiki_sync_state_read_errors_total[15m]) > 0
```

#### Configured-but-missing reports `0`, not absence

This gauge is the one deliberate exception to "absent, not zero", and the
exception is what makes it work.

The 2026-07 outage was an **init-container crashloop** — `ob sync --continuous`
never started, so the heartbeat was *never created*, not "created then went
stale". Had a missing path reported by absence, `(time() - gauge) > 3600` would
have evaluated over an empty vector and stayed **silent for all 8 days**. Only a
separately-written `absent()` rule would have caught it, which makes "someone
remembered to add the second rule" a single point of failure.

So when the path is **configured but missing**, the gauge reports `0` — old
enough that the primary staleness query fires. This degrades toward alerting
instead of toward silence.

The rule elsewhere in this file still holds, because the cases differ in whether
there is an observation at all. Emitting `0` for a vault we have not scanned yet
would be a lie about a healthy system. Emitting `0` for a heartbeat the operator
explicitly declared and that is not there is simply **true**.

| State | Gauge |
|---|---|
| `WIKI_SYNC_STATE_PATH` unset | absent (never opted in) |
| configured, path missing | `0` → staleness alert fires |
| configured, path present | real mtime |

**When the sentinel actually appears — narrower than "every rollout".** The
heartbeat lives on the shared data volume and nothing deletes it, so once the
first successful touch has happened a restart leaves a *stale* file, not a
missing one. The sentinel is therefore limited to:

- a first-ever deploy,
- a fresh/replaced PVC,
- manual deletion of the heartbeat.

Routine rollouts and upgrades do **not** produce it. (Nor does the #172
migration, which deletes the old sync-state directory, not the heartbeat.)

**Sizing `for:` — the init container dominates, not the heartbeat interval.**
The obvious reading is that the window is one heartbeat interval (60s). It is
not. Both init retry paths sleep, and a first deploy that ultimately *succeeds*
can spend a long time in them:

| Init step | Worst case before a success |
|---|---|
| `ob login` | 10 attempts, 30s apart → **270s** |
| initial `ob sync` | 5 attempts, 30s doubling (30+60+120+240) → **450s** |

That is **~720s (12 min) of pure sleep on a flaky-but-recovering first deploy**,
before counting login round-trips and the initial vault sync itself. So `for:
15m` is too tight and will page on a deploy that was going to succeed.

Two options, better one second:

```promql
# Simple: widen the window past the worst-case init path.
#   for: 30m

# Better: suppress while the sync pod isn't Ready. Readiness gates on the sync
# lock — the same signal the heartbeat form of this gauge is copied from — so a
# short for: stays safe. (With the directory form the two diverge: readiness
# tracks the lock, the gauge tracks the state DB.)
(time() - wiki_sync_state_last_modified_timestamp_seconds) > 3600
  unless on(namespace) kube_pod_status_ready{pod=~".*-sync-.*"}
```

`absent()` is still worth pairing for defence in depth — it catches the server
being gone entirely, which no value-based query can.

#### What this gauge does and does not prove

It proves the sync process's event loop is **running**. It does *not* prove
syncs are **succeeding** — and the difference matters when reading an alert.

`requestSync()` catches sync errors in continuous mode, logs them, and swallows
them, retrying every 30s indefinitely. Throughout that the process stays alive
and keeps refreshing whatever the heartbeat derives from, so an error-looping
sync reads as *alive* here. That is correct behaviour: the distinction this
gauge draws is alive-vs-dead, not healthy-vs-unhealthy. Do not read a fresh
heartbeat as "sync is healthy".

The gap is partly covered from the other side. An error-looping sync is not
writing to the vault, so `wiki_vault_last_modified_timestamp_seconds` goes stale
while the heartbeat stays fresh. **Heartbeat fresh + vault mtime stale is the
error-loop signature**:

```promql
(time() - wiki_vault_last_modified_timestamp_seconds) > 7 * 86400
  and (time() - wiki_sync_state_last_modified_timestamp_seconds) < 3600
```

> **This query is weaker the more agents write to the vault — read its silence
> as "no information", not "healthy".** The left-hand side assumes vault mtime
> tracks *sync* activity, but **any** write refreshes it, including agent and
> API writes (which also touch `meta/activity/`). On a vault that agents write
> to regularly the left side never goes stale, so the query never fires even
> with sync error-looping. It is sharp on a human-only vault and degrades toward
> useless as agent traffic rises — which, for an agent-first deployment, means
> assume it is degraded.

Catching an error loop *directly* needs log content, not mtimes, and is out of
scope for a filesystem-observing metric. If you need that coverage, alert on the
error rate in `sync.log` via the log pipeline rather than trying to infer it
here.

### Configuration

Both variables are declared in
[`internal/cli/envvars.go`](../internal/cli/envvars.go) (the canonical
inventory).

| Variable | Format | Default | Effect |
|---|---|---|---|
| `WIKI_VAULT_FRESHNESS_INTERVAL` | Go duration | `5m` | Scan cadence. Non-positive disables the observer entirely (no metrics registered). Coarser than the 60s inbox poll on purpose: a "N days" dead-man's switch needs no finer granularity, and a full vault walk over NFS is expensive. |
| `WIKI_SYNC_STATE_PATH` | path (file or directory) | empty | When set, also export `wiki_sync_state_last_modified_timestamp_seconds` (newest mtime at that path) and `wiki_sync_state_read_errors_total`. Unset ⇒ neither is registered nor emitted. A path that does not exist yet is not an error, but reports the sentinel `0` so the staleness alert fires. |

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
