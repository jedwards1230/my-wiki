{{/*
Shared obsidian-headless sync scripts.

Both the standalone sync Deployment (sync-deployment.yaml) and the in-pod
init+sidecar path (deployment.yaml) run byte-identical scripts; they differ only
in what backs /data/home. Keep them here so a fix lands in one place.
*/}}

{{/*
my-wiki.syncHomeSeparate — "true" when the sync HOME needs its own volume.

obsidian-headless keeps a SQLite sync-state DB under $HOME (/data/home). SQLite's
WAL shared memory cannot be created on NFS, and RWX always means NFS — Longhorn
RWX included, since it exports through an NFSv4 share-manager pod. So the HOME
must come off the shared volume whenever that volume is RWX. That is every
standalone deployment (standalone mandates RWX) *and* any sidecar deployment on
an RWX PVC, e.g. replicaCount > 1. A default RWO or emptyDir /data is a real
block device or node-local disk, where SQLite is fine — leave those alone.
*/}}
{{- define "my-wiki.syncHomeSeparate" -}}
{{- if and .Values.persistence.enabled (eq .Values.persistence.accessMode "ReadWriteMany") -}}true{{- end -}}
{{- end }}

{{/*
my-wiki.syncSetupScript — init container: log in, configure sync if needed, and
run the initial sync with bounded retry and transient/permanent classification.
*/}}
{{- define "my-wiki.syncSetupScript" -}}
set -e
export HOME=/data/home
mkdir -p $HOME /data/vault

echo "Logging in to Obsidian..."
# `ob login` has no retry and exits non-zero on any failure —
# notably Obsidian's /user/signin origin intermittently returns
# HTTP 504 (Cloudflare gateway timeout) as an HTML page that the
# CLI's JSON parse chokes on. Retry with backoff so a transient
# failure doesn't crashloop the pod.
n=0
until ob login --email="$OBSIDIAN_EMAIL" --password="$OBSIDIAN_PASSWORD"; do
  rc=$?
  n=$((n+1))
  if [ "$n" -ge 10 ]; then echo "ob login failed after $n attempts (last exit code $rc)" >&2; exit 1; fi
  echo "ob login attempt $n failed (exit code $rc) — retrying in 30s..." >&2
  sleep 30
done

if ! ob sync-status --path /data/vault 2>/dev/null; then
  echo "Setting up sync for {{ .Values.obsidian.vault }}..."
  ob sync-setup \
    --vault {{ .Values.obsidian.vault | quote }} \
    --path /data/vault \
    --password "$OBSIDIAN_E2E_PASSWORD" \
    --device-name {{ .Values.obsidian.deviceName | quote }}
  ob sync-config \
    --path /data/vault \
    --excluded-folders {{ .Values.obsidian.excludedFolders | quote }} \
    --mode {{ .Values.obsidian.syncMode | quote }} \
    --device-name {{ .Values.obsidian.deviceName | quote }}
fi

