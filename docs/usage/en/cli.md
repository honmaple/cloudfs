# Command-Line Guide

The CloudFS command supports file operations and synchronization across local and cloud storage.

```sh
go install github.com/honmaple/cloudfs/cmd/cloudfs@latest
# Build from source
go build -o cloudfs ./cmd/cloudfs
cloudfs --help
cloudfs help cp
```

## Configuration Discovery

Without `--config` / `-c`, the command searches the current directory in order: `config.json`, `config.yaml`, `config.yml`, then `config.toml`. It uses the first existing file without merging files. If none exists, local paths still work; named drivers require configuration.

An explicit configuration takes precedence. An unreadable or invalid selected configuration returns an error before file operations; the command does not fall back to another file.

```sh
# Automatically read config.yaml when config.json is absent
cloudfs ls name1:
# Explicitly select another configuration
cloudfs -c ./production.toml ls name1:/
```

## Paths and Basic Operations

`name:/path` addresses a configured driver. `name:` is shorthand for the driver root `name:/`. Other paths are native local paths, including absolute paths, relative paths, and `..`. Relative paths use the working directory, and `ls` defaults to that directory. Global `--config` (alias `-c`) can appear before the command or after its paths. Local operations need no configuration.

```sh
cloudfs ls
cloudfs mkdir ./docs
cloudfs cp ./hello.txt ./docs/hello.txt
cloudfs stat ./docs/hello.txt
cloudfs cat ./docs/hello.txt
cloudfs rename ./docs/hello.txt renamed.txt
cloudfs mv ./docs/renamed.txt ./saved.txt
cloudfs rm ./saved.txt
cloudfs rm -r ./docs
```

`cp/mv` always take an exact destination path. `rename` takes a new name only. `ls` prints type, size, modification time, and name; `stat` prints JSON. `cat` writes raw bytes to stdout and supports shell redirection. Directory removal requires `-r` / `--recursive`; filesystem root removal is rejected.

```sh
cloudfs -c config.yaml cp ./hello.txt name1:/hello.txt
cloudfs -c config.yaml cp name1:/hello.txt ./download.txt
cloudfs -c config.yaml cat name1:/hello.txt > ./output.txt
cloudfs -c config.yaml sync --conflict src ./documents name2:/backup
```

Use `cp` for uploads and downloads; `put/get` and `--root` are removed. Transfers may overwrite files; parent directories must exist. Failures may leave partial files. Errors go to stderr with exit status 1; Ctrl+C cancels operations. Stream and filesystem close errors are returned. Restrict credential file permissions, for example `chmod 600 config.yaml`.

## Named Drivers and Cross-Driver Operations

Configuration file extensions `.json`, `.yaml`, `.yml`, and `.toml` are supported. Driver names are top-level keys, without an extra wrapper, and are case-insensitive. Equivalent YAML and TOML examples:

```yaml
name1:
  driver: local
  option:
    path: /data
name2:
  driver: webdav
  option:
    endpoint: https://example.com/dav
    username: user
    password: replace-me
```

```toml
[name1]
driver = "local"
[name1.option]
path = "/data"

[name2]
driver = "webdav"
[name2.option]
endpoint = "https://example.com/dav"
username = "user"
password = "replace-me"
```

```sh
cloudfs --config config.yaml cp name1:/root/a.txt name2:/share/b.txt
cloudfs --config config.toml ls name2:/share
```


```json
{
  "name1": {
    "driver": "local",
    "option": {
      "path": "/data"
    }
  },
  "name2": {
    "driver": "webdav",
    "option": {
      "endpoint": "https://example.com/dav",
      "username": "user",
      "password": "replace-me"
    }
  }
}
```

```sh
cloudfs --config drivers.json ls name1:/root
cloudfs --config drivers.json cp name1:/root/a.txt name2:/share/b.txt
cloudfs --config drivers.json cp name1:/photos name2:/archive/photos
cloudfs --config drivers.json mv name1:/root/a.txt name2:/share/a.txt
```

`cp/mv` use the exact destination path. Directory contents merge into the destination directory without deleting target-only files. File destination parents must exist. Both commands accept named drivers. Copies stream through the client without buffering whole files or invoking server-side copy. `mv` removes the source only after successful copying and stream closure; removal failure leaves both copies. Moves are not atomic. Equal or overlapping paths are rejected; callers must still avoid aliases that refer to overlapping storage.

Configuration uses top-level driver names only. Unqualified paths always refer to native local paths, even with configuration. `ls/cat/stat/mkdir/rename/rm` also accept `name:/path`.

## Synchronization Commands

`sync` uses `ModeOneWay`; `bisync` uses `ModeTwoWay` and requires `--state`.

