# CloudFS Documentation

[English](../en/_index.md) | [简体中文](../zh/_index.md)
CloudFS is a Go filesystem abstraction library that exposes local and remote storage backends through a single `cloudfs.FS` interface. Callers can list directories, read files, create files, move, copy, rename, and remove entries with a consistent API.

## Documentation Map

- [Middleware Guide](./middleware.md)
- [Driver Guide](./drivers.md)
- [Synchronization Guide](./syncer.md)
- [Project README](../../../README.md)

- [Command-Line Guide](./cli.md)

## Highlights

- Unified `cloudfs.FS` interface
- Direct driver construction and dynamic driver construction by name
- Middleware-based composition on top of existing drivers
- Support for prefix mapping, caching, rate limiting, compression, encryption, and custom path/file mapping

## Install

Requires Go **1.25.8 or newer**.

```bash
go get github.com/honmaple/cloudfs
```

Import only the driver you need:

```go
import "github.com/honmaple/cloudfs/driver/webdav"
```

If you want to create drivers dynamically through `driver.New()`, import the desired driver package for registration, or the aggregate registration package:

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
}
```

## Core Interface

```go
type FS interface {
	List(context.Context, string) ([]FileInfo, error)
	Move(context.Context, string, string) error
	Copy(context.Context, string, string) error
	Rename(context.Context, string, string) error
	Remove(context.Context, string) error
	MakeDir(context.Context, string) error
	Stat(context.Context, string) (FileInfo, error)
	Open(context.Context, string) (File, error)
	Create(context.Context, string) (FileWriter, error)
	Close() error
}
```

Notes:

- All paths are Unix-style paths such as `/`, `/dir`, and `/dir/file.txt`
- `Open()` returns a readable and seekable file object
- `Create()` returns an `io.WriteCloser` and must be closed explicitly to finish uploads or writes
- `FileInfo` extends `io/fs.FileInfo` with `Path()`, `Type()`, and `ExtraInfo()`

## Creating Drivers

### Direct Construction

```go
fs, err := webdav.New(&webdav.Option{
	Endpoint: "https://example.com/dav",
	Username: "user",
	Password: "pass",
})
```

### Dynamic Construction

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

### From a JSON String

```go
fs, err := driver.NewFromString("local", `{"path":"/tmp/cloudfs"}`)
```

### Validation Helpers

```go
ok := driver.Exists("s3")
err := driver.VerifyOption("s3", `{"endpoint":"https://s3.example.com","bucket":"files"}`)
```

Notes:

- `driver.New()` and `driver.NewFromString()` depend on per-driver `init()` registration
- Validation errors currently come back in Chinese

## Middleware Composition

You can layer middleware through `cloudfs.New()` or `middleware.NewFS()`:

```go
import (
	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver/local"
	"github.com/honmaple/cloudfs/middleware"
)

base, err := local.New(&local.Option{Path: "/data"})
if err != nil {
	panic(err)
}

fs, err := cloudfs.New(
	base,
	middleware.PrefixFS("/project-a"),
	middleware.CacheFS(&middleware.CacheOption{ExpireTime: 120}),
	middleware.RateLimitFS(&middleware.RateLimitOption{
		Wait:  true,
		Burst: 20,
		Limit: 1,
	}),
)
```

See [middleware.md](./middleware.md) for details.

## Common Operations

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

## Driver Capability Matrix

| Driver | Name | List | Mkdir | Rename | Move | Copy | Remove | Upload | Download |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Local | `local` | yes | yes | yes | yes | yes | yes | yes | yes |
| FTP | `ftp` | yes | yes | yes | yes | yes | yes | yes | yes |
| SFTP | `sftp` | yes | yes | yes | yes | yes | yes | yes | yes |
| S3 | `s3` | yes | yes | yes | yes | yes | yes | yes | yes |
| SMB | `smb` | yes | yes | yes | yes | yes | yes | yes | yes |
| WebDAV | `webdav` | yes | yes | yes | yes | yes | yes | yes | yes |
| Foxel | `foxel` | yes | yes | yes | yes | yes | yes | yes | yes |
| OpenList | `openlist` | yes | yes | yes | yes | yes | yes | yes | yes |
| UpYun | `upyun` | yes | yes | yes | yes | yes | yes | yes | yes |
| Google Drive | `gdrive` | yes | yes | yes | yes | yes | yes | yes | yes |
| OneDrive | `onedrive` | yes | yes | yes | yes | yes | yes | yes | yes |
| 115 Drive | `pan115` | yes | yes | yes | yes | yes | yes | no | yes |
| Quark Drive | `quark` | yes | yes | yes | yes | no | yes | no | yes |
| GitHub Repository | `github` | yes | no | no | no | no | no | no | yes |
| GitHub Release | `github-release` | yes | no | no | no | no | no | no | yes |
| Mirror | `mirror` | yes | no | no | no | no | no | no | yes |

See [drivers.md](./drivers.md) for complete parameter and behavior details.

One-way and bidirectional synchronization support conflict policies, metadata recording, current-file retries, and task events. See the [synchronization guide](./syncer.md).
