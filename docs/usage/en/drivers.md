# Driver Guide

This guide summarizes the built-in CloudFS drivers, their registration names, options, examples, and notable behavior.

## Overview

| Driver | Name | Typical Use |
| --- | --- | --- |
| Local | `local` | Local filesystem |
| FTP | `ftp` | Traditional FTP servers |
| SFTP | `sftp` | SSH-based file transfer |
| S3 | `s3` | AWS S3 or S3-compatible object storage |
| SMB | `smb` | Windows shares and NAS devices |
| WebDAV | `webdav` | WebDAV services |
| Foxel | `foxel` | Foxel file service |
| OpenList | `openlist` | OpenList/Alist-compatible API |
| UpYun | `upyun` | UpYun object storage |
| Google Drive | `gdrive` | Google Drive and Shared Drives |
| OneDrive | `onedrive` | Microsoft OneDrive |
| 115 Drive | `pan115` | 115 drive, primarily read/download |
| Quark Drive | `quark` | Quark drive, primarily read/download |
| GitHub | `github` | GitHub repository browsing |
| GitHub Release | `github-release` | GitHub release asset browsing |
| Mirror | `mirror` | HTTP directory mirror sites |

## Local

Registration name: `local`

Purpose: access a directory on the local machine.

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `path` | `string` | yes | Local root directory. Must be absolute and already normalized |

Example:

```go
fs, err := driver.New("local", map[string]any{
	"path": "/data/files",
})
```

Notes:

- `path` must be absolute
- `filepath.Clean(path)` must match the original input or the driver returns `fs.ErrInvalid`
- Windows path separators are normalized automatically

## FTP

Registration name: `ftp`

Parameters:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `host` | `string` | yes | none | FTP host |
| `port` | `int` | no | `21` | Port |
| `username` | `string` | yes | none | Username |
| `password` | `string` | yes | none | Password |

Example:

```go
fs, err := driver.New("ftp", map[string]any{
	"host": "127.0.0.1",
	"username": "demo",
	"password": "secret",
})
```

Notes:

- Connection setup uses a 10 second dial timeout
- `Copy()` uses the library-level generic copy flow, which effectively downloads and re-uploads

## SFTP

Registration name: `sftp`

Parameters:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `host` | `string` | yes | none | SFTP host |
| `port` | `int` | no | `22` | Port |
| `username` | `string` | yes | none | Username |
| `password` | `string` | conditionally required | none | Required when `private_key` is not set |
| `private_key` | `string` | conditionally required | none | Required when `password` is not set; contains the private key text |

Example:

```go
fs, err := driver.New("sftp", map[string]any{
	"host": "sftp.example.com",
	"username": "demo",
	"private_key": privateKeyPEM,
})
```

Notes:

- Host key verification currently uses `ssh.InsecureIgnoreHostKey()`
- `private_key` must be PEM text accepted by `ssh.ParsePrivateKey()`

## S3

Registration name: `s3`

Parameters:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `endpoint` | `string` | yes | none | S3 or S3-compatible endpoint |
| `bucket` | `string` | yes | none | Bucket name |
| `region` | `string` | no | empty | Region |
| `access_key` | `string` | no | empty | Access key |
| `secret_key` | `string` | conditionally required | empty | Required when `access_key` is set |
| `session_token` | `string` | no | empty | Temporary session token |
| `list_version` | `string` | no | empty | `v1` or `v2` list API |
| `force_path_style` | `bool` | no | `false` | Force path-style addressing |

Example:

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

Notes:

- Directories are modeled as empty objects with a trailing slash
- `Copy()` uses server-side copy for files and recursive copy for directories
- `Stat()` first checks for a file and then falls back to prefix-based directory detection

## SMB

Registration name: `smb`

Parameters:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `host` | `string` | yes | none | SMB host |
| `port` | `int` | no | `445` | Port |
| `username` | `string` | yes | none | Username |
| `password` | `string` | yes | none | Password |
| `domain` | `string` | no | empty | Domain |
| `share_name` | `string` | yes | none | Share name |

Example:

```go
fs, err := driver.New("smb", map[string]any{
	"host": "192.168.1.10",
	"username": "demo",
	"password": "secret",
	"share_name": "public",
})
```

Notes:

- Use paths starting with `/`, such as `/docs/file.txt`
- `MakeDir()` uses permission `0700`

## WebDAV

Registration name: `webdav`

Parameters:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `endpoint` | `string` | yes | none | WebDAV endpoint |
| `username` | `string` | yes | none | Username |
| `password` | `string` | yes | none | Password |
| `dir_perm` | `os.FileMode` | no | `0755` | Permission used for mkdir and stream writes |

Example:

```go
fs, err := driver.New("webdav", map[string]any{
	"endpoint": "https://example.com/dav",
	"username": "demo",
	"password": "secret",
})
```

Notes:

- Directory and file permissions are fixed at `0755`; `dir_perm` does not change that value
- `Stat()` maps WebDAV 403/404 errors to `os.ErrPermission` and `os.ErrNotExist`

## Foxel

Registration name: `foxel`

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `endpoint` | `string` | yes | Foxel service endpoint |
| `username` | `string` | yes | Username |
| `password` | `string` | yes | Password |

Example:

```go
fs, err := driver.New("foxel", map[string]any{
	"endpoint": "https://foxel.example.com",
	"username": "demo",
	"password": "secret",
})
```

Notes:

- The driver logs in during construction and stores a bearer token
- It retries once after a 401 by logging in again
- `List()` uses a built-in page size of 500

## OpenList

Registration name: `openlist`

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `endpoint` | `string` | yes | OpenList service endpoint |
| `username` | `string` | yes | Username |
| `password` | `string` | yes | Password |

Example:

```go
fs, err := driver.New("openlist", map[string]any{
	"endpoint": "https://openlist.example.com",
	"username": "demo",
	"password": "secret",
})
```

Additional `List()` options:

| Option | Type | Description |
| --- | --- | --- |
| `password` | `string` | Directory password passed to `/api/fs/list` |

Example:

```go
files, err := fs.List(ctx, cloudfs.PathWithValues("/secret", map[string]any{
	"password": "dir-pass",
}))
```

Notes:

- The server must support the OpenList/Alist API
- Construction performs login immediately

## UpYun

Registration name: `upyun`

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `bucket` | `string` | yes | Service bucket |
| `operator` | `string` | yes | Operator account |
| `password` | `string` | yes | Operator password |

Example:

```go
fs, err := driver.New("upyun", map[string]any{
	"bucket": "demo-space",
	"operator": "demo",
	"password": "secret",
})
```

Notes:


## Google Drive

Registration name: `gdrive`

Parameters:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `credentials` | `string` | no | empty | Credentials JSON content |
| `credentials_file` | `string` | no | empty | Path to credentials JSON |
| `access_token` | `string` | no | empty | OAuth access token |
| `root_id` | `string` | no | `root` | Root folder ID |
| `shared_drive_id` | `string` | no | empty | Shared Drive ID |
| `export_mime_type` | `string` | no | empty | Export format for Google Workspace docs |
| `page_size` | `int64` | no | `1000` | Page size |
| `acknowledge_abuse` | `bool` | no | `false` | Auto-acknowledge abusive file downloads |
| `supports_all_drives` | `bool` | no | `false` | Enable All Drives support |

Example:

```go
fs, err := driver.New("gdrive", map[string]any{
	"credentials_file": "/path/to/service-account.json",
	"root_id": "root",
	"supports_all_drives": true,
})
```

Notes:

- Auth sources are selected in this order: `credentials`, `credentials_file`, `access_token`
- If `shared_drive_id` is set and `root_id` is empty, the root becomes `shared_drive_id`
- Reading native Google Docs/Sheets/Slides requires `export_mime_type`
- `Create()` overwrites an existing file with the same name

## OneDrive

Registration name: `onedrive`

Parameters:

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `access_token` | `string` | yes | none | Microsoft Graph access token |
| `endpoint` | `string` | no | `https://graph.microsoft.com/v1.0` | Graph API endpoint |
| `drive_id` | `string` | no | empty | Target drive |
| `user_id` | `string` | no | empty | Target user drive |
| `root_id` | `string` | no | `root` | Root item ID |
| `page_size` | `int64` | no | `200` | Child page size |
| `copy_timeout` | `time.Duration` | no | `10m` | Max wait for async copy completion |

Example:

```go
fs, err := driver.New("onedrive", map[string]any{
	"access_token": token,
	"drive_id": "b!xxxx",
})
```

Notes:

- `drive_id` takes precedence over `user_id`
- `Copy()` uses the Graph async copy API and may return 202 before polling for completion

## 115 Drive

Registration name: `pan115`

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `cookie` | `string` | yes | Logged-in 115 cookie |

Example:

```go
fs, err := driver.New("pan115", map[string]any{
	"cookie": cookie,
})
```

Additional `List()` options:

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `offset` | `int64` | `0` | Pagination offset |
| `page_size` | `int64` | `50` | Page size |

Notes:

- The driver validates the cookie and performs a login check
- `ExtraInfo()` commonly contains `id` and `pick_code`
- Uploads are unsupported; directory creation, moving, copying, renaming, and removal are supported

## Quark

Registration name: `quark`

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `cookie` | `string` | yes | Logged-in Quark cookie |

Example:

```go
fs, err := driver.New("quark", map[string]any{
	"cookie": cookie,
})
```

Notes:

- Copy and upload are unsupported; browsing, downloads, directory creation, moving, renaming, and removal are supported

## GitHub

Registration name: `github`

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `ref` | `string` | no | Fixed branch or tag |
| `repo` | `string` | no | Fixed repository |
| `owner` | `string` | yes | Repository owner or organization |
| `token` | `string` | no | GitHub token |
| `show_tag` | `bool` | no | Expose tags as a top-level layer when `ref` is not fixed |
| `show_branch` | `bool` | no | Expose branches as a top-level layer when `ref` is not fixed |

Example:

```go
fs, err := driver.New("github", map[string]any{
	"owner": "honmaple",
	"repo": "cloudfs",
	"ref": "main",
})
```

Path model:

- With fixed `repo` and `ref`: `/README.md`
- With fixed `repo` but dynamic refs: `/main/README.md`
- With both dynamic: `/cloudfs/main/README.md`

Notes:

- Read-only browsing and download driver
- Branch and tag names containing `/` are exposed through URL escaping

## GitHub Release

Registration name: `github-release`

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `repo` | `string` | no | Fixed repository |
| `owner` | `string` | yes | Repository owner or organization |
| `release` | `string` | no | Fixed release name |
| `token` | `string` | no | GitHub token |

Example:

```go
fs, err := driver.New("github-release", map[string]any{
	"owner": "cli",
	"repo": "cli",
	"release": "GitHub CLI 2.0.0",
})
```

Path model:

- With fixed `repo` and `release`: `/gh_2.0.0_checksums.txt`
- With dynamic release selection: `/GitHub%20CLI%202.0.0/gh_2.0.0_checksums.txt`

Notes:

- The directory layer uses the release name, not the tag
- Read-only release asset browser

## Mirror

Registration name: `mirror`

Parameters:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `endpoint` | `string` | yes | Base URL of the mirror site |
| `format` | `string` | no | Parser format: `tuna`, `aliyun`, or default nginx-style parsing |

Example:

```go
fs, err := driver.New("mirror", map[string]any{
	"endpoint": "https://mirrors.example.com",
	"format": "tuna",
})
```

Notes:

- Read-only driver
- `Stat()` depends on HTTP `HEAD` headers such as `Content-Type`, `Content-Length`, and `Last-Modified`
- Custom mirror HTML layouts may require parser extensions

## Choosing a Driver

- Use `local` for local directories or mounted filesystems
- Use `sftp`, `smb`, or `webdav` for standard NAS/server file sharing
- Use `s3` for object storage
- Use `gdrive` or `onedrive` for SaaS drive integration
- Use `github`, `github-release`, `mirror`, `pan115`, or `quark` for read-heavy scenarios

## Construction and Configuration Notes

Import the selected driver or `driver/all` before dynamic construction. Currently `driver.New` / `NewFromString` do not apply their wrapper arguments; explicitly wrap the result with `cloudfs.New`. Unsupported operations return `cloudfs.ErrNotSupport`.

Without explicit Google Drive credentials, the Google client uses default credential discovery. OneDrive `copy_timeout` uses standard `time.Duration`: use `10 * time.Minute` in Go or integer nanoseconds in JSON, not the string `"10m"`.
