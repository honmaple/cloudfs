package syncer

import (
	"context"
	"crypto/sha256"
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
		tree.walkSubtree(path, func(string, *Entry) error {
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
		entry   *Entry
		replace bool // Remove a type conflict before creating its replacement.
		// Planning resolves operation dependencies.
		root    string
		reverse bool // Write to the original source instead of the target.
	}
	// executor owns the filesystems, callbacks and execution state
	// shared by all tasks in one plan.
	executor struct {
		srcFS, dstFS *syncFS
		option       Option
		state        *State

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
func (e *executor) run(ctx context.Context, tasks []syncTask) error {
	e.skipped = make(map[string]bool)
	for _, group := range e.groupTasks(tasks) {
		if len(group) == 0 {
			continue
		}
		concurrency := 1
		if group[0].op == OpCopy || group[0].op == OpRemove && !group[0].replace {
			concurrency = max(1, e.option.Concurrency)
		}
		if err := e.parallel(ctx, group, concurrency); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// parallel stops dispatching on error or cancellation and waits for active tasks.
func (e *executor) parallel(ctx context.Context, tasks []syncTask, concurrency int) error {
	eg, dispatchCtx := errgroup.WithContext(ctx)
	eg.SetLimit(concurrency)

	errs := make([]error, len(tasks))
	for index, task := range tasks {
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
			errs[index] = e.execute(ctx, task)
			return errs[index]
		})
	}
	resultErr := eg.Wait()
	for _, err := range errs {
		if err != nil && !errors.Is(resultErr, err) {
			resultErr = errors.Join(resultErr, err)
		}
	}
	if err := ctx.Err(); err != nil && !errors.Is(resultErr, err) {
		resultErr = errors.Join(resultErr, err)
	}
	return resultErr
}

func (e *executor) execute(ctx context.Context, task syncTask) error {
	e.mu.Lock()
	blocked := e.skipped[task.root]
	for path := task.path; path != "/"; path = stdpath.Dir(path) {
		if e.skipped[path] {
			blocked = true
			break
		}
	}
	e.mu.Unlock()
	if blocked {
		e.notify(&task, StatusSkipped, nil)
		return nil
	}
	e.notify(&task, StatusStarted, nil)
	entry, err := e.apply(ctx, &task)
	status := StatusSucceeded
	if errors.Is(err, errSkipOperation) {
		status = StatusSkipped
	} else if err != nil {
		status = StatusFailed
	}
	if err == nil {
		e.mu.Lock()
		srcState, dstState := e.state.Source, e.state.Target
		if task.reverse {
			srcState, dstState = e.state.Target, e.state.Source
		}
		if task.op == OpRename {
			dstState[task.newPath] = entry
			delete(dstState, task.path)
		} else if entry != nil {
			srcState[task.path] = task.entry.withHash(entry.Hash)
			dstState[task.path] = entry
		} else {
			delete(dstState, task.path)
			if !task.replace {
				delete(srcState, task.path)
			}
			if task.op == OpRmdir {
				// The sending side is absent or a replacement file, so neither
				// side can retain confirmed descendants of the removed directory.
				for _, entries := range []map[string]*Entry{srcState, dstState} {
					for path := range entries {
						if strings.HasPrefix(path, task.path+"/") {
							delete(entries, path)
						}
					}
				}
			}
		}
		e.mu.Unlock()
	}
	e.notify(&task, status, err)
	if err == nil && task.op == OpRename {
		*task.entry = *entry
	}
	if errors.Is(err, errSkipOperation) {
		e.mu.Lock()
		path := task.path
		if task.root != "" && (task.op == OpRemove || task.op == OpRmdir || task.op == OpRename) {
			path = task.root
		}
		e.skipped[path] = true
		e.mu.Unlock()
		return nil
	}
	return err
}

// apply performs one task and returns the confirmed target entry.
// A successful removal returns nil; execute records the result.
func (e *executor) apply(ctx context.Context, task *syncTask) (*Entry, error) {
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
		if info.IsDir() || (task.entry.Size >= 0 && info.Size() >= 0 && info.Size() != task.entry.Size) {
			return nil, fmt.Errorf("%w: invalid transfer result for %s", ErrSyncConflict, task.path)
		}
		return &Entry{Size: info.Size(), ModTime: info.ModTime().UTC(), Hash: copiedHash}, nil
	case OpRename:
		if err := e.handleError(ctx, task, func(ctx context.Context) error {
			if err := dstFS.checkFileSnapshot(ctx, task.path, task.entry); err != nil {
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
		return &Entry{Size: info.Size(), ModTime: info.ModTime().UTC(), Hash: task.entry.Hash}, nil
	case OpMkdir:
		if err := e.handleError(ctx, task, func(ctx context.Context) error {
			return dstFS.MakeDir(ctx, task.path)
		}); err != nil {
			return nil, err
		}
		return &Entry{IsDir: true}, nil
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
	if err := srcFS.checkFileSnapshot(ctx, task.path, task.entry); err != nil {
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
			FileInfo: task.entry.FileInfo(task.path),
			Reverse:  task.reverse,
		}
		progress = func(written int64) {
			event.Written = written
			e.option.OnEvent(event)
		}
	}

	hasher := sha256.New()
	n, copyErr := ioutil.Copy(ctx, io.MultiWriter(writer, hasher), reader, progress)
	// Finalize both streams before confirming the transfer, including on failure.
	err = errors.Join(copyErr, writer.Close(), reader.Close())
	if copyErr != nil {
		return "", err
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	err = errors.Join(err, srcFS.checkFileSnapshot(ctx, task.path, task.entry))
	if task.entry.Size >= 0 && n != task.entry.Size {
		err = errors.Join(err, io.ErrUnexpectedEOF)
	}
	if task.entry.Hash != "" && task.entry.Hash != sum && !errors.Is(err, ErrSourceChanged) {
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
	e.option.OnEvent(SyncEvent{
		FileInfo: task.entry.FileInfo(task.path),
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
			FileInfo: task.entry.FileInfo(task.path),
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
