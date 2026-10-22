# CloudFS

[English](README.md) | [简体中文](README.zh-CN.md)

CloudFS is a Go library that exposes local disks and multiple cloud storage
providers through one filesystem-like interface.

It provides:

- A common `cloudfs.FS` interface for listing, reading, writing, moving, copying,
renaming, and deleting files.
- Driver packages for Local, FTP, SFTP, S3, SMB, WebDAV, Google Drive, OneDrive,
GitHub, and other storage providers.
- Middleware wrappers for prefix mapping, caching, rate limiting, compression,
encryption, and custom hooks.
- One-way and bidirectional synchronization with conflict handling and optional
  metadata recording (required for bidirectional synchronization).

## Install

Requires Go **1.25.8 or newer** (see `go.mod`).

```bash
go get github.com/honmaple/cloudfs
```

Import only the driver you need:

```go
import "github.com/honmaple/cloudfs/driver/webdav"
```

Or import all built-in drivers for dynamic creation through `driver.New`:

```go
import _ "github.com/honmaple/cloudfs/driver/all"
```

## Quick Start

```go
package main

import (
	"context"
	"io"
	"strings"

	"github.com/honmaple/cloudfs/driver/local"
)

func main() {
	fs, err := local.New(&local.Option{
		Path: "/tmp/cloudfs",
	})
	if err != nil {
		panic(err)
	}
	defer fs.Close()

	ctx := context.Background()

	if err := fs.MakeDir(ctx, "/docs"); err != nil {
		panic(err)
	}

	w, err := fs.Create(ctx, "/docs/hello.txt")
	if err != nil {
		panic(err)
	}
	if _, err := io.Copy(w, strings.NewReader("hello cloudfs")); err != nil {
		_ = w.Close()
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}

	files, err := fs.List(ctx, "/docs")
	if err != nil {
		panic(err)
	}
	for _, file := range files {
		println(file.Path(), file.Name(), file.Size())
	}

	r, err := fs.Open(ctx, "/docs/hello.txt")
	if err != nil {
		panic(err)
	}
	defer r.Close()
}
```

## Create a Driver

You can create a driver directly through its package:

```go
fs, err := webdav.New(&webdav.Option{
	Endpoint: "https://example.com/dav",
	Username: "user",
	Password: "pass",
})
```

You can also create drivers dynamically. Import `driver` for the factory and
blank-import the desired driver package, or `driver/all` to register all built-in drivers:

```go
import (
	"github.com/honmaple/cloudfs/driver"
	_ "github.com/honmaple/cloudfs/driver/all"
)

fs, err := driver.New("webdav", map[string]any{
	"endpoint": "https://example.com/dav",
	"username": "user",
	"password": "pass",
})
```

Use `driver.NewFromString` when the configuration is already JSON:

```go
fs, err := driver.NewFromString("local", `{"path":"/tmp/cloudfs"}`)
```

Useful helpers:

```go
ok := driver.Exists("s3")
err := driver.VerifyOption("s3", `{"endpoint":"https://s3.example.com","bucket":"files"}`)
```

## Common Operations

All paths use slash-separated absolute-style paths such as `/`, `/dir`, and
`/dir/file.txt`. `List` options are passed as query parameters on the path,
for example `/dir?page=2&page_size=50` with `PaginationFS`. Supported query
parameters depend on the driver or middleware; pagination is not automatic.

```go
ctx := context.Background()

files, err := fs.List(ctx, "/")
info, err := fs.Stat(ctx, "/file.txt")
err = fs.MakeDir(ctx, "/new-dir")
err = fs.Rename(ctx, "/file.txt", "new-name.txt")
err = fs.Move(ctx, "/new-name.txt", "/new-dir")
err = fs.Copy(ctx, "/new-dir/new-name.txt", "/")
err = fs.Remove(ctx, "/new-dir/new-name.txt")
```

`List` and `Stat` return `cloudfs.FileInfo`, which follows `io/fs.FileInfo`
and adds `Path()`, `Type()`, and `ExtraInfo()` for driver-specific metadata.
Use `cloudfs.Entry` when you need to build a `cloudfs.FileInfo` manually.