Sync commands print events to stdout. File paths in those events are relative to the sync roots. Each phase result includes its own duration as `elapsed`. Finalization prints only its result.

```sh
cloudfs --config drivers.json sync --conflict src name1:/documents name2:/backup
cloudfs sync name1:/documents name2:/backup --dry-run --state ./sync-state.json --config drivers.json
cloudfs --config drivers.json sync --delete --state ./sync-state.json name1:/documents name2:/backup
cloudfs --config drivers.json bisync --state ./bisync-state.json name1:/documents name2:/backup
```

`sync` synchronizes directory contents, preserving target-only files unless `--delete` is set. Optional `--state` records metadata. `bisync` requires `--state` and propagates changes and deletions in both directions. Both directories must exist. Subcommand flags can appear before or after paths.

- `--concurrency N`: maximum parallel copies and final file removals (default 1; 0 is also serial). Values above 1 require concurrency-safe drivers and middleware. Renames, type-replacement preparation, and directory creation run first; copies precede final file and directory removals. Active tasks finish before an error is returned.
- `--dry-run`: print phase events and file operations with `status=planned` without changing files or state.
- `--max-delete N`: limit combined removed entries, including recursive directory contents and type replacements. 0 is unlimited.
- `--max-delete-percent N`: limit removals separately for each receiving side (0–100; 0 disables). Exceeding either deletion limit stops the run before writes, including dry runs. Ordinary file overwrites do not count as deletions.
- `--checksum`: compare file contents by hash. Reuses saved hashes when size and modification time match.
- `--conflict abort|skip|src|dst|newer|both`: default `abort`; differing files or types conflict. `src/dst` explicitly prefer one side; `skip` preserves conflicts.
- `--conflict newer`: compare known modification times for two regular files, choosing source on ties; deletion conflicts restore the survivor. A directory or unknown time when both entries exist aborts the run. One-way sync never writes back.
- `--conflict both`: supported only by `bisync`. File conflicts keep the source at the original path and preserve the target in `.conflict` copies on both sides. Type conflicts keep the directory and preserve the file; deletion conflicts restore the survivor. Dry runs show actual copy destinations.
- `--state FILE`: local JSON state file. The parent directory must exist. Updates replace the file atomically with mode `0600`.

For repeated syncs across backends, use the same `--state` file from the first run to record each side's modification times.

State binds the ordered endpoints and a digest of configuration, without storing raw credentials. Changing names, options (including credentials), or directories requires a separate state file. Keep state outside synchronized directories and exclude concurrent commands for the same filesystem pair or state file. The file wraps metadata in a command-specific envelope rather than storing bare `syncer.State`. See the [synchronization guide](syncer.md) for conflicts and partial success behavior.

With state enabled, cancellation still attempts to save confirmed results. A save that begins after cancellation receives a separate five-second deadline. Cancellation and save errors are both returned; forced termination can still lose this run's state.

## Per-Driver Middleware

Each driver accepts an optional ordered `middlewares` list. Each entry has a `type` and an optional `option` object; omitting options uses defaults. Invalid options are rejected before constructing a driver. JSON, YAML, and TOML support the same structure:

```yaml
backup:
  driver: local
  option:
    path: /data
  middlewares:
    - type: prefix
      option:
        prefix: /archive
    - type: encrypt
      option:
        password: replace-me
        version: v2
    - type: compress
      option:
        level: 9
    - type: cache
      option:
        expire_time: 60
```

`backup:/file.txt` maps to `/data/archive/file.txt`. Wrappers apply in list order; the last is closest to the caller. This example compresses then encrypts writes and reverses that process on reads. The backend prefix directory should already exist.

| type | option fields | Behavior |
| --- | --- | --- |
| `prefix` | `prefix` | Must start with `/`; maps into a directory |
| `trim_prefix` | `prefix` | Must start with `/`; removes an incoming path prefix |
| `cache` | `expire_time` | Seconds; defaults to 60 |
| `pagination` | `page_key`, `page_size_key` | Defaults to `page`, `page_size`; slices a full listing using query parameters |
| `rate_limit` | `wait`, `burst`, `limit` | Defaults: false, 100, 1; `limit` is an integer number of seconds, not a duration string |
| `compress` | `level` | gzip level -2 through 9; 0 selects the default best compression |
| `encrypt` | `password`, `password_salt`, `mode`, `version`, `dir_name`, `file_name`, `suffix` | Password required; see middleware guide for other options |

`PredicateFS` requires Go callbacks and cannot be configured here. Unknown types, option fields, and invalid value types are rejected. Middleware configuration participates in synchronization state binding. Synchronization requires reported size to match readable content; content middleware does not automatically adjust backend sizes, so verify that contract before using such stacks for sync. See the [middleware guide](middleware.md).
