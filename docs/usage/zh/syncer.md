# 同步使用指南

通过 `Sync` 和 `Option.Mode` 选择模式。`ModeCopy` 是默认的单向同步，保留目标端独有条目。`ModeMirror` 从源端复制到目标端，复制后删除目标端独有条目。`ModeTwoWay` 双向传播变化及删除，必须提供 `StateStore`。

导入 `github.com/honmaple/cloudfs/syncer`。`Sync` 使用传入文件系统的 `/`，两端根目录须已存在。同步子目录时先通过 `middleware.PrefixFS` 包装：

```go
source, err := cloudfs.New(src, middleware.PrefixFS("/documents"))
if err != nil {
	return err
}
target, err := cloudfs.New(dst, middleware.PrefixFS("/backup"))
if err != nil {
	return err
}
return syncer.Sync(ctx, source, target)
```

## 自定义过滤

`Option.Skipper` 接受 `syncer.Skipper`，即 `func(info syncer.FileInfo) bool`，返回 true 时跳过路径：

```go
option := syncer.Option{
	Skipper: func(info syncer.FileInfo) bool {
		return info.Path() == "cache" || strings.HasSuffix(info.Path(), ".tmp")
	},
}
```

需要导入 `strings`。回调在每端扫描过程中、读取文件计算哈希之前同步执行，此时 `Hash()` 为空；不接收根目录 `/`，路径相对于同步根目录。命中的目录不再遍历，后代不调用回调。历史条目不传给回调。本次扫描命中的路径及被过滤目录遮蔽的后代保留旧基线；两端均确认不存在且未被过滤范围覆盖的历史路径正常清理。回调不得修改文件系统或元数据。

任一端命中就跳过两端同路径及其后代，保留旧元数据，不产生冲突回调和任务事件。若删除或类型替换父目录会影响被过滤的后代，则跳过该父目录的整棵子树操作。新目录可以创建并同步未被过滤的子项。nil 表示不过滤。

命中文件不读取内容计算 `Checksum`，命中目录不再列举子项。规划遍历时同时检查两端当前路径的过滤标记；任一端命中即跳过该路径或子树，两端均不计算其哈希。仍需列举父目录才能发现条目，过滤前遇到的扫描错误正常返回。修改过滤条件后，下一次调用重新扫描并按保留的历史正常比较。

## 内置冲突处理

两种同步选项均通过 `OnConflict` 接收 `ConflictHandler`，为空时默认使用 `Abort()`：

```go
option := syncer.Option{
    Mode: syncer.ModeTwoWay,
    StateStore: store,
    OnConflict: syncer.PreferNewer(),
}
```

| 处理函数 | 冲突行为 |
| --- | --- |
| `Abort()` | 终止本轮同步，写入前返回 `ErrSyncConflict`。 |
| `Skip()` | 保留冲突路径和旧状态，继续其他路径。 |
| `PreferSrc()` | 选择源端版本，包括源端删除。 |
| `PreferDst()` | 选择目标端版本，包括目标端删除；单向同步只保留目标，不反写源端。 |
| `KeepBoth()` | 双向同步保留双方版本，详见下文。 |
| `PreferNewer()` | 删除冲突保留仍存在的一端；两个普通文件按已知修改时间选择较新者，同时间选源端。 |

`PreferNewer()` 只比较提供的修改时间，不读取文件；两端时间须可比较。双方均存在时，遇到目录或任一时间未知会选择终止，因为目录快照不保存修改时间。单向同步选择较新的目标时保留目标，不反向复制。此处理函数只作用于交给回调的冲突，目标独有条目由 `ModeMirror` 清理。

自定义回调仍返回 `ConflictPolicy` 枚举和错误；可按路径选择 `ConflictAbort`、`ConflictSkip`、`ConflictPreferSrc`、`ConflictPreferDst`、`ConflictKeepBoth`，或调用内置处理函数。回调错误保留原始文本，在任何写入前终止。

## 并发复制

