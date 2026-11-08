# CloudFS

[English](README.md) · [简体中文](README.zh-CN.md)

CloudFS 是一个 Go 库，通过统一的 `cloudfs.FS` 接口访问本地和远程存储。

- 使用相同的文件操作访问本地目录、S3、SFTP、WebDAV、SMB 等存储。
- 通过中间件添加路径映射、缓存、限速、压缩或加密。
- 支持跨存储复制，以及目录的单向复制、镜像同步和双向同步。

## 快速开始
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

## 命令行

```sh
go install github.com/honmaple/cloudfs/cmd/cloudfs@latest
```

新增配置文件 `config.yaml`

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

列出存储桶中的 `share` 目录。

```sh
cloudfs --config config.yaml ls s3:/share
```

## 支持的驱动

| 驱动 | 注册名 | List | Mkdir | Rename | Move | Copy | Remove | Upload | Download |
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
| 115 网盘 | `pan115` | yes | yes | yes | yes | yes | yes | no | yes |
| 夸克网盘 | `quark` | yes | yes | yes | yes | no | yes | no | yes |
| GitHub Repository | `github` | yes | no | no | no | no | no | no | yes |
| GitHub Release | `github-release` | yes | no | no | no | no | no | no | yes |
| Mirror | `mirror` | yes | no | no | no | no | no | no | yes |

## 使用文档

[Go 库入门](docs/usage/zh/_index.md) · [驱动](docs/usage/zh/drivers.md) · [中间件](docs/usage/zh/middleware.md) · [同步](docs/usage/zh/syncer.md) · [命令行](docs/usage/zh/cli.md)