Reading supports `io.Reader`, `io.Seeker`, and `io.Closer`:

```go
r, err := fs.Open(ctx, "/file.txt")
if err != nil {
	return err
}
defer r.Close()

_, _ = r.Seek(10, io.SeekStart)
```

Writing returns an `io.WriteCloser`. Always close it to finish the upload:

```go
w, err := fs.Create(ctx, "/file.txt")
if err != nil {
	return err
}
_, err = io.Copy(w, source)
if closeErr := w.Close(); err == nil {
	err = closeErr
}
```

## Drivers

| Driver         | Name             | List | Mkdir | Rename | Move | Copy | Remove | Upload | Download |
|----------------|------------------|------|-------|--------|------|------|--------|--------|----------|
| Local          | `local`          | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| FTP            | `ftp`            | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| SFTP           | `sftp`           | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| S3             | `s3`             | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| SMB            | `smb`            | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| WebDAV         | `webdav`         | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| Foxel          | `foxel`          | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| Openlist       | `openlist`       | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| Upyun          | `upyun`          | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| Google Drive   | `gdrive`         | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| OneDrive       | `onedrive`       | yes  | yes   | yes    | yes  | yes  | yes    | yes    | yes      |
| 115            | `pan115`         | yes  | yes   | yes    | yes  | yes  | yes    | no     | yes      |
| Quark          | `quark`          | yes  | yes   | yes    | yes  | no   | yes    | no     | yes      |
| GitHub         | `github`         | yes  | no    | no     | no   | no   | no     | no     | yes      |
| Mirror         | `mirror`         | yes  | no    | no     | no   | no   | no     | no     | yes      |
| GitHub Release | `github-release` | yes  | no    | no     | no   | no   | no     | no     | yes      |

## Driver Options

Common examples:

```go
// Local
fs, err := driver.New("local", map[string]any{
	"path": "/data/files",
})

// WebDAV
fs, err = driver.New("webdav", map[string]any{
	"endpoint": "https://example.com/dav",
	"username": "user",
	"password": "pass",
})

// S3-compatible storage
fs, err = driver.New("s3", map[string]any{
	"endpoint": "https://s3.example.com",
	"bucket": "files",
	"region": "us-east-1",
	"access_key": "key",
	"secret_key": "secret",
	"force_path_style": true,
})
```

Google Drive supports credentials JSON, a credentials file, an access token, or
Application Default Credentials from the Google client library:

```go
fs, err := driver.New("gdrive", map[string]any{
	"credentials_file": "/path/to/service-account.json",
	"root_id": "root",
	"export_mime_type": "application/pdf",
})
```

Useful Google Drive options:

- `credentials`: service account or OAuth credentials JSON
- `credentials_file`: path to credentials JSON
- `access_token`: OAuth access token
- `root_id`: root folder ID, defaults to `root`
- `shared_drive_id`: shared drive ID
- `export_mime_type`: export MIME type for Google Workspace files
- `supports_all_drives`: enable all-drives calls

OneDrive uses Microsoft Graph and requires an OAuth access token:

```go
fs, err := driver.New("onedrive", map[string]any{
	"access_token": "token",
})
```

Useful OneDrive options:

- `access_token`: Microsoft Graph OAuth access token
- `endpoint`: Graph endpoint, defaults to `https://graph.microsoft.com/v1.0`
- `drive_id`: target a specific drive
- `user_id`: target a user's default drive
- `root_id`: root item ID, defaults to `root`
- `copy_timeout`: maximum time to wait for async copy operations

## Middlewares

Middlewares wrap an existing `cloudfs.FS`. They are applied in the order passed
to `cloudfs.New`.

```go
wrapped, err := cloudfs.New(
	fs,
	middleware.PrefixFS("/tenant-a"),
	middleware.CacheFS(&middleware.CacheOption{}),
)
```

### PrefixFS

Maps all user-visible paths under a backend prefix.

```go
fs, err = cloudfs.New(fs, middleware.PrefixFS("/storage/root"))
```

### CacheFS

Caches directory listings and invalidates affected parent directories on writes.
`ExpireTime` is in seconds; default is 60.