`Option.Concurrency` 控制文件复制及最后的文件删除的并发上限，默认值 0 和值 1 都串行执行；负值无效：

```go
option := syncer.Option{
    Mode: syncer.ModeTwoWay,
    Concurrency: 4,
    StateStore:  store,
}
```

大于 1 时，传入的两个文件系统及其全部中间件必须支持并发调用。操作按重命名、类型替换所需删除、建目录、复制、删除文件、删除目录的顺序执行，各阶段完成后再进入下一阶段。建目录从浅到深，删除目录从深到浅；复制和最后的文件删除最多并发 `Concurrency` 个任务，空位释放后立即继续派发。扫描与哈希比较仍按原流程执行，预演按相同阶段顺序报告操作。

同一任务的事件顺序不变，不同任务的事件可交错，完成顺序不保证与规划顺序相同。`OnEvent` 和 `OnError` 的用户代码串行执行，但可能来自不同 goroutine。回调仍不得修改同步中的文件系统。

收到未忽略的错误后，停止派发新任务并等待在途任务结束；已确认的成功结果参与收尾保存，在途任务的错误一并返回。复制阶段未成功结束时，不进入最后的删除阶段。可取消调用方上下文来中止在途操作，取消后的保存遵循前述独立保存期限。返回前不会留下仍在执行的任务。

## 按需哈希

`cloudfs.Hasher` 提供 `Hashes() []crypto.Hash` 和 `Hash(ctx, path, algorithm)`。`Hashes` 按优先级列出驱动支持的算法。同步循环选择两端共同支持且可用于流式校验的首个算法，没有共同算法时使用 SHA-256。驱动返回所选算法的小写十六进制内容摘要；返回空字符串或 `cloudfs.ErrNotSupport` 时，同步读取文件计算，其他错误直接返回。`PrefixFS` 转发算法列表和哈希请求。

同步默认按大小和修改时间比较。设置 `Checksum: true` 后按哈希比较文件内容，并在有可用历史哈希时复用。首次复制通过传输流建立两端哈希基线；规划阶段仅在需要比较内容时读取哈希。之后，文件大小和修改时间与该端历史一致、大小已知、时间非零且历史有哈希时直接复用；其余参与这些规划判断的文件向驱动获取所选算法的摘要，没有可用摘要时读取内容计算。只改变时间、内容相同的文件不会重复复制，也不修改另一端时间，只刷新本端状态。计算哈希不报告复制进度。

每端同一路径在一轮规划中最多读取一次，复制传输另行读取。单向同步中仅需保留或删除的目标独有文件、被跳过的类型冲突子树，以及终止后未遍历的路径不计算哈希。复制方向确定后，生成任务不再预读源文件。新增文件、选中的类型替换和子树复制通过传输流计算哈希；如果比较时已经取得哈希，复制时仍校验一致性。成功复制后，两端基线都记录实际传输哈希，不依赖是否开启比较选项。复用历史哈希依赖大小和修改时间，可能漏掉两者均未变化的内容修改。`Checksum` 可在不同轮次之间切换。旧状态缺少哈希时会重新读取；新发现的内容差异按正常变更或冲突规则处理，不会仅因旧大小和时间一致而被忽略。

过滤先于哈希读取，预演仍会进行必要读取但不保存刷新后的状态。读取错误与关闭错误会一并返回。

## 预演与删除保护

`Option` 支持 `DryRun`、`MaxDelete`、`MaxDeletePercent`：

```go
option := syncer.Option{
    DryRun:           true,
    Mode:             syncer.ModeMirror,
    MaxDelete:        syncer.MaxDelete(100),
    MaxDeletePercent: 20,
    OnConflict: syncer.PreferSrc(),
    OnEvent: func(notification syncer.Event) {
        if event, ok := notification.(syncer.TaskEvent); ok {
            log.Printf("%s %s %s", event.Status, event.Op, event.FileInfo.Path())
        }
    },
}
```

