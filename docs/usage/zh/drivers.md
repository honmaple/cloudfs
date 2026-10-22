# 驱动文档

本文档汇总 CloudFS 内置驱动的注册名、用途、参数、示例和已知限制。

## 总览

| 驱动 | 注册名 | 场景 |
| --- | --- | --- |
| Local | `local` | 本地文件系统 |
| FTP | `ftp` | 传统 FTP 服务器 |
| SFTP | `sftp` | 基于 SSH 的文件传输 |
| S3 | `s3` | AWS S3 或兼容对象存储 |
| SMB | `smb` | Windows 共享目录/NAS |
| WebDAV | `webdav` | WebDAV 服务 |
| Foxel | `foxel` | Foxel 文件服务 |
| OpenList | `openlist` | OpenList/Alist 兼容接口 |
| UpYun | `upyun` | 又拍云存储 |
| Google Drive | `gdrive` | Google Drive/Shared Drive |
| OneDrive | `onedrive` | Microsoft OneDrive |
| 115 网盘 | `pan115` | 115 网盘只读下载为主 |
| 夸克网盘 | `quark` | 夸克网盘只读下载为主 |
| GitHub | `github` | GitHub 仓库文件浏览 |
| GitHub Release | `github-release` | GitHub Releases 资产浏览 |
| Mirror | `mirror` | HTTP 目录镜像站 |

## Local

注册名：`local`

用途：访问本机目录。

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `path` | `string` | 是 | 本地根目录，必须是绝对路径，且需要已经规范化 |

示例：

```go
fs, err := driver.New("local", map[string]any{
	"path": "/data/files",
})
```

注意事项：

- `path` 必须是绝对路径
- `filepath.Clean(path)` 之后必须和原始值一致，否则会返回 `fs.ErrInvalid`
- Windows 下会自动处理斜杠转换

## FTP

注册名：`ftp`

参数：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | `string` | 是 | 无 | FTP 主机 |
| `port` | `int` | 否 | `21` | 端口 |
| `username` | `string` | 是 | 无 | 用户名 |
| `password` | `string` | 是 | 无 | 密码 |

示例：

```go
fs, err := driver.New("ftp", map[string]any{
	"host": "127.0.0.1",
	"username": "demo",
	"password": "secret",
})
```

注意事项：

- 建立连接时使用 10 秒超时
- `Copy()` 走库内通用复制逻辑，本质是“下载后再上传”

## SFTP

注册名：`sftp`

参数：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | `string` | 是 | 无 | SFTP 主机 |
| `port` | `int` | 否 | `22` | 端口 |
| `username` | `string` | 是 | 无 | 用户名 |
| `password` | `string` | 条件必填 | 无 | 未提供 `private_key` 时必填 |
| `private_key` | `string` | 条件必填 | 无 | 未提供 `password` 时必填，内容是私钥文本 |

示例：

```go
fs, err := driver.New("sftp", map[string]any{
	"host": "sftp.example.com",
	"username": "demo",
	"private_key": privateKeyPEM,
})
```

注意事项：

- HostKey 校验当前使用 `ssh.InsecureIgnoreHostKey()`
- `private_key` 需要是可被 `ssh.ParsePrivateKey()` 解析的 PEM 内容

## S3

注册名：`s3`

参数：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `endpoint` | `string` | 是 | 无 | S3 或兼容对象存储地址 |
| `bucket` | `string` | 是 | 无 | Bucket 名称 |
| `region` | `string` | 否 | 空 | 区域 |
| `access_key` | `string` | 否 | 空 | Access Key |
| `secret_key` | `string` | 条件必填 | 空 | 配置了 `access_key` 时必须同时提供 |
| `session_token` | `string` | 否 | 空 | 临时会话 Token |
| `list_version` | `string` | 否 | 空 | `v1` 或 `v2`，控制对象列表 API 版本 |
| `force_path_style` | `bool` | 否 | `false` | 是否强制路径风格访问 |

示例：

```go
fs, err := driver.New("s3", map[string]any{
	"endpoint": "https://s3.example.com",
	"bucket": "files",
	"region": "us-east-1",
	"access_key": "key",
	"secret_key": "secret",
	"force_path_style": true,
	"list_version": "v2",
})
```

注意事项：

- 目录是通过以 `/` 结尾的空对象模拟的
- `Copy()` 文件使用服务端复制，目录使用递归复制
- `Stat()` 会先按文件查询，再退化为目录前缀判断

## SMB

注册名：`smb`

参数：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `host` | `string` | 是 | 无 | SMB 主机 |
| `port` | `int` | 否 | `445` | 端口 |
| `username` | `string` | 是 | 无 | 用户名 |
| `password` | `string` | 是 | 无 | 密码 |
| `domain` | `string` | 否 | 空 | 域名 |
| `share_name` | `string` | 是 | 无 | 共享名 |

