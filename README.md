# CloudFS

CloudFS is a Go library that exposes local disks and multiple cloud storage
providers through one filesystem-like interface.

It provides:

- A common `cloudfs.FS` interface for listing, reading, writing, moving, copying,
  renaming, and deleting files.
- Driver packages for Local, FTP, SFTP, S3, SMB, WebDAV, Google Drive, OneDrive,
  GitHub, and other storage providers.
- Middleware wrappers for prefix mapping, caching, rate limiting, compression,
  encryption, and custom hooks.

## Install

```bash
go get github.com/honmaple/cloudfs
```

Import only the driver you need:

```go
import "github.com/honmaple/cloudfs/driver/webdav"
```

Or import all built-in drivers for dynamic creation through `cloudfs.New`:

```go
import _ "github.com/honmaple/cloudfs/driver"
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

You can also create drivers dynamically. Import `driver` once for side-effect
registration:

```go
import (
	"github.com/honmaple/cloudfs"
	_ "github.com/honmaple/cloudfs/driver"
)

fs, err := cloudfs.New("webdav", map[string]any{
	"endpoint": "https://example.com/dav",
	"username": "user",
	"password": "pass",
})
```

Use `cloudfs.NewWithString` when the configuration is already JSON:

```go
fs, err := cloudfs.NewWithString("local", `{"path":"/tmp/cloudfs"}`)
```

Useful helpers:

```go
ok := cloudfs.Exists("s3")
err := cloudfs.Verify("s3", `{"endpoint":"https://s3.example.com","bucket":"files"}`)
```

## Common Operations

All paths use slash-separated absolute-style paths such as `/`, `/dir`, and
`/dir/file.txt`.

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
| GitHub Release | `github-release` | yes  | no    | no     | no   | no   | no     | no     | yes      |

## Driver Options

Common examples:

```go
// Local
fs, err := cloudfs.New("local", map[string]any{
	"path": "/data/files",
})

// WebDAV
fs, err = cloudfs.New("webdav", map[string]any{
	"endpoint": "https://example.com/dav",
	"username": "user",
	"password": "pass",
})

// S3-compatible storage
fs, err = cloudfs.New("s3", map[string]any{
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
fs, err := cloudfs.New("gdrive", map[string]any{
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
fs, err := cloudfs.New("onedrive", map[string]any{
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
to `middleware.NewFS`.

```go
wrapped, err := middleware.NewFS(
	fs,
	middleware.PrefixFS("/tenant-a"),
	middleware.CacheFS(&middleware.CacheOption{}),
)
```

### PrefixFS

Maps all user-visible paths under a backend prefix.

```go
fs, err = middleware.NewFS(fs, middleware.PrefixFS("/storage/root"))
```

### CacheFS

Caches directory listings and invalidates affected parent directories on writes.
`ExpireTime` is in seconds; default is 60.

```go
fs, err = middleware.NewFS(fs, middleware.CacheFS(&middleware.CacheOption{
	ExpireTime: 60,
}))
```

### RateLimitFS

Limits operation frequency.

```go
fs, err = middleware.NewFS(fs, middleware.RateLimitFS(&middleware.RateLimitOption{
	Wait: true,
	Burst: 30,
	Limit: time.Second,
}))
```

### CompressFS

Compresses content on write and decompresses it on read. File names are not
changed by this middleware.

```go
fs, err = middleware.NewFS(fs, middleware.CompressFS(&middleware.CompressOption{}))
```

### EncryptFS

Encrypts file content and can optionally encrypt directory and file names.

```go
fs, err = middleware.NewFS(fs, middleware.EncryptFS(&middleware.EncryptOption{
	Password: "secret",
	DirName: false,
	FileName: true,
}))
```

### HookFS

Use `HookFS` when you need custom path or file metadata rewriting.

```go
fs, err = middleware.NewFS(fs, middleware.HookFS(&middleware.HookOption{
	PathFn: func(path string) string {
		return "/backend" + path
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
	cloudfs.Register("example", func() cloudfs.Option {
		return &Option{}
	})
}
```