预演正常加载状态、扫描和解决冲突，通过 `OnEvent` 为每个计划操作发送一次 `StatusPlanned`，不执行写入、不保存状态、不发送复制进度或任务成功事件，阶段事件仍正常发送。哈希比较和回调照常执行，因此调用方应避免回调产生写入副作用。双向预演仍要求 `StateStore`；单向预演仅在提供存储时加载状态。预演不保留可供稍后执行的计划；实际同步会重新扫描。

`MaxDelete` 限制两端合计显式删除的条目数。省略时不限制，`syncer.MaxDelete(0)` 禁止删除。文件和目录各计一项，递归删除包含全部已扫描后代，同一端同一路径只计一次。各模式下类型替换造成的删除也计入。普通文件覆盖不计入删除数。

`MaxDeletePercent` 按每个接收端分别检查：计划删除条目数除以该端当前扫描条目数，根目录不计入。两个限制同时设置时必须同时满足；达到限制允许执行，超过才拒绝。`MaxDeletePercent` 默认为 0，表示禁用。删除上限必须为非负数，百分比范围为 0–100。

超过限制返回可由 `errors.Is` 判断的 `ErrTooManyDeletes`，整轮不执行任何写入或保存状态。预演同样遵守限制。过滤、跳过的冲突不会产生删除任务，也不增加删除计数。

## 源文件一致性

每次复制尝试前检查源文件的类型、大小和修改时间是否符合扫描快照；成功读取并关闭流后再次检查。存在规划哈希时，复制还会利用传输流校验哈希，不额外读取内容。检测到变化返回可由 `errors.Is` 判断的 `ErrSourceChanged`，该文件不推进同步状态。复制前发现变化时，本次复制尝试不会打开目标写入；此前的类型替换删除等任务可能已修改目标，复制中发生变化也可能已影响目标，这不构成原子覆盖。

复制中的检查失败进入 `OnError`，可返回 `ErrorSkip` 跳过，或返回错误终止。重试仍使用原扫描快照，源文件仍有变化时继续拒绝；下一次同步重新扫描。规划阶段获取哈希失败时直接停止规划，不进入 `OnError`。每次完整读取的复制尝试增加两次源端 `Stat`；规划阶段计算哈希不额外调用 `Stat`。比较元数据或复用历史哈希时，大小和时间均不变的修改仍可能漏检，调用方仍须排除并发写入。

## 单向和镜像同步

```go
err := syncer.Sync(ctx, src, dst, syncer.Option{
	Mode:       syncer.ModeMirror,
	OnConflict: syncer.PreferSrc(),

	StateStore:     store,
})
```

`ModeCopy` 保留目标端独有条目，`ModeMirror` 在复制后清理这些条目。默认冲突策略为 `Abort()`，已有文件内容或类型不一致时在写入前返回 `ErrSyncConflict`；显式设置 `OnConflict: syncer.PreferSrc()` 才覆盖目标。两种模式处理类型替换时都会先删除目标冲突路径，再创建源子树。目录只调用一次 `FS.Remove`，不产生逐个子项删除事件。

`PreferDst()` 和 `Skip()` 保留冲突子树及旧元数据，不反向写入源端。`OnConflict` 逐路径选择策略；回调错误会在写入前终止。

同步分为扫描、规划、执行三个阶段：扫描只收集元数据并过滤目录；规划遍历时检查两端过滤标记，按配置计算或复用哈希，完成对比、冲突处理、任务生成和删除限制检查；最后统一执行任务。每次调用只扫描两端各一次，全部规划成功后才写入。包含空目录；不复制权限和时间戳，拒绝符号链接及特殊文件。默认按大小和修改时间比较。`Checksum: true` 时按需复用或获取哈希来比较内容。双向同步判断删除与子树修改时，也按需检查后代内容。双方都有哈希时比较哈希，否则比较大小和时间；相同大小、相同时间的内容变化可能无法识别。

