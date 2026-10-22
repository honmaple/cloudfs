# Middleware Guide

CloudFS middleware wraps an existing `cloudfs.FS` through `cloudfs.WrapFunc`. It lets you add path remapping, caching, rate limiting, compression, encryption, and custom transformations without changing the underlying driver.

## Usage

```go
base, err := local.New(&local.Option{Path: "/data"})
if err != nil {
	panic(err)
}

fs, err := middleware.NewFS(
	base,
	middleware.PrefixFS("/team-a"),
	middleware.CacheFS(&middleware.CacheOption{ExpireTime: 60}),
)
```

`middleware.NewFS` is effectively an alias of `cloudfs.New`.

## Middleware List

### CacheFS

Caches directory listing results to reduce repeated `List()` calls.

```go
fs, err := middleware.NewFS(
	base,
	middleware.CacheFS(&middleware.CacheOption{ExpireTime: 60}),
)
```

Parameters:

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `expire_time` | `time.Duration` | `60` | Cache lifetime in seconds |

Behavior:

- `List()` caches directory entries by path
- `Stat()` first checks the parent directory cache
- `Create()`, `Rename()`, `Move()`, `Copy()`, `MakeDir()`, and `Remove()` invalidate related cache entries
- When `ExpireTime <= 0`, the code falls back to `60`

Notes:

- Only directory metadata is cached, not file content
- Set `ExpireTime` as a number of seconds: use `60` for one minute, not `time.Minute`

### PaginationFS

Slices the backend listing in memory; it does not reduce backend listing work.

```go
fs, err := middleware.NewFS(base, middleware.PaginationFS(&middleware.PaginationOption{}))
files, err := fs.List(ctx, "/photos?page=2&page_size=100")
```

`PageKey` (`page_key`) defaults to `page`; `PageSizeKey` (`page_size_key`) defaults to `page_size`. Pages start at 1; nonpositive pages become 1. A nonpositive page size delegates unchanged. When enabled, pagination removes these two query parameters before delegating and preserves other parameters. Out-of-range pages return an empty slice.

### RateLimitFS

Limits operation frequency. Useful for remote APIs with strict quotas.

```go
fs, err := middleware.NewFS(
	base,
	middleware.RateLimitFS(&middleware.RateLimitOption{
		Wait:  true,
		Burst: 10,
		Limit: 1,
	}),
)
```

Parameters:

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `wait` | `bool` | `false` | Whether to block until a token is available |
| `burst` | `int` | `100` | Bucket size |
| `limit` | `time.Duration` | `1` | Time interval per token in seconds |

Behavior:

- Intercepts `List`, `Copy`, `Move`, `Rename`, `Remove`, `MakeDir`, `Stat`, `Open`, and `Create`
- With `Wait=false`, it returns the error `访问频率限制` when no token is available
- With `Wait=true`, it blocks until a token is available or the context is canceled

Notes:

- The defaults roughly mean one token interval per second with a burst of 100
- `Limit` is not QPS; it is the refill interval

### CompressFS

Applies gzip compression on writes and gzip decompression on reads.

```go
fs, err := middleware.NewFS(
	base,
	middleware.CompressFS(&middleware.CompressOption{
		Level: gzip.BestCompression,
	}),
)
```

Parameters:

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `level` | `int` | `gzip.BestCompression` | gzip compression level |

Behavior:

- `Create()` compresses data before sending it to the underlying driver
- `Open()` transparently decompresses the stored data

Notes:

- The backend stores compressed binary content and does not append `.gz`
- Best suited for data written and read through the same middleware stack

### EncryptFS

Provides transparent encryption for file content and path names.

```go
fs, err := middleware.NewFS(
	base,
	middleware.EncryptFS(&middleware.EncryptOption{
		Password:     "secret",
		PasswordSalt: "secret-salt",
		Mode:         "CFB",
		DirName:      true,
		FileName:     true,
		Version:      "v2",
	}),
)
```

