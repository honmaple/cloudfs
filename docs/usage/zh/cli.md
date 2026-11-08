# 命令行使用

CloudFS 命令支持本地和云存储之间的文件操作与同步。

```sh
go install github.com/honmaple/cloudfs/cmd/cloudfs@latest
# 源码构建
go build -o cloudfs ./cmd/cloudfs
cloudfs --help
cloudfs help cp
```

## 配置文件查找

未指定 `--config` / `-c` 时，按顺序查找当前目录的 `config.json`、`config.yaml`、`config.yml`、`config.toml`，使用第一个存在的文件，不合并多个文件。均不存在时仍可操作本地路径；访问具名驱动需提供配置。

显式指定的配置文件优先。找到的配置不可读取或格式有误时会返回错误，不会继续尝试其他文件，也不会执行文件操作。

```sh
# 自动读取当前目录的 config.yaml（不存在 config.json 时）
cloudfs ls name1:
# 显式选择其他配置
cloudfs -c ./production.toml ls name1:/
```

## 路径与基本操作

`name:/path` 访问配置中的具名驱动，`name:` 等同于根目录 `name:/`。其余路径均为操作系统本地路径，支持绝对路径、相对路径和 `..`。相对路径以当前工作目录为基准，`ls` 默认列出当前目录。全局 `--config`（别名 `-c`）可以放在子命令之前或路径之后。本地操作无需配置。

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

`cp/mv` 的第二参数始终是完整目标路径；`rename` 的第二参数只能是新名称。`ls` 输出类型、大小、修改时间和名称；`stat` 输出 JSON；`cat` 原样写入标准输出，可用 shell 重定向保存。删除目录须显式使用 `-r` / `--recursive`，禁止删除文件系统根目录。

```sh
cloudfs -c config.yaml cp ./hello.txt name1:/hello.txt
cloudfs -c config.yaml cp name1:/hello.txt ./download.txt
cloudfs -c config.yaml cat name1:/hello.txt > ./output.txt
cloudfs -c config.yaml sync --conflict src ./documents name2:/backup
```

本地与驱动之间的上传、下载统一使用 `cp`，不再提供 `put/get` 或 `--root`。`cp/mv` 可能覆盖文件，父目录须存在，失败可能留下部分文件；错误输出到标准错误并以退出码 1 退出。Ctrl+C 取消当前操作，关闭流和文件系统的错误会返回。配置含凭证时应限制读取权限，例如 `chmod 600 config.yaml`。

## 多驱动配置与跨驱动操作

配置根据扩展名支持 `.json`、`.yaml`、`.yml`、`.toml`。驱动名称直接作为顶层键，无需额外包装。名称不区分大小写。下面 YAML / TOML 与 JSON 示例等价：

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

`cp/mv` 的目标为完整路径，文件不会再自动追加源文件名；目录复制把子项复制到指定目标目录并合并已有内容，不删除目标独有项。文件目标的父目录需存在。`cp/mv` 均支持具名驱动。传输通过流进行，不整文件载入内存；跨驱动不调用服务端复制。`mv` 在复制及关闭流成功后才删除源，删除失败会留下两份数据，整个移动不保证原子性。相同或重叠目录会被拒绝；不同远程名称指向同一后端时仍需调用方避免别名重叠。

配置仅使用顶层驱动名称映射；无论是否提供配置，普通路径始终为本地路径。`ls/cat/stat/mkdir/rename/rm` 均支持 `name:/path`。

## 同步命令

`sync` 使用 `ModeOneWay`；`bisync` 使用 `ModeTwoWay`，必须指定 `--state`。

同步命令将事件写入标准输出，事件中的文件路径相对于同步根目录。每个阶段的结果通过 `elapsed` 显示该阶段的耗时，收尾阶段只输出结果。

```sh
cloudfs --config drivers.json sync --conflict src name1:/documents name2:/backup
cloudfs sync name1:/documents name2:/backup --dry-run --state ./sync-state.json --config drivers.json
cloudfs --config drivers.json sync --delete --state ./sync-state.json name1:/documents name2:/backup
cloudfs --config drivers.json bisync --state ./bisync-state.json name1:/documents name2:/backup
```