# `ob sync` used to be bare under `set -e`, one line below a carefully retried
# `ob login`: any failure killed the init container instantly and Kubernetes
# restarted it. A permanent failure therefore spun the restart counter in
# silence — 1611 restarts over 8 days in the 2026-07 incident — indistinguishable
# from a flaky network.
#
# Retry alone does NOT fix that: the incident's failure was permanent and every
# retry would have failed identically. What matters is telling the two apart.
echo "Running initial sync..."
sync_out=$(mktemp)
sync_rc=$(mktemp)
attempt=0
max_attempts={{ .Values.obsidianSync.initialSync.maxAttempts | int }}
backoff={{ .Values.obsidianSync.initialSync.backoffSeconds | int }}
while :; do
  attempt=$((attempt+1))
  # tee so a long first sync streams to `kubectl logs` live, while the captured
  # copy is still available to classify. `sh` has no pipefail, so the real exit
  # status travels out of the pipeline through a file.
  { ob sync --path /data/vault 2>&1; echo "$?" >"$sync_rc"; } | tee "$sync_out"
  rc=$(cat "$sync_rc")
  if [ "$rc" -eq 0 ]; then
    echo "Init sync complete."
    break
  fi

  # Non-retryable: the storage underneath is wrong or full. Deliberately narrow
  # — a bare EACCES or "SqliteError" is usually a single unreadable file or lock
  # contention, which IS worth retrying, so those must not appear here.
  if grep -qE 'SQLITE_IOERR|SQLITE_CANTOPEN|SQLITE_READONLY|disk I/O error|ENOSPC|no space left on device' "$sync_out"; then
    echo "" >&2
    echo "=========== PERMANENT SYNC FAILURE (not retrying) ===========" >&2
    echo "ob sync failed with an error that will not clear on retry" >&2
    echo "(exit code $rc). Exiting now with code 3 rather than spending" >&2
    echo "the retry budget on attempts that cannot succeed." >&2
    echo "" >&2
    echo "Kubernetes still restarts this init container — what changes is" >&2
    echo "that the cause is stated here and exit code 3 is visible in the" >&2
    echo "pod's lastState.terminated.exitCode. Alert on it." >&2
    echo "" >&2
    echo "If the error mentions SQLite or disk I/O: obsidian-headless keeps" >&2
    echo "its SQLite sync state under HOME (/data/home), and SQLite's WAL" >&2
    echo "shared memory cannot be created on NFS — which every RWX volume" >&2
    echo "is, Longhorn RWX included. Set obsidianSync.homePersistence to" >&2
    echo "give /data/home its own ReadWriteOnce volume." >&2
    echo "=============================================================" >&2
    exit 3
  fi

  if [ "$attempt" -ge "$max_attempts" ]; then
    echo "" >&2
    echo "=========== SYNC FAILED AFTER $attempt ATTEMPTS ===========" >&2
    echo "ob sync did not succeed after $attempt attempts (last exit code $rc)." >&2
    echo "Exiting with code 4 — retries are exhausted, not misclassified." >&2
    echo "===========================================================" >&2
    exit 4
  fi

  echo "ob sync attempt $attempt failed (exit code $rc) — retrying in ${backoff}s..." >&2
  sleep "$backoff"
  backoff=$((backoff*2))
done
{{- end }}

{{/*
my-wiki.syncRunScript — long-running container: cap obsidian-headless's sync.log,
then hand the process off to `ob sync --continuous`.
*/}}
{{- define "my-wiki.syncRunScript" -}}
export HOME=/data/home
{{- if .Values.obsidianSync.logRotation.enabled }}