Parameters:

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `mode` | `string` | `CFB` | Encryption mode; `CTR`, `OFB`, or fallback to `CFB` |
| `dir_name` | `bool` | `false` | Encrypt directory names |
| `file_name` | `bool` | `false` | Encrypt file names |
| `suffix` | `string` | `""` | Optional suffix when file names are not encrypted |
| `password` | `string` | none | Required encryption password |
| `password_salt` | `string` | same as `password` | Used when generating the IV |
| `version` | `string` | `v1` | `v2` uses a different encryption format; match the version used for existing data |

Behavior:

- File content is encrypted and decrypted through streaming AES
- Directory names and file names can be controlled independently
- `suffix` is useful when you do not want to encrypt file names but still want to disguise extensions
- `Stat()` may try both directory and file forms because the path type is unknown ahead of time

Notes:

- `password` is mandatory
- Use `v1` or `v2`; read existing data with the same version used to write it
- Changing `Password`, `PasswordSalt`, `Mode`, or `Version` can make old data unreadable

### PredicateFS

Provides fully custom path and file metadata transformations for advanced scenarios.

```go
fs, err := middleware.NewFS(
	base,
	middleware.PredicateFS(&middleware.PredicateOption{
		PathFn: func(path string) string {
			return "/tenant-a" + path
		},
		FileFn: func(file cloudfs.FileInfo) (cloudfs.FileInfo, bool) {
			return file, true
		},
	}),
)
```

Parameters:

| Field | Type | Description |
| --- | --- | --- |
| `PathFn` | `func(string) string` | Maps an external path to the backend path |
| `FileFn` | `func(cloudfs.FileInfo) (cloudfs.FileInfo, bool)` | Transforms file metadata; `bool=false` filters the entry out |

Behavior:

- `PathFn` is applied to `List`, `Stat`, `Open`, `Create`, `Copy`, `Move`, `Rename`, `Remove`, and `MakeDir`
- `FileFn` transforms `List` and `Stat`; its boolean filters only `List` entries (`Stat` ignores it)
- This is a code-level extension point and is not suitable for JSON-based dynamic construction

### PrefixFS

Adds a fixed prefix to backend paths. This is a common convenience wrapper around `PredicateFS`.

```go
fs, err := middleware.NewFS(base, middleware.PrefixFS("/project-a"))
```

Effect:

- External `/docs/a.txt` becomes backend `/project-a/docs/a.txt`
- Exposed file paths automatically strip the prefix again

### TrimPrefixFS

Strips a fixed prefix from external paths before delegating to the backend.

```go
fs, err := middleware.NewFS(base, middleware.TrimPrefixFS(base, "/mnt"))
```

Effect:

- External path `/mnt/docs/a.txt`
- Backend path `/docs/a.txt`

Notes:

- The function signature is `TrimPrefixFS(fs cloudfs.FS, prefix string)`, but it still returns a `cloudfs.WrapFunc`
- Use it when the destination requires a fixed prefix to be removed

## Composition Order

Wrappers are applied in argument order; the last one is closest to the caller. Choose the order from the actual read/write flow. To compress before encrypting on writes, place encryption inside compression:

```go
fs, err := cloudfs.New(base,
	middleware.EncryptFS(&middleware.EncryptOption{Password: "secret"}),
	middleware.CompressFS(&middleware.CompressOption{}),
)
```

Writes flow from caller to compression, encryption, then backend; reads decrypt before decompressing. Placing cache inside pagination allows reuse of the full listing. Pagination slices results rather than requesting remote pages.

## Known Limitations

- Go programs compose middleware with `cloudfs.New`; the command uses each driver’s `middlewares` configuration. See the [command-line guide](cli.md)
- `PredicateOption` contains functions and cannot be built directly from JSON
- Set `ExpireTime` and `Limit` in seconds, even though their Go field type is `time.Duration`