示例：

```go
fs, err := driver.New("smb", map[string]any{
	"host": "192.168.1.10",
	"username": "demo",
	"password": "secret",
	"share_name": "public",
})
```

注意事项：

- 路径统一以 `/` 开头，例如 `/docs/file.txt`
- `MakeDir()` 使用权限 `0700`

## WebDAV

注册名：`webdav`

参数：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `endpoint` | `string` | 是 | 无 | WebDAV 端点 |
| `username` | `string` | 是 | 无 | 用户名 |
| `password` | `string` | 是 | 无 | 密码 |
| `dir_perm` | `os.FileMode` | 否 | `0755` | 创建目录/上传时使用的权限 |

示例：

```go
fs, err := driver.New("webdav", map[string]any{
	"endpoint": "https://example.com/dav",
	"username": "demo",
	"password": "secret",
})
```

注意事项：

- 创建目录和文件时权限固定为 `0755`，`dir_perm` 不改变该值
- `Stat()` 会把 WebDAV 的 403/404 映射到 `os.ErrPermission` / `os.ErrNotExist`

## Foxel

注册名：`foxel`

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `endpoint` | `string` | 是 | Foxel 服务地址 |
| `username` | `string` | 是 | 用户名 |
| `password` | `string` | 是 | 密码 |

示例：

```go
fs, err := driver.New("foxel", map[string]any{
	"endpoint": "https://foxel.example.com",
	"username": "demo",
	"password": "secret",
})
```

注意事项：

- 驱动启动时会自动登录并缓存 Bearer Token
- 请求遇到 401 时会自动重登一次
- 目录列表按每页 500 项获取

## OpenList

注册名：`openlist`

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `endpoint` | `string` | 是 | OpenList 服务地址 |
| `username` | `string` | 是 | 用户名 |
| `password` | `string` | 是 | 密码 |

示例：

```go
fs, err := driver.New("openlist", map[string]any{
	"endpoint": "https://openlist.example.com",
	"username": "demo",
	"password": "secret",
})
```

`List()` 支持的附加参数：

| 参数名 | 类型 | 说明 |
| --- | --- | --- |
| `password` | `string` | 目录密码，传给 `/api/fs/list` |

示例：

```go
files, err := fs.List(ctx, cloudfs.PathWithValues("/secret", map[string]any{
	"password": "dir-pass",
}))
```

注意事项：

- 服务端须支持 OpenList/Alist 接口
- 构造时会立即登录

## UpYun

注册名：`upyun`

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `bucket` | `string` | 是 | 服务空间名 |
| `operator` | `string` | 是 | 操作员 |
| `password` | `string` | 是 | 操作员密码 |

示例：

```go
fs, err := driver.New("upyun", map[string]any{
	"bucket": "demo-space",
	"operator": "demo",
	"password": "secret",
})
```

注意事项：


## Google Drive

注册名：`gdrive`

参数：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `credentials` | `string` | 否 | 空 | 凭证 JSON 内容 |
| `credentials_file` | `string` | 否 | 空 | 凭证 JSON 文件路径 |
| `access_token` | `string` | 否 | 空 | OAuth Access Token |
| `root_id` | `string` | 否 | `root` | 根目录 ID |
| `shared_drive_id` | `string` | 否 | 空 | Shared Drive ID |
| `export_mime_type` | `string` | 否 | 空 | Google Workspace 文档导出格式 |
| `page_size` | `int64` | 否 | `1000` | 分页大小 |
| `acknowledge_abuse` | `bool` | 否 | `false` | 下载可疑文件时是否自动确认 |
| `supports_all_drives` | `bool` | 否 | `false` | 是否启用 All Drives 支持 |

示例：

```go
fs, err := driver.New("gdrive", map[string]any{
	"credentials_file": "/path/to/service-account.json",
	"root_id": "root",
	"supports_all_drives": true,
})
```

注意事项：

- 三种认证方式按顺序择一：`credentials`、`credentials_file`、`access_token`
- 如果设置 `shared_drive_id` 且未设置 `root_id`，根目录会自动使用 `shared_drive_id`
- 读取 Google Docs/Sheets/Slides 等原生文档时，必须提供 `export_mime_type`
- `Create()` 对已存在同名文件会执行覆盖更新

## OneDrive

注册名：`onedrive`

参数：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| `access_token` | `string` | 是 | 无 | Microsoft Graph Access Token |
| `endpoint` | `string` | 否 | `https://graph.microsoft.com/v1.0` | Graph API 地址 |
| `drive_id` | `string` | 否 | 空 | 指定 Drive |
| `user_id` | `string` | 否 | 空 | 指定用户的 Drive |
| `root_id` | `string` | 否 | `root` | 根项目 ID |
| `page_size` | `int64` | 否 | `200` | 子项分页大小 |
| `copy_timeout` | `time.Duration` | 否 | `10m` | 异步复制最长等待时间 |