# obsidian-headless writes an ever-growing sync.log and never rotates it —
# 113 MB observed on a live deployment, over 5x the vault it was syncing.
# Truncate in place (`: >`) rather than unlink: the writer holds the file open
# in append mode, so this reclaims the space without needing a restart.
max_bytes=$(( {{ .Values.obsidianSync.logRotation.maxSizeMB | int }} * 1024 * 1024 ))
(
  while :; do
    sleep {{ .Values.obsidianSync.logRotation.intervalSeconds | int }}
    for f in "$HOME"/.config/obsidian-headless/sync/*/sync.log; do
      [ -f "$f" ] || continue
      size=$(wc -c < "$f" 2>/dev/null) || continue
      if [ "$size" -gt "$max_bytes" ]; then
        echo "Truncating $f ($size bytes > ${max_bytes} cap)"
        : > "$f"
      fi
    done
  done
) &
{{- end }}
{{- if .Values.obsidianSync.heartbeat.enabled }}

# Cross-pod sync-liveness signal. In standalone mode wiki-server cannot see the
# obsidian-headless sync state at all — that lives on this pod's RWO volume, by
# design (see obsidianSync.homePersistence). So mirror a timestamp onto the
# shared data volume, which both pods do mount.
#
# The timestamp is COPIED from obsidian-headless's own sync lock with `touch -r`,
# never generated here. That lock is a directory the sync process creates at
# <vault>/.obsidian/.sync.lock and whose mtime its own `setInterval` refreshes
# every second for as long as it holds it, releasing (rmdir) on clean shutdown.
# Touching on our own timer instead would report "this shell's sleep loop is
# alive" — which stays true long after sync has died, and is precisely the
# worthless signal this whole workstream exists to stop trusting.
#
# When the lock is absent (not started yet, or cleanly released) we touch
# nothing: a stale or missing heartbeat is the honest answer.
sync_lock=/data/vault/.obsidian/.sync.lock
(
  while :; do
    sleep {{ .Values.obsidianSync.heartbeat.intervalSeconds | int }}
    [ -e "$sync_lock" ] || continue
    touch -r "$sync_lock" {{ .Values.obsidianSync.heartbeat.path }} 2>/dev/null || true
  done
) &
{{- end }}

echo "Starting continuous sync..."
# exec so `ob` becomes PID 1 and receives SIGTERM directly — its sync command
# installs SIGINT/SIGTERM handlers that close the SQLite state store, which the
# intermediate shell would otherwise swallow, leaving a dirty WAL behind.
exec ob sync --path /data/vault --continuous
{{- end }}

{{/*
my-wiki.syncReadinessScript — has sync started, and is its event loop alive?

Takes (dict "staleAfter" <int>). Asserts two things:

  1. At least one state.db exists — initial sync has produced state. Its *mtime*
     is deliberately not checked (see below); only its existence.
  2. The sync lock exists and its mtime is within staleAfter.

State-db mtime measures *activity*, not liveness, and cannot carry a readiness
verdict: obsidian-headless's `_sync()` does no work on a tick with nothing to do,
so a quiet vault and a wedged sync produce the identical frozen-mtime signature.
A vault that is merely idle overnight therefore pins the Deployment at
Available=False forever, which destroys the signal — you cannot alert on NotReady
if it is always NotReady. Observed 2026-07-29: the sync pod sat NotReady for four
days on `newest write 38180s ago` while sync was healthy and its lock was one
second old.

The lock carries it instead: the sync process's own timer refreshes it every
second regardless of vault activity, so a stale lock means the event loop has
stopped. Unlike liveness, an *absent* lock fails here — readiness may honestly
report "not started yet"; liveness must not restart on it.

staleAfter must stay below the liveness threshold so readiness degrades before a
restart fires, leaving a NotReady window for a human or an alert to see.

Deliberately does NOT consider sync.log. obsidian-headless tees every log line
there, including the errors it catches and swallows on each poll, so a wedged
sync that error-loops keeps sync.log fresh indefinitely.

Linux-only: `stat -c %Y` is the GNU/BusyBox spelling. That is fine here — this
only ever runs inside the container — but it does not port to BSD/macOS, where
the equivalent is `stat -f %m`.
*/}}
{{- define "my-wiki.syncReadinessScript" -}}
d=/data/home/.config/obsidian-headless/sync
lock=/data/vault/.obsidian/.sync.lock
have_state=0
for f in "$d"/*/state.db; do
  if [ -f "$f" ]; then have_state=1; break; fi
done
if [ "$have_state" -eq 0 ]; then
  echo "no obsidian-headless sync state under $d yet; initial sync has not completed" >&2
  exit 1
fi
if [ ! -e "$lock" ]; then
  echo "sync lock $lock is absent; the sync process is not holding the vault" >&2
  exit 1
fi
m=$(stat -c %Y "$lock" 2>/dev/null) || m=0
if [ "$m" -eq 0 ]; then
  echo "cannot read mtime of sync lock $lock" >&2
  exit 1
fi
age=$(( $(date +%s) - m ))
if [ "$age" -gt {{ .staleAfter | int }} ]; then
  echo "sync lock stale: refreshed ${age}s ago (limit {{ .staleAfter | int }}s); the sync process refreshes it every second, so its event loop has stopped" >&2
  exit 1
fi
exit 0
{{- end }}

{{/*
my-wiki.syncLockLivenessScript — is the sync process's event loop still running?

Liveness must only fire on unambiguous death, because its consequence is a
restart. Sync-state freshness cannot carry that burden: obsidian-headless's
`_sync()` does no work on a tick with nothing to do, so a genuinely quiet vault
and a sync stuck in an error loop produce the *identical* mtime signature —
frozen state. Restarting on that would kill healthy containers overnight.

The lock directory can carry it. Its mtime is refreshed every second by the sync
process's own timer, independent of whether the vault changed, so a stale lock
means the event loop has stopped — not that the vault is quiet. A missing lock
means sync has not started or has exited; the container is then either still
initializing (pass, and let the pod's own restart handle a dead PID 1) or being
reported on by readiness instead.

Readiness keys off the same lock (see my-wiki.syncReadinessScript) on a shorter
threshold, so it degrades first and a restart is never the only warning.

Linux-only: `stat -c %Y` is the GNU/BusyBox spelling (BSD/macOS is `stat -f %m`).
*/}}
{{- define "my-wiki.syncLockLivenessScript" -}}
lock=/data/vault/.obsidian/.sync.lock
# Not started, or cleanly released — nothing to judge.
[ -e "$lock" ] || exit 0
m=$(stat -c %Y "$lock" 2>/dev/null) || exit 0
age=$(( $(date +%s) - m ))
if [ "$age" -gt {{ .staleAfter | int }} ]; then
  echo "sync lock stale: refreshed ${age}s ago (limit {{ .staleAfter | int }}s); the sync process refreshes it every second, so its event loop has stopped" >&2
  exit 1
fi
exit 0
{{- end }}
