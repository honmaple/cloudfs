# Synchronization Guide

Use `Sync` with `Option.Mode`: `ModeOneWay` is the default one-way mode. `ModeTwoWay` propagates changes and deletions in both directions and requires `StateStore`.

Import `github.com/honmaple/cloudfs/syncer` to synchronize `cloudfs.FS` instances.
`Sync` uses `/` in each supplied filesystem. To synchronize
subdirectories, wrap the filesystems with `middleware.PrefixFS` first:

```go
source, err := cloudfs.New(src, middleware.PrefixFS("/documents"))
if err != nil {
    return err
}
target, err := cloudfs.New(dst, middleware.PrefixFS("/backup"))
if err != nil {
    return err
}
err = syncer.Sync(ctx, source, target) // The same wrappers work with either mode.
```

## Custom Filtering

`Option.Skipper` accept `syncer.Skipper`, a `func(info syncer.FileInfo) bool`. Return true to exclude a path:

```go
option := syncer.Option{
	Skipper: func(info syncer.FileInfo) bool {
		return info.Path() == "/cache" || strings.HasSuffix(info.Path(), ".tmp")
	},
}
```

Import `strings` for this example. The callback runs synchronously during each scan, before reading file contents for checksums; `Hash()` is empty for these scan snapshots. The root `/` is not passed to the callback. Paths are absolute within the supplied FS. Excluded directories are not traversed and their descendants are not passed to the callback. The callback receives only the current scan snapshots. Paths excluded by the current scans, including descendants hidden by an excluded directory, retain their previous baseline. Historical paths confirmed absent from both scans are removed unless covered by an exclusion. Do not mutate filesystems or metadata in the callback.

A match on either side excludes that path and descendants on both sides, preserves previous metadata, and emits neither conflict callbacks nor task events. An ancestor deletion or type replacement that would affect an excluded child is skipped for the whole subtree. New directories can still be created with their included children. Nil disables filtering.

Excluded files are not opened for `Checksum`, and excluded directories are not listed. Planning checks both scan exclusion markers as it traverses each path; a match on either side prunes that path or subtree and prevents hashing on both sides. Listing the parent is still necessary to discover entries; errors encountered before filtering are returned. After changing filters, the next call scans again and compares against retained history.

## Built-in Conflict Handlers

Both synchronization options accept a `ConflictHandler` through `OnConflict`. Nil defaults to `Abort()`:

```go
option := syncer.Option{
    Mode: syncer.ModeTwoWay,
    StateStore: store,
    OnConflict: syncer.PreferNewer(),
}
```

| Handler | Conflict behavior |
| --- | --- |
| `Abort()` | Stop the run with `ErrSyncConflict` before writes. |
| `Skip()` | Preserve the conflicting path and old state; continue other paths. |
| `PreferSrc()` | Choose the source version, including source deletions. |
| `PreferDst()` | Choose the target version, including target deletions; one-way sync preserves the target without writing back. |
| `KeepBoth()` | Preserve both versions in bidirectional sync, as described below. |
| `PreferNewer()` | Restore the survivor in a deletion conflict; compare known times for two regular files, preferring the source on ties. |

`PreferNewer()` compares supplied modification times without reading contents; the two sides' times must be comparable. When both entries exist, a directory or an unknown timestamp selects abort because directory snapshots do not retain modification times. In one-way sync, a newer target is preserved without copying back to the source. This handler affects only conflicts passed to it; one-way `Delete` still controls target-only entry removal.

Custom callbacks still return a `ConflictPolicy` and an error. They can select `ConflictAbort`, `ConflictSkip`, `ConflictPreferSrc`, `ConflictPreferDst`, or `ConflictKeepBoth` per path, or call a built-in handler. Callback errors retain their original text and stop the run before writes.

## Concurrent Copies

`Option.Concurrency` limits parallel copies and final file removals. Both the default 0 and the value 1 run serially; negative values are invalid:

```go
option := syncer.Option{
    Mode: syncer.ModeTwoWay,
    Concurrency: 4,
    StateStore:  store,
}
```

Values above 1 require both supplied filesystems and all their middleware to support concurrent calls. Operations run in stages: rename, removals required for type replacement, mkdir, copy, remove files, then remove directories. Each stage finishes before the next begins. Directories are created shallowest first and removed deepest first. Copies and final file removals run up to `Concurrency` tasks at a time, dispatching another task as soon as a slot becomes available. Scanning and hashing retain their existing order; dry runs report the same stage order.

