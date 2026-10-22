# CloudFS 中文文档

[English](../en/_index.md) | [简体中文](../zh/_index.md)
CloudFS 是一个 Go 文件系统抽象库，用统一的 `cloudfs.FS` 接口封装本地磁盘和多种云存储/远程存储。调用方可以用一致的方式完成目录浏览、文件读取、文件写入、移动、复制、重命名和删除。

## 文档导航

- [中间件文档](./middleware.md)
- [驱动文档](./drivers.md)
- [同步使用指南](./syncer.md)
- [项目 README](../../../README.zh-CN.md)

- [命令行使用](./cli.md)

## 功能概览

- 统一的 `cloudfs.FS` 接口
- 支持直接创建驱动，也支持按名称动态创建驱动
- 支持通过中间件包装已有驱动
- 支持按路径前缀挂载、缓存、限速、压缩、加密、自定义路径与文件映射

## 安装

需要 Go **1.25.8 或更新版本**。

```bash
go get github.com/honmaple/cloudfs
```

按需引入驱动：

```go
import "github.com/honmaple/cloudfs/driver/webdav"
```

如果你希望通过 `driver.New()` 动态按名字创建驱动，需要导入对应驱动包以执行注册，也可以导入聚合注册包：

```go
import _ "github.com/honmaple/cloudfs/driver/all"
```

## 快速开始

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

## 核心接口

CloudFS 的核心接口如下：

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

说明：

- 所有路径都使用以 `/` 开头的类 Unix 风格路径
- `Open()` 返回可读、可 `Seek()` 的文件对象
- `Create()` 返回 `io.WriteCloser`，必须显式 `Close()` 才能完成上传/落盘
- `FileInfo` 在标准 `io/fs.FileInfo` 基础上增加了 `Path()`、`Type()` 和 `ExtraInfo()`

## 创建驱动

### 方式一：直接创建

```go
fs, err := webdav.New(&webdav.Option{
	Endpoint: "https://example.com/dav",
	Username: "user",
	Password: "pass",
})
```

### 方式二：动态创建

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

### 方式三：从 JSON 字符串创建

```go
fs, err := driver.NewFromString("local", `{"path":"/tmp/cloudfs"}`)
```

### 配置校验

```go
ok := driver.Exists("s3")
err := driver.VerifyOption("s3", `{"endpoint":"https://s3.example.com","bucket":"files"}`)
```

注意：

- `driver.New()` 和 `driver.NewFromString()` 依赖每个驱动在 `init()` 中完成注册
- 校验错误消息目前使用中文返回

## 中间件包装

你可以通过 `cloudfs.New()` 或 `middleware.NewFS()` 叠加多个中间件：

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

更完整的中间件说明见 [middleware.md](./middleware.md)。

## 常用操作

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

## 驱动能力矩阵

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

完整参数说明和驱动注意事项见 [drivers.md](./drivers.md)。

单向与双向同步支持冲突策略、元数据记录、当前文件重试和任务事件，详见 [同步使用指南](./syncer.md)。