```go
fs, err = cloudfs.New(fs, middleware.CacheFS(&middleware.CacheOption{
	ExpireTime: 60,
}))
```

### PaginationFS

Paginates `List` results with query parameters. `page` is 1-based, and
pagination is enabled only when `page_size` is greater than 0.

```go
fs, err = cloudfs.New(fs, middleware.PaginationFS(&middleware.PaginationOption{}))

files, err := fs.List(ctx, "/photos?page=2&page_size=100")
```

### RateLimitFS

Limits operation frequency. Set `Limit` to a number of seconds: use `1` for
one second, not `time.Second`.

```go
fs, err = cloudfs.New(fs, middleware.RateLimitFS(&middleware.RateLimitOption{
	Wait: true,
	Burst: 30,
	Limit: 1,
}))
```

### CompressFS

Compresses content on write and decompresses it on read. File names are not
changed by this middleware.

```go
fs, err = cloudfs.New(fs, middleware.CompressFS(&middleware.CompressOption{}))
```

### EncryptFS

Encrypts file content and can optionally encrypt directory and file names.

```go
fs, err = cloudfs.New(fs, middleware.EncryptFS(&middleware.EncryptOption{
	Password: "secret",
	DirName: false,
	FileName: true,
}))
```

### PredicateFS

Use `PredicateFS` when you need custom path rewriting or want to filter or
rewrite listed file metadata.

```go
fs, err = cloudfs.New(fs, middleware.PredicateFS(&middleware.PredicateOption{
	PathFn: func(path string) string {
		return "/backend" + path
	},
	FileFn: func(file cloudfs.FileInfo) (cloudfs.FileInfo, bool) {
		return file, true
	},
}))
```

## Add a New Driver

A driver implements `cloudfs.FS`, usually embeds `cloudfs.BaseFS`, validates its
`Option`, and registers itself in `init`.

```go
type Option struct {
	Token string `json:"token" validate:"required"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