Events remain ordered within each task, while events from different tasks may interleave and completion order may differ from plan order. User code in `OnEvent` and `OnError` is serialized but can run on different goroutines. Callbacks must still avoid filesystem mutation.

After receiving an unignored error, dispatch stops and active tasks are awaited. Confirmed successes participate in the final state save, and active-task errors are returned together. A failed copy stage prevents the final deletion stages. Cancel the caller context to interrupt active operations; cancellation saves follow the separate save-deadline rules described above. No tasks remain running after sync returns.

## Hash Comparison

`cloudfs.Hasher` provides `Hashes() []crypto.Hash` and `Hash(ctx, path, algorithm)`. `Hashes` lists the driver's supported algorithms in preference order. Sync selects the first algorithm supported by both sides and available for streamed verification, falling back to SHA-256 when there is no shared algorithm. Drivers return a lowercase hexadecimal content digest. An empty string or `cloudfs.ErrNotSupport` falls back to reading the file; other errors are returned. `PrefixFS` forwards both methods.

Sync compares size and modification time by default. Set `Checksum: true` to compare file contents by hash. Initial copies establish both hash baselines from the transfer stream; planning reads hashes only when a content comparison is needed. Later plans reuse a historical hash only when that side's size and modification time match, the size is known, and the timestamp is nonzero. Other files needed by those planning decisions obtain the selected digest from the driver or by reading their contents. Timestamp-only changes with identical content refresh the local baseline without copying or changing the receiver's time. Hashing emits no copy progress.

Each side reads a path at most once for planning; transfer reads are separate. Destination-only files preserved or deleted by one-way sync, skipped type-conflict subtrees, and paths not reached after an abort are not hashed. Once copying is determined, task generation does not pre-read the source. New files, selected type replacements, and subtree copies use the transfer stream for their hash; a hash already obtained during comparison is still validated during copying. Successful copies record the transfer hash in both source and target baselines, independently of the comparison options. Reusing a saved hash trusts size and modification time, so content changes preserving both can escape detection. `Checksum` can be switched between runs. Older state without hashes is read again; newly discovered content differences follow normal change or conflict rules rather than being ignored because old size and time match.

Filtering runs before hash reads. Dry runs perform needed reads but do not save refreshed state. Hash reads return both read and stream-close errors.

## Dry Runs and Deletion Limits

Both `Option` accept `DryRun`, `MaxDelete`, and `MaxDeletePercent`:

```go
option := syncer.Option{
    DryRun:           true,
    Delete:           true,
    MaxDelete:        100,
    MaxDeletePercent: 20,
    OnConflict: syncer.PreferSrc(),
    OnEvent: func(notification syncer.Event) {
        if event, ok := notification.(syncer.SyncEvent); ok {
            log.Printf("%s %s %s", event.Status, event.Op, event.FileInfo.Path())
        }
    },
}
```

A dry run loads state, scans, and resolves conflicts normally. `OnEvent` receives one `StatusPlanned` per planned operation. It performs no writes or state saves and emits no copy progress or successful task events. Phase events are still emitted. Hash comparison performs the same reads as a real run, and callbacks still run; callers must avoid write side effects in callbacks. Bidirectional dry runs still require `StateStore`; one-way dry runs load state when a store is provided. No executable plan is retained: a real run scans again.

`MaxDelete` limits the combined number of explicitly removed entries across both sides. Each file and directory counts once per side; recursive removals include all scanned descendants. Type replacements count even when one-way `Delete` is false. Ordinary file overwrites do not count as deletions.

`MaxDeletePercent` is checked separately for each receiving side against that side's current scanned entry count, excluding the root. Both limits must pass when enabled. Equality is allowed; only exceeding a limit fails. Both default to 0 (disabled). Counts cannot be negative; percentages must be in 0–100.

Exceeding a limit returns `ErrTooManyDeletes`, detectable with `errors.Is`, before any writes or state saves. Dry runs enforce the same limits. Filtered paths and skipped conflicts produce no deletion tasks and add nothing to the deletion count.

## Source Consistency