示例：

```go
fs, err := driver.New("onedrive", map[string]any{
	"access_token": token,
	"drive_id": "b!xxxx",
})
```

注意事项：

- `drive_id` 优先于 `user_id`
- `Copy()` 走 Graph 异步复制接口，可能返回 202 并等待轮询完成

## 115 网盘

注册名：`pan115`

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cookie` | `string` | 是 | 115 登录 Cookie |

示例：

```go
fs, err := driver.New("pan115", map[string]any{
	"cookie": cookie,
})
```

`List()` 支持的附加参数：

| 参数名 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `offset` | `int64` | `0` | 分页偏移 |
| `page_size` | `int64` | `50` | 分页大小 |

注意事项：

- 创建驱动时会校验 Cookie 并检查登录状态
- 返回值中的 `ExtraInfo()` 常含 `id`、`pick_code`
- 不支持上传；支持建目录、移动、复制、重命名和删除

## Quark

注册名：`quark`

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cookie` | `string` | 是 | 夸克登录 Cookie |

示例：

```go
fs, err := driver.New("quark", map[string]any{
	"cookie": cookie,
})
```

注意事项：

- 不支持复制和上传；支持浏览、下载、建目录、移动、重命名和删除

## GitHub

注册名：`github`

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `ref` | `string` | 否 | 固定分支/标签名 |
| `repo` | `string` | 否 | 固定仓库名 |
| `owner` | `string` | 是 | 仓库所属用户或组织 |
| `token` | `string` | 否 | GitHub Token |
| `show_tag` | `bool` | 否 | 当未固定 `ref` 时，在根目录下暴露标签层级 |
| `show_branch` | `bool` | 否 | 当未固定 `ref` 时，在根目录下暴露分支层级 |

示例：

```go
fs, err := driver.New("github", map[string]any{
	"owner": "honmaple",
	"repo": "cloudfs",
	"ref": "main",
})
```

路径模型：

- 固定 `repo` 与 `ref` 时：`/README.md`
- 固定 `repo`，但未固定 `ref` 且启用 `show_branch/show_tag` 时：`/main/README.md`
- 都未固定时：`/cloudfs/main/README.md`

注意事项：

- 只支持浏览和下载，不支持写操作
- 分支和标签名中若包含 `/`，对外会使用 URL 编码

## GitHub Release

注册名：`github-release`

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `repo` | `string` | 否 | 固定仓库名 |
| `owner` | `string` | 是 | 仓库所属用户或组织 |
| `release` | `string` | 否 | 固定 Release 名称 |
| `token` | `string` | 否 | GitHub Token |

示例：

```go
fs, err := driver.New("github-release", map[string]any{
	"owner": "cli",
	"repo": "cli",
	"release": "GitHub CLI 2.0.0",
})
```

路径模型：

- 固定 `repo` 和 `release` 时：`/gh_2.0.0_checksums.txt`
- 未固定 `release` 时：`/GitHub%20CLI%202.0.0/gh_2.0.0_checksums.txt`

注意事项：

- Release 目录层级使用 Release 名称，不是 tag
- 只支持浏览和下载 Release 资产

## Mirror

注册名：`mirror`

参数：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `endpoint` | `string` | 是 | 镜像站基础 URL |
| `format` | `string` | 否 | 页面解析格式，可选 `tuna`、`aliyun`，默认按 Nginx 风格解析 |

示例：

```go
fs, err := driver.New("mirror", map[string]any{
	"endpoint": "https://mirrors.example.com",
	"format": "tuna",
})
```

注意事项：

- 这是只读驱动
- `Stat()` 依赖 HTTP `HEAD` 返回的 `Content-Type`、`Content-Length`、`Last-Modified`
- 如果镜像站目录页结构不同，可能需要扩展解析函数

## 驱动选择建议

- 本地或挂载目录优先使用 `local`
- 标准 NAS/服务器文件共享可选 `sftp`、`smb`、`webdav`
- 对象存储优先使用 `s3`
- 需要 SaaS 网盘集成时可选 `gdrive`、`onedrive`
- 仅读场景可考虑 `github`、`github-release`、`mirror`、`pan115`、`quark`

## 构造与配置补充

动态创建前需导入对应驱动或 `driver/all` 完成注册。当前 `driver.New` / `NewFromString` 不应用传入的包装函数，请创建驱动后显式调用 `cloudfs.New`。不支持的操作返回 `cloudfs.ErrNotSupport`。

Google Drive 未配置三种显式凭证时使用 Google 客户端默认凭证发现机制。OneDrive 的 `copy_timeout` 使用标准 `time.Duration`；Go 配置可用 `10 * time.Minute`，JSON 则填写纳秒整数，不是字符串 `"10m"`。
