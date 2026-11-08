package syncer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	stdpath "path"
	"sort"
	"strings"
	"sync"

	"github.com/honmaple/cloudfs/utils/ioutil"
	"golang.org/x/sync/errgroup"
)

// checkDeleteLimits validates the complete plan before execution or preview.
func checkDeleteLimits(tasks []syncTask, src, dst *syncTree, maxDelete, maxPercent int) error {
	if maxDelete == 0 && maxPercent == 0 {
		return nil
	}
	var removed [2][]string
	for _, task := range tasks {
		if task.op == OpRemove || task.op == OpRmdir {
			if task.reverse {
				removed[0] = append(removed[0], task.path)
			} else {
				removed[1] = append(removed[1], task.path)
			}
		}
	}
	counts := [2]int{countDeletedEntries(src, removed[0]), countDeletedEntries(dst, removed[1])}
	totals := [2]int{len(src.entries), len(dst.entries)}
	if maxDelete > 0 && counts[0]+counts[1] > maxDelete {
		return fmt.Errorf("%w: %d entries, limit %d", ErrTooManyDeletes, counts[0]+counts[1], maxDelete)
	}
	if maxPercent > 0 {
		for side, count := range counts {
			if count*100 > maxPercent*totals[side] {
				name := "source"
				if side == 1 {
					name = "target"
				}
				return fmt.Errorf("%w: %d of %d %s entries, limit %d%%", ErrTooManyDeletes, count, totals[side], name, maxPercent)
			}
		}
	}
	return nil
}

// countDeletedEntries counts entries under removal roots. Planning prunes
// removed directories, so roots on the same side do not overlap.
func countDeletedEntries(tree *syncTree, paths []string) int {
	count := 0
	for _, path := range paths {
		tree.walkSubtree(path, func(string, *entry) error {
			count++
			return nil
		})
	}
	return count
}

type (
	// syncTask exists only during this call; it is never persisted or resumed.
	syncTask struct {
		op      Op
		path    string
		newPath string
		record  *Record
		replace bool // Remove a type conflict before creating its replacement.
		// Planning resolves operation dependencies.
		root    string
		reverse bool // Write to the original source instead of the target.
	}
	taskResult struct {
		err    error
		task   *syncTask // Set only on success, including removals.
		record *Record
	}
	// executor owns the filesystems, callbacks and execution state
	// shared by all tasks in one plan.
	executor struct {
		srcFS, dstFS *syncFS
		option       Option

		mu      sync.Mutex
		skipped map[string]bool
	}
)

// groupTasks orders preparation, writes, and final deletions for execution and preview.
func (e *executor) groupTasks(tasks []syncTask) [][]syncTask {
	var renames, replacements, mkdirs, copies, removes, rmdirs []syncTask
	for _, task := range tasks {
		switch {
		case task.op == OpRename:
			renames = append(renames, task)
		case task.replace:
			replacements = append(replacements, task)
		case task.op == OpMkdir:
			mkdirs = append(mkdirs, task)
		case task.op == OpCopy:
			copies = append(copies, task)
		case task.op == OpRemove:
			removes = append(removes, task)
		case task.op == OpRmdir:
			rmdirs = append(rmdirs, task)
		}
	}
	sort.SliceStable(mkdirs, func(i, j int) bool {
		return strings.Count(mkdirs[i].path, "/") < strings.Count(mkdirs[j].path, "/")
	})
	sort.SliceStable(rmdirs, func(i, j int) bool {
		return strings.Count(rmdirs[i].path, "/") > strings.Count(rmdirs[j].path, "/")
	})
	return [][]syncTask{renames, replacements, mkdirs, copies, removes, rmdirs}
}

func (e *executor) dryRun(ctx context.Context, tasks []syncTask) error {
	for _, group := range e.groupTasks(tasks) {
		for index := range group {
			if err := ctx.Err(); err != nil {
				return err
			}
			e.notify(&group[index], StatusPlanned, nil)
		}
	}
	return ctx.Err()
}