Before each copy attempt, the source's type, size, and modification time must match its scanned snapshot. After a successful read and stream closure, these checks run again. When a planning hash exists, the copy validates its hash using the transfer stream, without another content read. Detected changes return `ErrSourceChanged`, detectable with `errors.Is`, and do not advance that file's saved state. A pre-copy mismatch prevents this copy attempt from opening a target writer; earlier tasks, such as type replacement removals, may already have changed the target. During-copy changes may already have affected the target: this is not atomic replacement.

During copying, `OnError` may skip the changed file with `ErrorSkip` or stop by returning the error. Retries retain the original snapshot and reject a still-changed source; a later sync scans again. Hash retrieval failures stop planning before writes and bypass `OnError`. Metadata checks add two source `Stat` calls per fully read copy attempt. Planning hash reads add no `Stat` calls. They add no content reads. Changes preserving size and time can escape detection when comparing metadata or reusing saved hashes. Callers must still exclude concurrent writers.

## One-way synchronization

```go
err := syncer.Sync(ctx, src, dst)
err = syncer.Sync(ctx, src, dst, syncer.Option{
    Delete: true, // Default: preserve destination-only entries.
    OnConflict: syncer.PreferSrc(), // Explicitly overwrite differing target files.

    StateStore: store,
    MaxRetry: 2,
    OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
        return syncer.ErrorRetry, nil
    },
})
```

`Sync` traverses each root once with `cloudfs.WalkDir`, completes comparison
before writing, and queries only necessary paths afterward. Both roots must
exist. Empty directories are included. Differing existing files and file/directory mismatches use `OnConflict`
with `Abort()` as the default. It reports conflicts as
`ErrSyncConflict` before writes. Set `OnConflict: syncer.PreferSrc()` to overwrite target files.
`PreferSrc()` removes the conflicting target subtree and recreates it from
source, even when `Delete` is false. Directory removal calls `FS.Remove` once
for the directory; descendants do not generate separate operations or events. `PreferDst()` and `Skip()` preserve
both sides of that subtree and its previous metadata; `Sync` never writes back to
source. `OnConflict` receives current and previous snapshots and selects the policy for each conflict. A callback error stops comparison before writes. The caller owns the filesystems; the library closes file streams.
Copying checks both stream `Close()` errors before confirming the target with `Stat`.
Close failures enter `OnError` and retries restart the whole copy; unconfirmed
transfers do not advance metadata. Transfer and close errors are preserved together
for `errors.Is`. Driver, callback, and storage errors retain their original text. Use callback
file information and task events for path, operation, and direction.

Comparison defaults to size and modification time. With `Checksum: true`, planning reuses or obtains the selected digest as needed after both metadata scans. Hashes compare source and destination files; copying also checks the transferred hash against the source hash computed during planning when available. Copying always records the actual transfer hash in both baselines without rereading either side. When both entries have a hash, equality compares hashes; if either hash is
missing, it compares size and modification time. `Checksum` does not constrain
stored metadata or require hashes to exist.
Permissions and timestamps are not copied. Symbolic links and special files are
rejected. The caller must supply filesystem metadata compatible with its content.

Every `Sync` call is a fresh synchronization: it scans both filesystems and
compares their current entries. A non-nil `StateStore` automatically enables loading and saving confirmed source/destination metadata for later comparisons, including when upload timestamps differ across backends. With a nil store, Sync keeps no persistent state:

```go
type StateStore interface {
    Load(context.Context) (*syncer.State, error)
    Save(context.Context, *syncer.State) error
}
```

For local JSON storage, pass a `FileStore` in the sync options.

```go
option := syncer.Option{
    Mode:       syncer.ModeTwoWay,
    StateStore: syncer.NewFileStore("sync-state.json", "photos-source", "photos-target"),
}
```

Use stable identifiers for the source and target roots in that order. Place the
state file outside both sync roots, in an existing directory.

`State.Source` and `State.Target` are `map[string]*syncer.Record`, keyed by
relative paths. `Record` contains `IsDir`, `Size`, `ModTime`, and `Hash`.
`State.HashAlgorithm` is the required `crypto.Hash` used by this baseline; subsequent syncs retain it.
`State.Version` remains the integer protocol version, currently `1`; other
protocol versions are rejected. `State.Revision` is a nonempty string identifying
the saved content. New saves use the SHA-256 of the JSON-encoded `HashAlgorithm`, `Version`,
`Source`, and `Target`, excluding `Revision` itself. Map insertion order does not
affect the result. Loading also accepts other nonempty revision identifiers, such
as a caller-provided UUID. Unchanged metadata keeps its revision and is not saved
again.