`sync` 同步目录内容，默认保留目标独有文件，`--delete` 开启删除。`--state` 可选，提供后记录元数据。`bisync` 要求 `--state`，传播双方变化及删除。两端目录须已存在。子命令选项可以放在路径之前或之后。

- `--concurrency N`：文件复制及最后的文件删除的并发上限，默认 1，0 也按串行执行。大于 1 时要求驱动及中间件支持并发调用。重命名、类型替换准备和建目录先执行，复制后再删除文件及目录；错误后等待在途任务结束再返回。
- `--dry-run`：输出阶段事件，计划中的文件操作标为 `status=planned`，不修改文件或状态。
- `--max-delete N`：限制两端合计删除条目数，包含递归目录内容和类型替换。0 表示不限制。
- `--max-delete-percent N`：限制各接收端删除比例，范围 0–100，0 禁用。超过任一删除限制时，整轮写入前报错，预演同样检查。普通文件覆盖不计入删除。
- `--checksum`：按哈希比较文件内容。大小和修改时间与历史一致时复用已有哈希。
- `--conflict abort|skip|src|dst|newer|both`：默认 `abort`，不同文件或类型冲突会报错；`src/dst` 显式选择一端，`skip` 保留冲突路径。
- `--conflict newer`：比较两个普通文件的已知修改时间，同时间选源端；删除冲突保留仍存在的一端。双方存在时，目录或未知时间会终止。单向不会反写源端。
- `--conflict both`：仅用于 `bisync`。文件冲突保留源版本在原路径，目标版本另存到两端 `.conflict` 副本；类型冲突保留目录、另存文件；删除冲突恢复仍存在的版本。预演显示副本的实际目标路径。
- `--state FILE`：本地 JSON 状态文件。父目录须存在，更新时原子替换文件，权限为 `0600`。

跨存储重复同步时，从第一轮起使用同一个 `--state` 文件，分别记录两端的修改时间。

状态保存元数据及有顺序的端点标识，配置内容只保存摘要；更换名称、配置（含凭证）或目录后，旧状态会被拒绝，须使用独立状态文件。不要把状态文件放在同步目录内，也不要对同一同步对或状态文件并发运行命令。状态格式为命令专用外壳，不是裸 `syncer.State`。复制、删除、冲突和部分成功保存遵循 [同步指南](syncer.md)。

配置状态存储后，取消同步时会尝试保存已确认结果；若开始保存时已经取消，保存上下文具有独立的 5 秒期限。取消与保存错误都会返回，强制退出仍可能丢失本轮状态。

## 每个驱动的中间件

驱动配置可选 `middlewares` 有序列表，每项包含 `type` 和可选 `option` 对象。省略 `option` 使用默认值；配置错误在创建驱动前返回。JSON、YAML、TOML 均支持此结构：

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

`backup:/file.txt` 映射到底层 `/data/archive/file.txt`。列表从上到下包装，最后一项最靠近调用方；上述内容写入顺序为压缩 → 加密 → 底层，读取时逆向还原。底层前缀目录应已存在。

| type | option 字段 | 说明 |
| --- | --- | --- |
| `prefix` | `prefix` | 必须以 `/` 开头，映射到指定目录 |
| `trim_prefix` | `prefix` | 必须以 `/` 开头，移除传入路径前缀 |
| `cache` | `expire_time` | 秒数，默认 60 |
| `pagination` | `page_key`, `page_size_key` | 默认 `page`, `page_size`；按查询参数对完整列表分页 |
| `rate_limit` | `wait`, `burst`, `limit` | 默认 false、100、1；`limit` 填秒数，例如 1，不能填时间字符串 |
| `compress` | `level` | gzip 级别 -2 到 9；0 使用默认最佳压缩 |
| `encrypt` | `password`, `password_salt`, `mode`, `version`, `dir_name`, `file_name`, `suffix` | `password` 必填，其他参数见中间件文档 |

依赖 Go 回调函数的 `PredicateFS` 不支持配置。未知类型、选项字段或错误类型会报错。中间件配置参与同步状态绑定，修改后需使用匹配的新状态文件。同步要求 FS 的大小和读取内容保持一致；内容转换中间件不自动修正底层大小，组合用于同步前需确认符合比较与传输校验契约。更多行为见 [中间件文档](middleware.md)。