// run completes each operation stage before starting the next. Copies and final
// file removals share a continuously replenished concurrency limit.
func (e *executor) run(ctx context.Context, tasks []syncTask) ([]taskResult, error) {
	e.skipped = make(map[string]bool)
	results := make([]taskResult, 0, len(tasks))
	for _, group := range e.groupTasks(tasks) {
		if len(group) == 0 {
			continue
		}
		concurrency := 1
		if group[0].op == OpCopy || group[0].op == OpRemove && !group[0].replace {
			concurrency = max(1, e.option.Concurrency)
		}
		start := len(results)
		results = results[:start+len(group)]
		if err := e.parallel(ctx, group, results[start:], concurrency); err != nil {
			return results, err
		}
	}
	return results, ctx.Err()
}

// parallel stops dispatching on error or cancellation and waits for active tasks.
func (e *executor) parallel(ctx context.Context, tasks []syncTask, results []taskResult, concurrency int) error {
	eg, dispatchCtx := errgroup.WithContext(ctx)
	eg.SetLimit(concurrency)

	for index := range tasks {
		if dispatchCtx.Err() != nil {
			break
		}
		eg.Go(func() error {
			// Go may wait for a slot; check dispatch cancellation after acquiring it.
			if dispatchCtx.Err() != nil {
				return nil
			}
			// Keep active operations on the caller context so another failure only
			// stops new tasks, without canceling operations already in progress.
			results[index] = e.execute(ctx, &tasks[index])
			return results[index].err
		})
	}
	resultErr := eg.Wait()
	for _, result := range results {
		if result.err != nil && !errors.Is(resultErr, result.err) {
			resultErr = errors.Join(resultErr, result.err)
		}
	}
	if err := ctx.Err(); err != nil && !errors.Is(resultErr, err) {
		resultErr = errors.Join(resultErr, err)
	}
	return resultErr
}

func (e *executor) execute(ctx context.Context, task *syncTask) taskResult {
	e.mu.Lock()
	blocked := e.skipped[task.root]
	for path := task.path; path != "."; path = stdpath.Dir(path) {
		if e.skipped[path] {
			blocked = true
			break
		}
	}
	e.mu.Unlock()
	if blocked {
		e.notify(task, StatusSkipped, nil)
		return taskResult{}
	}
	e.notify(task, StatusStarted, nil)
	record, err := e.apply(ctx, task)
	status := StatusSucceeded
	if errors.Is(err, errSkipOperation) {
		status = StatusSkipped
	} else if err != nil {
		status = StatusFailed
	}
	e.notify(task, status, err)
	switch status {
	case StatusSucceeded:
		if task.op == OpRename {
			*task.record = *record
		}
		return taskResult{task: task, record: record}
	case StatusSkipped:
		e.mu.Lock()
		path := task.path
		if task.root != "" && (task.op == OpRemove || task.op == OpRmdir || task.op == OpRename) {
			path = task.root
		}
		e.skipped[path] = true
		e.mu.Unlock()
		return taskResult{}
	default:
		return taskResult{err: err}
	}
}