复制始终顺带计算实际传输哈希用于两端确认基线；存在规划阶段取得的源端哈希时会校验传输哈希，不重读目标内容。复制检查两端流的 `Close` 错误，均成功后才通过目标 `Stat` 确认结果。关闭失败进入 `OnError`，重试从头复制，未确认传输不推进元数据；传输与关闭错误同时保留，可用 `errors.Is` 判断。驱动、回调和存储错误保留原始文本；路径、操作和方向可通过回调信息及任务事件获取。失败可能留下不完整文件，不保证原子覆盖。调用方负责关闭文件系统，并保证同步期间源内容稳定、没有并发写入。

## 同步状态存储

单向同步根据 `StateStore` 是否为 nil 决定是否记录：提供存储时自动加载和保存状态，为 nil 时不记录。双向同步始终需要存储。以下为存储契约：

```go
type StateStore interface {
	Load(context.Context) (*syncer.State, error)
	Save(context.Context, *syncer.State) error
}
```

要将状态保存为本地 JSON 文件，可以直接配置 `FileStore`。

```go
option := syncer.Option{
    Mode:       syncer.ModeTwoWay,
    StateStore: syncer.NewFileStore("sync-state.json", "photos-source", "photos-target"),
}
```

源端和目标端按顺序使用各自同步根目录的稳定标识。状态文件应放在两端同步范围之外，其父目录须已存在。

首次加载返回 nil。存储绑定固定、有顺序的一对文件系统，调用方负责持久化及独占执行。`Save` 必须原子替换整份记录，失败后保留完整旧值或新值；内存存储可以保留指针，持久化存储可自行使用 JSON。传入、返回的元数据均应视为只读。

`State.Source`、`Target` 为相对路径到 `*Record` 的映射，记录不可为 nil；`Record` 保存 `IsDir`、`Size`、`ModTime`、`Hash`。`HashAlgorithm` 为本次状态使用的 `crypto.Hash`，后续同步沿用该算法；此字段必填。`Version` 当前为 1。`Revision` 是内容修订标识，加载允许非空自定义标识；保存时计算内容哈希，无变化则跳过保存。`UpdatedAt` 为准备保存变化内容的 UTC 时间，部分成功也会更新，不代表整轮同步成功。

`Record.Valid(state.HashAlgorithm)`、`State.Valid()` 可用于校验；`Record.Equal()` 比较目录时只比较类型。`State.UpdateRevision()` 更新内容标识，不更新 `UpdatedAt`，应在共享记录前调用。切换 `Checksum` 不要求重建元数据，已知哈希会保留。

只有已确认结果推进元数据。后续操作失败或取消时，结束阶段仍尝试保存已确认结果；失败、忽略、未执行路径保留旧值。已确认删除和重命名按实际受影响端更新。

若开始保存时调用方上下文已取消，会使用保留上下文值、具有独立 5 秒期限的保存上下文；存储实现必须响应其取消和期限。同步仍返回原取消错误，保存失败一并返回，可分别用 `errors.Is` 判断。正常保存沿用调用方上下文；若取消发生在保存过程中，该次保存可能失败，不会自动重试。

保存仍只在结束阶段执行，不逐文件持久化。强制退出、进程崩溃或保存失败可能只留下之前的记录。每次调用重新扫描，不保存任务进度，也不提供断点续传或延迟执行计划。

## 错误重试与事件

`OnError` 接收同步上下文和 `FileError`，返回 `ErrorPolicy` 及错误。`FileError` 包括 `FileInfo`、`Op`、`Reverse`、`NewPath`、`Attempt`、`Err`；路径通过 `FileInfo.Path()` 获取，`Attempt` 从 1 开始。`Error()` 保留原错误文本，`Unwrap()` 返回原错误，可使用 `errors.Is` / `errors.As`。

```go
option := syncer.Option{
    MaxRetry: 2,
    OnError: func(ctx context.Context, failure syncer.FileError) (syncer.ErrorPolicy, error) {
        return syncer.ErrorRetry, nil
    },
}
```

