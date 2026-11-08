# CloudFS

[English](README.md) · [简体中文](README.zh-CN.md)

CloudFS is a Go library for working with local and remote storage through one `cloudfs.FS` interface.

- Use the same file operations with local directories, S3, SFTP, WebDAV, SMB, and other storage backends.
- Add path mapping, caching, rate limiting, compression, or encryption with middleware.
- Copy between backends or synchronize directories in copy, mirror, or bidirectional mode.

## Quick start

Requires Go 1.25.8 or newer.

```sh
go get github.com/honmaple/cloudfs
```

```go
package main

import (
	"context"
	"fmt"

	"github.com/honmaple/cloudfs/driver/s3"
)

func main() {
	fs, err := s3.New(&s3.Option{
		Endpoint:       "https://s3.example.com",
		Bucket:         "my-bucket",
		Region:         "us-east-1",
		AccessKey:      "your-access-key",
		SecretKey:      "your-secret-key",
		ListVersion:    "v2",
		ForcePathStyle: true,
	})
	if err != nil {
		panic(err)
	}
	defer fs.Close()

	files, err := fs.List(context.Background(), "/")
	if err != nil {
		panic(err)
	}
	for _, file := range files {
		fmt.Println(file.Name())
	}
}
```

## Command line

```sh
go install github.com/honmaple/cloudfs/cmd/cloudfs@latest
```

Create `config.yaml`.

```yaml
s3:
  driver: s3
  option:
    endpoint: https://s3.example.com
    bucket: my-bucket
    region: us-east-1
    access_key: "your-access-key"
    secret_key: "your-secret-key"
    list_version: v2
    force_path_style: true
```

List the bucket root.

```sh
cloudfs --config config.yaml ls s3:
```

## Supported drivers

| Driver | Registered name | List | Mkdir | Rename | Move | Copy | Remove | Upload | Download |
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

## Documentation

[Go library](docs/usage/en/_index.md) · [Drivers](docs/usage/en/drivers.md) · [Middleware](docs/usage/en/middleware.md) · [Synchronization](docs/usage/en/syncer.md) · [Command line](docs/usage/en/cli.md)