// apply performs one task and returns the confirmed target record.
// A successful removal returns a nil record.
func (e *executor) apply(ctx context.Context, task *syncTask) (*Record, error) {
	dstFS := e.dstFS
	if task.reverse {
		dstFS = e.srcFS
	}
	switch task.op {
	case OpCopy:
		var copiedHash string
		if err := e.handleError(ctx, task, func(ctx context.Context) error {
			var err error
			copiedHash, err = e.copy(ctx, task)
			return err
		}); err != nil {
			return nil, err
		}
		info, err := dstFS.Stat(ctx, task.path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() || (task.record.Size >= 0 && info.Size() >= 0 && info.Size() != task.record.Size) {
			return nil, fmt.Errorf("%w: invalid transfer result for /%s", ErrSyncConflict, task.path)
		}
		return &Record{Size: info.Size(), ModTime: info.ModTime().UTC(), Hash: copiedHash}, nil
	case OpRename:
		if err := e.handleError(ctx, task, func(ctx context.Context) error {
			if err := dstFS.checkSnapshot(ctx, task.path, task.record); err != nil {
				return err
			}
			return dstFS.Rename(ctx, task.path, stdpath.Base(task.newPath))
		}); err != nil {
			return nil, err
		}
		info, err := dstFS.Stat(ctx, task.newPath)
		if err != nil {
			return nil, err
		}
		return &Record{Size: info.Size(), ModTime: info.ModTime().UTC(), Hash: task.record.Hash}, nil
	case OpMkdir:
		if err := e.handleError(ctx, task, func(ctx context.Context) error {
			return dstFS.MakeDir(ctx, task.path)
		}); err != nil {
			return nil, err
		}
		return &Record{IsDir: true}, nil
	case OpRemove, OpRmdir:
		if err := e.handleError(ctx, task, func(ctx context.Context) error {
			return dstFS.Remove(ctx, task.path)
		}); err != nil {
			return nil, err
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("sync: invalid task op %q", task.op)
	}
}

func (e *executor) copy(ctx context.Context, task *syncTask) (string, error) {
	srcFS, dstFS := e.srcFS, e.dstFS
	if task.reverse {
		srcFS, dstFS = e.dstFS, e.srcFS
	}
	if err := srcFS.checkSnapshot(ctx, task.path, task.record); err != nil {
		return "", err
	}
	reader, err := srcFS.Open(ctx, task.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errors.Join(ErrSourceChanged, err)
		}
		return "", err
	}

	writer, err := dstFS.Create(ctx, task.path)
	if err != nil {
		return "", errors.Join(err, reader.Close())
	}

	var progress func(int64)
	if e.option.OnEvent != nil {
		event := ProgressEvent{
			FileInfo: task.record.FileInfo(task.path),
			Reverse:  task.reverse,
		}
		progress = func(written int64) {
			event.Written = written
			e.option.OnEvent(event)
		}
	}

	hasher := srcFS.algorithm.New()
	n, copyErr := ioutil.Copy(ctx, io.MultiWriter(writer, hasher), reader, progress)
	// Finalize both streams before confirming the transfer, including on failure.
	err = errors.Join(copyErr, writer.Close(), reader.Close())
	if copyErr != nil {
		return "", err
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	err = errors.Join(err, srcFS.checkSnapshot(ctx, task.path, task.record))
	if task.record.Size >= 0 && n != task.record.Size {
		err = errors.Join(err, io.ErrUnexpectedEOF)
	}
	if task.record.Hash != "" && task.record.Hash != sum && !errors.Is(err, ErrSourceChanged) {
		err = errors.Join(err, ErrSourceChanged)
	}
	if err != nil {
		return "", err
	}
	return sum, nil
}

func (e *executor) notify(task *syncTask, status Status, err error) {
	if e.option.OnEvent == nil {
		return
	}
	if status == StatusSkipped {
		err = nil
	}
	e.option.OnEvent(TaskEvent{
		FileInfo: task.record.FileInfo(task.path),
		Op:       task.op,
		Status:   status,
		Reverse:  task.reverse,
		NewPath:  task.newPath,
		Err:      err,
	})
}

func (e *executor) handleError(ctx context.Context, task *syncTask, fn func(context.Context) error) error {
	for retries := 0; ; retries++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn(ctx)
		if err == nil || e.option.OnError == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSyncConflict) {
			return err
		}
		failure := FileError{
			FileInfo: task.record.FileInfo(task.path),
			Op:       task.op,
			Reverse:  task.reverse,
			NewPath:  task.newPath,
			Attempt:  retries + 1,
			Err:      err,
		}
		policy, handlerErr := e.option.OnError(ctx, failure)
		if handlerErr != nil {
			return handlerErr
		}
		switch policy {
		case ErrorAbort:
			return err
		case ErrorSkip:
			return errSkipOperation
		case ErrorRetry:
			if retries >= e.option.MaxRetry {
				return err
			}
			continue
		default:
			return fmt.Errorf("sync: invalid error policy %d", policy)
		}
	}
}