`ErrorPolicy` 包括 `ErrorAbort`、`ErrorSkip`、`ErrorRetry`。`ErrorAbort` 返回本次操作错误并终止；`ErrorSkip` 跳过操作；`ErrorRetry` 使用同一同步上下文和预期快照重试当前操作。未设置 `OnError` 时终止。回调返回非 nil 错误时，不论策略为何，都以该错误终止。

每次失败都会调用回调。`MaxRetry` 限制每个操作的重试次数，小于等于 0 使用默认值 3；初次尝试不计入重试次数。达到上限后仍返回 `ErrorRetry`，则以最后一次操作错误终止。上例每个操作最多重试两次；回调可提前选择终止或跳过，也可控制等待和退避。复制重试从头开始。忽略建目录会跳过后代，忽略类型替换所需的删除或重命名会阻止后续依赖操作。复制过程的源文件检查进入 `OnError`；扫描、目标元数据确认、存储、取消错误和执行时路径冲突不进入 `OnError`。

单向和双向同步均通过 `OnEvent` 发送阶段事件、任务事件和复制进度事件：

```go
handler := func(notification syncer.Event) {
    switch event := notification.(type) {
    case syncer.SyncEvent:
        log.Printf("phase=%s status=%s err=%v", event.Phase, event.Status, event.Err)
    case syncer.TaskEvent:
        log.Printf("%s %s %s reverse=%t new_path=%s err=%v",
            event.FileInfo.Path(), event.Op, event.Status,
            event.Reverse, event.NewPath, event.Err)
    case syncer.ProgressEvent:
        log.Printf("%s: %d/%d bytes",
            event.FileInfo.Path(), event.Written, event.FileInfo.Size())
    }
}
```

所有事件均实现 `String()`，可直接打印：

```go
OnEvent: func(event syncer.Event) {
    log.Print(event)
},
```

`SyncEvent` 表示整个同步过程的阶段，包含 `Phase`、`Status`、`Err`。

| Phase | 过程 |
| --- | --- |
| `PhaseScan` | 加载历史状态、扫描两端并应用路径过滤 |
| `PhasePlan` | 比较并生成任务 |
| `PhaseExecute` | 执行任务，预演时报告任务计划 |
| `PhaseFinish` | 整理和保存已确认状态，预演跳过此阶段 |

每个阶段先发送 `StatusStarted`，结束后发送 `StatusSucceeded` 或带 `Err` 的 `StatusFailed`。扫描或规划失败后停止；执行失败仍进入收尾阶段。任务列表为空时跳过执行阶段。

`Status` 包括 `StatusStarted`、`StatusSucceeded`、`StatusFailed`、`StatusSkipped`、`StatusPlanned`。

`Op` 包括 `OpCopy`、`OpRename`、`OpMkdir`、`OpRemove`、`OpRmdir`。

`TaskEvent.FileInfo` 描述复制的读取端、待创建目录，或重命名、删除的对象。`Reverse` 为 false 表示写入原始目标端，为 true 表示写入原始源端；重命名事件的 `NewPath` 给出目标路径。路径相对于同步根目录；快照可保留，访问不产生额外 I/O。

尝试执行时发送 `StatusStarted`，重试处理结束后发送一次 `StatusSucceeded`、`StatusFailed` 或 `StatusSkipped`；依赖被阻塞的任务只发送跳过。失败事件带 `Err`，忽略的原始错误由 `OnError` 获取。

回调用户代码同步串行执行，不得修改同步中的文件系统；并发复制时，不同任务的事件可交错。无变化文件、比较阶段跳过的冲突和终止后未执行的任务不产生事件。任务成功不代表最终状态保存成功。

## 双向同步

```go
err := syncer.Sync(ctx, src, dst, syncer.Option{
    Mode: syncer.ModeTwoWay,
	StateStore:     store,
	OnConflict: syncer.Abort(),
})
```

首次合并不同路径；后续按两端各自历史识别变化与删除，上传时间不同不会导致重复传输。冲突包括双方修改、删除与修改并发以及类型不符：