func init() {
	driver.Register("example", func() driver.Option {
		return &Option{}
	})
}
```

## Synchronization

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

### Custom Filtering

`Option.Skipper` accept `syncer.Skipper`, a `func(info syncer.FileInfo) bool`. Return true to exclude a path:

```go
option := syncer.Option{
	Skipper: func(info syncer.FileInfo) bool {
		return info.Path() == "/cache" || strings.HasSuffix(info.Path(), ".tmp")
	},
}
```

Import `strings` for this example. The callback runs synchronously during each scan, before reading file contents for checksums; `Hash()` is empty for these scan snapshots. The root `/` is not passed to the callback. Paths are absolute within the supplied FS. Excluded directories are not traversed and their descendants are not passed to the callback. Historical entries are not passed to the callback. Paths excluded by the current scans, including descendants hidden by an excluded directory, retain their previous baseline. Historical paths confirmed absent from both scans are removed unless covered by an exclusion. Do not mutate filesystems or metadata in the callback.

A match on either side excludes that path and descendants on both sides, preserves previous metadata, and emits neither conflict callbacks nor task events. An ancestor deletion or type replacement that would affect an excluded child is skipped for the whole subtree. New directories can still be created with their included children. Nil disables filtering.

Excluded files are not opened for `Checksum`, and excluded directories are not listed. Planning checks both scan exclusion markers as it traverses each path; a match on either side prunes that path or subtree and prevents hashing on both sides. Listing the parent is still necessary to discover entries; errors encountered before filtering are returned. After changing filters, the next call scans again and compares against retained history.

### Built-in Conflict Handlers

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

### Concurrent Copies

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

### Hashing on Change

The default still compares size and modification time without content reads. `Option.HashOnChange` explicitly enable state-based hash reuse:

```go
option := syncer.Option{
    HashOnChange: true,

    StateStore:   store,
}
```

One-way sync requires `StateStore` for hash reuse; bidirectional sync uses its required store. Initial copies establish both hash baselines from the transfer stream; planning reads hashes only when a content comparison is needed. Later plans reuse a historical hash only when that side's size and modification time match, the size is known, and the timestamp is nonzero. Other files needed by those planning decisions are read to compute SHA-256. Timestamp-only changes with identical content refresh the local baseline without copying or changing the receiver's time. Hashing emits no copy progress.

`Checksum: true` takes precedence and computes fresh hashes on demand during planning, without reusing historical hashes. Each side reads a path at most once for planning; transfer reads are separate. Destination-only files preserved or deleted by one-way sync, skipped type-conflict subtrees, and paths not reached after an abort are not hashed. Once copying is determined, task generation does not pre-read the source. New files, selected type replacements, and subtree copies use the transfer stream for their hash; a hash already obtained during comparison is still validated during copying. Successful copies record the transfer hash in both source and target baselines, independently of the comparison options. Hashing on change trusts size and time, so content changes preserving both can escape detection; use `Checksum` for a full check. Neither option changes the state format, and both can be switched between runs. Older state without hashes is read again; newly discovered content differences follow normal change or conflict rules rather than being ignored because old size and time match.

Filtering runs before hash reads. Dry runs perform needed reads but do not save refreshed state. Hash reads use the same stream-close, byte-count, and source-snapshot checks as full checksum reads.

### Dry Runs and Deletion Limits

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

A dry run loads state, scans, and resolves conflicts normally. `OnEvent` receives one `StatusPlanned` per planned operation. It performs no writes or state saves and emits no copy progress or successful task events. Phase events are still emitted. `Checksum` and `HashOnChange` still perform their required reads, and callbacks still run; callers must avoid write side effects in callbacks. Bidirectional dry runs still require `StateStore`; one-way dry runs load state when a store is provided. No executable plan is retained: a real run scans again.

`MaxDelete` limits the combined number of explicitly removed entries across both sides. Each file and directory counts once per side; recursive removals include all scanned descendants. Type replacements count even when one-way `Delete` is false. Ordinary file overwrites do not count as deletions.

`MaxDeletePercent` is checked separately for each receiving side against that side's current scanned entry count, excluding the root. Both limits must pass when enabled. Equality is allowed; only exceeding a limit fails. Both default to 0 (disabled). Counts cannot be negative; percentages must be in 0–100.

Exceeding a limit returns `ErrTooManyDeletes`, detectable with `errors.Is`, before any writes or state saves. Dry runs enforce the same limits. Filtered paths and skipped conflicts produce no deletion tasks and add nothing to the deletion count.

### Source Consistency

Before each copy attempt, the source's type, size, and modification time must match its scanned snapshot. After a successful read and stream closure, these checks run again. Hashing during planning also checks metadata and byte count after reading; when a planning hash exists, the copy validates its hash using the transfer stream, without another content read. Detected changes return `ErrSourceChanged`, detectable with `errors.Is`, and do not advance that file's saved state. A pre-copy mismatch prevents this copy attempt from opening a target writer; earlier tasks, such as type replacement removals, may already have changed the target. During-copy changes may already have affected the target: this is not atomic replacement.

During copying, `OnError` may skip the changed file with `ErrorSkip` or stop by returning the error. Retries retain the original snapshot and reject a still-changed source; a later sync scans again. Changes detected while hashing stop planning before writes and bypass `OnError`. Metadata checks add two source `Stat` calls per fully read copy attempt and one per successful planning hash read. They add no content reads. With `Checksum` disabled, changes preserving size and time can escape detection. Callers must still exclude concurrent writers.

### One-way synchronization

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

Comparison uses size and modification time by default and does not open data
files. `HashOnChange` explicitly enables state-based hash reuse as described above. `Checksum: true` computes SHA-256 as the planning traversal compares file contents, after both metadata scans. Hashes compare source and destination files; copying also checks the transferred hash against the source hash computed during planning when available. Copying always records the actual transfer hash in both baselines without rereading either side. When both entries have a hash, equality compares hashes; if either hash is
missing, it compares size and modification time. `Checksum` does not constrain
stored metadata or require hashes to exist. State comparison cannot detect
same-size changes with unchanged times.
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

`State.Source` and `State.Target` are `map[string]*syncer.Entry`, keyed by
relative paths. `Entry` contains `IsDir`, `Size`, `ModTime`, and `Hash`.
`State.Version` remains the integer protocol version, currently `1`; other
protocol versions are rejected. `State.Revision` is a nonempty string identifying
the saved content. New saves use the SHA-256 of the JSON-encoded `Type`, `Version`,
`Source`, and `Target`, excluding `Revision` itself. Map insertion order does not
affect the result. Loading also accepts other nonempty revision identifiers, such
as a caller-provided UUID. Unchanged metadata keeps its revision and is not saved
again.

`State.UpdatedAt` is the UTC time when changed metadata was prepared for saving.
It is excluded from the revision hash and file comparisons. Unchanged calls do
not update it. Partial successes may also be saved, so this is not the last
successful synchronization time. Older records may omit the timestamp.

Use `Entry.Valid()` and `State.Valid()` to check caller-constructed metadata.
`Entry.Equal(other)` follows the synchronization comparison rules; directory
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
| `PhaseScan` | Scan both sides and apply path filters |
| `PhasePlan` | Load prior state, compare entries, and generate tasks |
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

### Bidirectional synchronization

```go
err := syncer.Sync(ctx, src, dst, syncer.Option{
    Mode: syncer.ModeTwoWay,
    StateStore: store, // Implements syncer.StateStore, as shown above.
    OnConflict: syncer.Abort(),
})
```

`ModeTwoWay` requires a caller-provided `StateStore` for a fixed, ordered filesystem
pair. It uses the same typed `State` and `Entry` as `Sync`, with separate
metadata for each side. Each call has three stages: scan metadata, plan, then execute. Planning checks exclusions while traversing paths, computes or reuses hashes as configured, compares entries, resolves conflicts, generates tasks, and checks deletion limits. Both roots are scanned once; the complete plan succeeds before writing. There is no public delayed plan or default state file; all
filenames are available for application data.

The first call merges distinct paths. Later calls propagate one-sided changes
and deletions using each side's previous metadata, so different upload timestamps
do not cause repeated transfers. `Checksum` explicitly reads file contents on demand during
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

### Keeping Both Versions

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

## Development
Run from the repository root:

```bash
go build ./...
go test ./...
go vet ./...
```

CloudFS provides a reusable library and an optional `cmd/cloudfs` command. `example/main.go` requires valid provider
configuration before running.

## Copy Between Filesystems

`cloudfs.CopyBetween(ctx, srcFS, srcPath, dstFS, dstPath)` streams a file or directory between two `cloudfs.FS` values. The destination is an exact path; the source name is not appended.

```go
err := cloudfs.CopyBetween(ctx, srcFS, "/docs/a.txt", dstFS, "/backup/b.txt")
```

Directory contents merge into the destination, preserving empty directories and target-only files. File destination parents must exist; existing files may be overwritten. Symbolic links and special files are rejected. Callers must exclude overlapping source/destination storage and close the filesystems. Copying closes its own streams and returns transfer or close errors; failures may leave partial destination content.

## Command Line

```sh
go install github.com/honmaple/cloudfs/cmd/cloudfs@latest
cloudfs ls /data
cloudfs --config ./drivers.yaml cp ./hello.txt webdav:/hello.txt
```

Supports `ls/stat/cat/mkdir/cp/mv/rename/rm/sync/bisync`. See the [command-line guide](docs/usage/en/cli.md) for configuration and examples.

Configuration supports named drivers addressed as `name:/path`, for example `cloudfs --config drivers.json cp name1:/root/a.txt name2:/share/b.txt`. `bisync` requires a local `--state` file.

Command configuration supports JSON / YAML / TOML, with driver names as top-level keys.

Command paths use `name:/path` for named drivers; other paths are native local absolute or relative paths. Uploads and downloads both use `cp`.

Each named driver also accepts an ordered `middlewares` list for prefix mapping, caching, pagination, rate limiting, compression, and encryption. See the [command-line guide](docs/usage/en/cli.md).

Without `--config`, the command reads the first existing `config.json`, `config.yaml`, `config.yml`, or `config.toml` in the current directory, in that order. Local operations work when none exists.

### Copy Progress

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
