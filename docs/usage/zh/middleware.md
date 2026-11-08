# 中间件文档

CloudFS 中间件通过 `cloudfs.WrapFunc` 包装已有的 `cloudfs.FS`，用于在不修改底层驱动实现的前提下增加路径映射、缓存、限速、压缩、加密等能力。

## 使用方式

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

`middleware.NewFS` 实际上等价于 `cloudfs.New`。

可用 `middleware.OptionFS` 将 `middleware.Option` 转成传给 `cloudfs.New` 的包装函数。

## 中间件列表

### CacheFS

用于缓存目录列表，减少重复 `List()` 调用。

```go
fs, err := middleware.NewFS(
	base,
	middleware.CacheFS(&middleware.CacheOption{ExpireTime: 60}),
)
```

参数：

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `expire_time` | `time.Duration` | `60` | 缓存有效时长，代码中按“秒”使用 |

行为说明：

- `List()` 会按路径缓存目录结果
- `Stat()` 会优先从父目录缓存中查找目标项
- `Create()`、`Rename()`、`Move()`、`Copy()`、`MakeDir()`、`Remove()` 会主动失效相关目录缓存
- `ExpireTime <= 0` 时使用默认值 `60`

注意事项：

- 当前缓存只缓存目录项，不缓存文件内容
- `ExpireTime` 使用秒数；设置一分钟缓存时填写 `60`，不要填写 `time.Minute`

### PaginationFS

对底层返回的目录列表进行内存分页，不减少底层列表读取量。

```go
fs, err := middleware.NewFS(base, middleware.PaginationFS(&middleware.PaginationOption{}))
files, err := fs.List(ctx, "/photos?page=2&page_size=100")
```

`PageKey` / `page_key` 默认 `page`，`PageSizeKey` / `page_size_key` 默认 `page_size`。页码从 1 开始，非正页码按 1 处理；`page_size <= 0` 时原样委托底层。启用分页时，先从查询中移除这两个参数，再向底层传递其他参数。越界页返回空列表。

### RateLimitFS

用于限制调用频率，适合 API 限流较严格的远程驱动。

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

参数：

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `wait` | `bool` | `false` | 触发限流时是否等待令牌 |
| `burst` | `int` | `100` | 桶容量 |
| `limit` | `time.Duration` | `1` | 单个令牌的时间间隔，代码中按秒处理 |

行为说明：

- 会拦截 `List`、`Copy`、`Move`、`Rename`、`Remove`、`MakeDir`、`Stat`、`Open`、`Create`
- 当 `Wait=false` 且无可用令牌时，返回错误 `访问频率限制`
- 当 `Wait=true` 时，会阻塞直到拿到令牌或 `context.Context` 取消

注意事项：

- 默认值相当于“每秒约 1 次补充，桶容量 100”
- `Limit` 不是 QPS，而是令牌补充间隔

### CompressFS

对写入内容进行 gzip 压缩，对读取内容进行 gzip 解压。

```go
fs, err := middleware.NewFS(
	base,
	middleware.CompressFS(&middleware.CompressOption{
		Level: gzip.BestCompression,
	}),
)
```

参数：

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `level` | `int` | `gzip.BestCompression` | gzip 压缩级别 |

行为说明：

- `Create()` 返回的 writer 会把写入内容压缩后再交给底层驱动
- `Open()` 返回的 reader 会自动解压底层内容

注意事项：

- 底层存储的是压缩后的二进制内容，不会自动追加 `.gz` 后缀
- 只适合由同一层中间件读写的场景

### EncryptFS

对文件内容以及路径名做透明加密/解密。

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

参数：

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `mode` | `string` | `CFB` | 加密模式，可选 `CTR`、`OFB`、其他值回退 `CFB` |
| `dir_name` | `bool` | `false` | 是否加密目录名 |
| `file_name` | `bool` | `false` | 是否加密文件名 |
| `suffix` | `string` | `""` | 不加密文件名时，为底层文件追加后缀 |
| `password` | `string` | 无 | 必填，加密密码 |
| `password_salt` | `string` | 与 `password` 相同 | 生成 IV 使用 |
| `version` | `string` | `v1` | `v2` 使用不同的加密格式，须与已有数据一致 |

行为说明：

- 文件内容通过流式 AES 加密/解密
- 可分别控制目录名和文件名是否加密
- `suffix` 常用于“不加密文件名但隐藏真实扩展名”的场景
- `Stat()` 因为无法预先判断路径是文件还是目录，必要时会尝试两次

注意事项：

- `password` 为空时会直接返回错误
- 读取已有数据时，`Version` 必须与写入时一致；使用 `v1` 或 `v2`
- 修改 `Password`、`PasswordSalt`、`Mode`、`Version` 后，旧数据可能无法再正常读取

### PredicateFS

提供完全自定义的路径映射和文件信息映射能力，适合高级场景。

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

参数：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `PathFn` | `func(string) string` | 将外部路径转换为底层路径 |
| `FileFn` | `func(cloudfs.FileInfo) (cloudfs.FileInfo, bool)` | 转换文件信息，`bool=false` 时会过滤掉当前文件 |

行为说明：

- `PathFn` 会应用于 `List`、`Stat`、`Open`、`Create`、`Copy`、`Move`、`Rename`、`Remove`、`MakeDir`
- `FileFn` 转换 `List` 和 `Stat` 的结果；布尔值仅过滤列表条目，`Stat` 忽略该布尔值
- 这是代码级扩展点，不适合通过 JSON 配置动态构造

### PrefixFS

给底层驱动自动追加固定前缀，是 `PredicateFS` 的常见封装。

```go
fs, err := middleware.NewFS(base, middleware.PrefixFS("/project-a"))
```

效果：

- 访问 `/docs/a.txt` 时，底层会访问 `/project-a/docs/a.txt`
- 对外暴露的路径会自动去掉该前缀

### TrimPrefixFS

把外部路径上的指定前缀裁剪后再传给底层驱动。

```go
fs, err := middleware.NewFS(base, middleware.TrimPrefixFS(base, "/mnt"))
```

效果：

- 对外访问 `/mnt/docs/a.txt`
- 底层实际访问 `/docs/a.txt`

注意事项：

- 这个函数签名为 `TrimPrefixFS(fs cloudfs.FS, prefix string)`，但返回值仍然是 `cloudfs.WrapFunc`
- 适用于需要移除固定路径前缀的场景

## 组合顺序

包装按参数顺序进行，最后一个中间件最靠近调用方，请按实际读写流程选择顺序。若希望写入先压缩再加密，应把加密放在内侧、压缩放在外侧：

```go
fs, err := cloudfs.New(base,
	middleware.EncryptFS(&middleware.EncryptOption{Password: "secret"}),
	middleware.CompressFS(&middleware.CompressOption{}),
)
```

写入流为调用方 → 压缩 → 加密 → 底层；读取则先解密再解压。缓存放在分页内侧可复用完整列表；分页只是切片，不会自动请求远端分页。

## 已知限制

- Go 程序通过 `cloudfs.New` 组合中间件；命令行使用驱动的 `middlewares` 配置，见 [命令行文档](cli.md)
- `PredicateOption` 包含函数，不能直接从 JSON 动态反序列化
- `ExpireTime` 和 `Limit` 均填写秒数，即使 Go 字段类型为 `time.Duration`