- `Abort()`：默认，在任何写入前报错。
- `Skip()`：保留冲突路径及旧元数据，继续处理其他路径。
- `PreferSrc()`：采用源端版本，包括源端删除。
- `PreferDst()`：采用目标端版本，包括目标端删除。
- `KeepBoth()`：保留双方版本到不同路径，仅用于双向同步。

目录决策作用于整个子树。`OnConflict(ctx, Conflict)` 可逐路径选择策略；`Conflict.Source` / `Target` 为当前信息，`SourceState` / `TargetState` 为历史信息，nil 表示不存在，均只读。回调错误在写入前终止。

重试、事件和部分成功保存遵循上述契约。下一轮按实际文件重新比较，未确认或不完整的写入可能需要冲突策略处理。


[返回文档首页](./_index.md) · [English](../en/syncer.md) · [内部设计](../../design/syncer.md)

## 保留双方版本

双向同步可将 `OnConflict` 设置为 `KeepBoth()`；自定义回调也可返回 `ConflictKeepBoth`：

```go
option := syncer.Option{
    Mode: syncer.ModeTwoWay,
    StateStore:     store,
    OnConflict: syncer.KeepBoth(),
}
```

- 两个文件冲突：源版本保留原路径，目标版本另存到两端的冲突副本。
- 文件与目录冲突：目录保留原路径，文件另存到两端的冲突副本。
- 删除与修改冲突：恢复仍存在的版本，包含目录子树。

副本位于同一父目录，例如 `report.txt` 对应 `report.conflict.txt`；重名依次使用 `report.conflict-2.txt`、`report.conflict-3.txt`。检查两端当前路径、历史状态和本轮已分配名称，避免覆盖已有条目。`Skipper` 也检查拟生成的副本路径；副本被过滤时，返回 `ErrSyncConflict`，整轮不写入。

先将保留版本重命名为同目录下的备份，随后在复制阶段同步备份和原路径，两次复制可以并发执行。重命名失败（包括被 `OnError` 忽略）会阻止该冲突路径的后续操作。备份复制失败时，本端备份仍保留，原路径可能已同步；忽略该复制错误不会阻止原路径同步。已完成操作在结束时保存状态。重命名需要驱动支持 `Rename`，不支持时返回 `ErrNotSupport`。复制失败可能留下半文件，整个流程不提供事务回滚。

预演会列出重命名和复制操作。`TaskEvent`、`ProgressEvent` 的 `FileInfo` 描述当前任务的对象，备份复制描述重命名后的文件和备份路径；重命名事件的 `NewPath` 给出相对于同步根目录的重命名目标路径。`Reverse` 表示写入端。`OpRename` 不发送进度事件，跨端复制报告进度并支持操作重试。重命名不计入删除限制。

该策略仅适用于 `ModeTwoWay`；单向 `Sync` 遇到该冲突决策时在写入前拒绝，不会反写源端。

## 复制进度

单向和双向同步均通过 `OnEvent` 发送 `ProgressEvent`：

```go
OnEvent: func(notification syncer.Event) {
    progress, ok := notification.(syncer.ProgressEvent)
    if !ok {
        return
    }
    info := progress.FileInfo
    log.Printf("%s: %d/%d bytes",
        info.Path(), progress.Written, info.Size())
},
```

`FileInfo` 为读取端在当前任务路径的执行前快照；`Reverse` 表示写入原始源端。`Written` 是本次尝试被目标流接受的字节数，发送端 `Size()` 是总量，负值表示未知。每次成功打开两端流后先报告 0，再随写入递增；重试时进度从 0 重新计数。空文件也报告 0，重命名、未变化和被跳过文件不报告进度。规划阶段的哈希读取不计入传输进度。

回调同步执行，不返回错误，不得修改文件系统；可取消调用方上下文来停止复制。进度达到总量不代表关闭、目标确认或状态保存成功，应以任务事件和最终返回值为准。进度不增加扫描或文件读取，不写入持久状态。