`State.UpdatedAt` is the UTC time when changed metadata was prepared for saving.
It is excluded from the revision hash and file comparisons. Unchanged calls do
not update it. Partial successes may also be saved, so this is not the last
successful synchronization time. Older records may omit the timestamp.

Use `Record.Valid(state.HashAlgorithm)` and `State.Valid()` to check caller-constructed metadata.
`Record.Equal(other)` follows the synchronization comparison rules; directory
comparison checks type only, not children. `State.UpdateRevision()` computes
a content revision without changing `UpdatedAt`; call it before sharing metadata
with a store. These methods perform no filesystem I/O.

`StateStore` receives `*syncer.State` directly. An in-memory store can retain
the value; persistent stores own serialization, for example using `encoding/json`.
Treat metadata as immutable and bind each store to one fixed pair of supplied
filesystems. Return nil for a first run. `Save` must replace metadata atomically,
leaving an intact old or new value if it fails.

Confirmed copies, renames, and removals advance their saved metadata. If a later file fails,
the finish phase still attempts to save confirmed successes, including after cancellation. Failed,
ignored, and unprocessed files retain their previous metadata. If the caller context is already cancelled when saving begins, the save uses a separate five-second deadline and retains caller context values; stores must honor that context. The sync still returns cancellation, joined with any save error for `errors.Is`. Normal saves retain the caller context; cancellation during an ongoing save may fail that save, without an automatic retry.

State is still saved only at the end, rather than after each file. Forced termination, process crashes, or save failures may leave only the previous saved metadata. The next call
always scans and compares again. There are no operation IDs, execution stages,
progress logs, or byte-level resume. A copy without confirmed target metadata
may be copied again on the next call. Retries reuse the initial source snapshot and check source metadata again before and after copying. Successful copies record the actual transfer hash; when the snapshot has a hash, the transfer must match it. The caller keeps source data stable during
a synchronization; a later `Sync` scans the current files afresh.

`OnError` receives the synchronization context and a `FileError`, returning an
`ErrorPolicy` and an error. `FileError` includes `FileInfo`, `Op`, `Reverse`,
`NewPath`, `Attempt`, and `Err`. Use `FileInfo.Path()` for the path; `Attempt`
starts at 1. `Error()` preserves the original error text, and `Unwrap()` returns
the underlying error for `errors.Is` / `errors.As`.

```go
option := syncer.Option{
    MaxRetry: 2,
    OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
        return syncer.ErrorRetry, nil
    },
}
```

`ErrorPolicy` includes `ErrorAbort`, `ErrorSkip`, and `ErrorRetry`. `ErrorAbort`
stops with the operation error; `ErrorSkip` skips the operation; `ErrorRetry`
repeats it with the same synchronization context and expected snapshot. A nil
`OnError` aborts. A non-nil callback error stops synchronization with that error,
regardless of policy.

The handler is called after each failure. `MaxRetry` limits retries per operation:
Values less than or equal to 0 default to 3; the initial attempt is not a retry.
Returning `ErrorRetry` after the limit is reached stops with the last operation error.
The example allows at most two retries per operation. The handler may abort or skip
earlier and controls delays. Copies restart from the beginning.
Skipping directory creation skips its descendants. Skipping a rename or removal
required for type replacement blocks its dependent operations.
Source checks during copying participate in `OnError` and retries. Scanning, checksum comparison, target metadata confirmation, state persistence, and cancellation errors do not invoke `OnError`. State is saved after execution and saving errors are returned.
Failure can leave a partial destination file. Concurrent writers must be excluded by the caller.
Synchronization runs immediately; no delayed execution plan is exposed.

Both synchronization modes report phase, task, and copy progress events through `OnEvent`:

```go
option := syncer.Option{
    OnEvent: func(notification syncer.Event) {
        switch event := notification.(type) {
        case syncer.PhaseEvent:
            log.Printf("phase=%s status=%s err=%v", event.Phase, event.Status, event.Err)
        case syncer.SyncEvent:
            log.Printf("path=%s op=%s status=%s reverse=%t new_path=%s err=%v",
                event.FileInfo.Path(), event.Op, event.Status,
                event.Reverse, event.NewPath, event.Err)
        case syncer.ProgressEvent:
            log.Printf("%s: %d/%d bytes",
                event.FileInfo.Path(), event.Written, event.FileInfo.Size())
        }
    },
}
```

All events implement `String()` and can be logged directly:

```go
OnEvent: func(event syncer.Event) {
    log.Print(event)
},
```

`PhaseEvent` reports the synchronization lifecycle through `Phase`, `Status`, and `Err`.

| Phase | Activity |
| --- | --- |
| `PhaseScan` | Load prior state, scan both sides, and apply path filters |
| `PhasePlan` | Compare entries and generate tasks |
| `PhaseExecute` | Execute tasks, or report planned tasks in a dry run |
| `PhaseFinish` | Reconcile and save confirmed state; omitted in dry runs |

Each phase emits `StatusStarted`, followed by `StatusSucceeded` or `StatusFailed` with `Err`. Scanning or planning failures stop the run. Execution failures still proceed to finalization. Phase events are also emitted when there are no file tasks.

`Status` includes `StatusStarted`, `StatusSucceeded`, `StatusFailed`, `StatusSkipped`, and `StatusPlanned`.

`Op` includes `OpCopy`, `OpRename`, `OpMkdir`, `OpRemove`, and `OpRmdir`.

`SyncEvent.FileInfo` describes the copied source, directory to create, or object being renamed or removed. `Reverse` is false for writes to the original target and true for writes to the original source. `NewPath` gives the destination path for renames. Paths are absolute within the supplied filesystems. Snapshots can be retained and their accessors perform no filesystem I/O.

An attempted task reports `StatusStarted` followed by `StatusSucceeded`, `StatusFailed`, or `StatusSkipped` after retries. Tasks blocked by ignored prerequisites report only skipped. Failed events carry `Err`; skipped operation errors are available to `OnError`.

Callbacks run synchronously and must not modify the filesystems. Serial execution follows task order; with parallel copies, different tasks may interleave while each task retains its event order.
Tasks blocked by an ignored prerequisite report only `StatusSkipped`. Unchanged
files, conflicts skipped during comparison, and remaining tasks after termination
produce no events. Scanning and state persistence are outside task notifications;
a successful task does not guarantee the final metadata save succeeds. Log each
retry attempt inside `OnError` if needed.

## Bidirectional synchronization

```go
err := syncer.Sync(ctx, src, dst, syncer.Option{
    Mode: syncer.ModeTwoWay,
    StateStore: store, // Implements syncer.StateStore, as shown above.
    OnConflict: syncer.Abort(),
})
```

`ModeTwoWay` requires a caller-provided `StateStore` for a fixed, ordered filesystem
pair. It uses the same typed `State` and `Record` as `Sync`, with separate
metadata for each side. Each call has three stages: scan metadata, plan, then execute. Planning checks exclusions while traversing paths, computes or reuses hashes as configured, compares entries, resolves conflicts, generates tasks, and checks deletion limits. Both roots are scanned once; the complete plan succeeds before writing. There is no public delayed plan or default state file; all
filenames are available for application data.

The first call merges distinct paths. Later calls propagate one-sided changes
and deletions using each side's previous metadata, so different upload timestamps
do not cause repeated transfers. `Checksum` compares file contents by hash during
planning, including when checking whether a surviving subtree changed after deletion on the other side. Comparisons use hashes when both entries have them and otherwise use
size and modification time. Changing `Checksum` does not invalidate metadata.

Incompatible edits, deletion versus modification, and file/directory mismatches
use the conflict policy:

- `Abort()` (default): return `ErrSyncConflict` before any writes.
- `Skip()`: preserve the path and its previous metadata and continue.
- `PreferSrc()`: select the source version, including source deletions.
- `PreferDst()`: select the target version, including target deletions.
- `KeepBoth()`: preserve conflicting versions under separate paths (ModeTwoWay only).

Directory decisions apply to the entire subtree. Independent paths still
synchronize in both directions. A resolver can override the policy per conflict:

```go
option := syncer.Option{
    Mode: syncer.ModeTwoWay,
    StateStore: store,
    OnConflict: func(ctx context.Context, conflict syncer.Conflict) (syncer.ConflictPolicy, error) {
        // Source/Target are current FileInfo; SourceState/TargetState
        // are previous FileInfo. Nil means absent; use a non-nil FileInfo.Path().
        return chooseVersion(ctx, conflict)
    },
}
```

Resolvers run during comparison, receive read-only entries, and must not modify
the filesystems. Resolver errors stop synchronization before writes.

`OnError` follows the single-direction retry and ignore contract above and retries
only the failing file operation. Ignoring a failed directory creation skips its
descendants. Ignoring a removal blocks the remaining replacement of that subtree.
Confirmed operations are offered to state storage even if a later operation fails or is cancelled, following the save-context rules above. Failed, ignored, and unprocessed operations retain their previous
metadata; confirmed removals update only the sides actually removed.

The next call always scans and compares again. An incomplete copy or a write
whose metadata could not be confirmed may require a conflict policy on that call.
There is no progress replay or byte-level resume. State persistence errors are
returned; stores must atomically retain an intact old or new value. Keep each
store exclusive to its filesystem pair, exclude concurrent writers, and keep the
source stable during each call. The caller owns the supplied filesystems.


[Documentation home](./_index.md) · [简体中文](../zh/syncer.md) · [Internal design (Chinese)](../../design/syncer.md)

## Keeping Both Versions

Set `OnConflict` to `KeepBoth()` for bidirectional synchronization; a custom handler can also return `ConflictKeepBoth`:

```go
option := syncer.Option{
    Mode: syncer.ModeTwoWay,
    StateStore:     store,
    OnConflict: syncer.KeepBoth(),
}
```

- File/file: keep the source version at the original path and preserve the target version under a conflict-copy path on both sides.
- File/directory: keep the directory at the original path and preserve the file under a conflict-copy path on both sides.
- Deletion/modification: restore the surviving version, including a directory subtree.

Copies stay in the same parent directory: `report.txt` becomes `report.conflict.txt`, then `report.conflict-2.txt`, and so on if occupied. Names are checked against both scans, historical state, and names allocated in this run. `Skipper` also checks proposed copy paths; a filtered copy returns `ErrSyncConflict` before any writes.

The preserved version is renamed to a sibling backup first. The backup and original path are then synchronized in the copy stage and may copy concurrently. A failed or ignored rename blocks later operations for that conflict. If the backup copy fails, its local backup remains and the original path may already have synchronized; ignoring that copy error does not block the original copy. Completed operations are recorded when state is saved at the end. The driver must support `Rename`; otherwise it returns `ErrNotSupport`. Copies may leave partial files on failure; the sequence does not provide transactional rollback.

Dry runs list rename and copy operations. `SyncEvent` and `ProgressEvent` expose `FileInfo` for the current task object. Backup copies describe the renamed file at the backup path. `NewPath` gives the absolute destination for renames. `Reverse` selects the receiving side. `OpRename` emits no progress events. Cross-side copies report progress and support operation retries. Renames do not count toward deletion limits.

This policy is supported only by `ModeTwoWay`. One-way `Sync` rejects this decision when a conflict is found, before writes, and never writes back to its source.

## Copy Progress

Both synchronization options report `ProgressEvent` through `OnEvent`:

```go
OnEvent: func(notification syncer.Event) {
    progress, ok := notification.(syncer.ProgressEvent)
    if !ok {
        return
    }
    info := progress.FileInfo
    log.Printf("%s: %d/%d bytes",
        info.Path(), progress.Written, info.Size())
},
```

`FileInfo` describes the sending file at the current task path before execution. `Reverse` indicates a write to the original source. `Written` counts bytes accepted by the receiving stream in this attempt; the sending entry's `Size()` is the total, negative if unknown. After both streams open, callbacks start at 0 and increase after writes. Retries reset the count to 0. Empty files report 0; renames, unchanged files, and skipped files do not report progress. Hashing during planning is excluded.

Callbacks run synchronously and return no error. Do not modify filesystems; cancel the caller context to stop copying. Reaching the total does not confirm stream closure, target validation, or state persistence; use task events and the returned error for completion. Progress adds no scans or content reads and is not persisted.
