// Package syncer synchronizes cloudfs filesystems in one or both directions.
package syncer

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	stdpath "path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/honmaple/cloudfs"
)

// Mode selects the synchronization direction.
type Mode uint8

const (
	// ModeOneWay synchronizes source changes to the target.
	ModeOneWay Mode = iota
	// ModeTwoWay propagates changes and deletions in both directions.
	ModeTwoWay
)

// Option configures synchronization. The zero value uses ModeOneWay
// and preserves destination-only entries.
type Option struct {
	// Mode defaults to ModeOneWay.
	Mode Mode
	// DryRun scans and plans normally, emitting StatusPlanned without writes or Save.
	DryRun bool
	// Concurrency limits parallel copies and final file removals. 0 and 1 run serially.
	// Values above 1 require concurrency-safe filesystems and middleware.
	Concurrency int
	// MaxRetry limits retries per operation after ErrorRetry. Values <= 0 default to 3.
	MaxRetry int
	// MaxDelete limits explicitly removed entries across both sides; 0 is unlimited.
	// Recursive removals and type replacements include all removed descendants.
	MaxDelete int
	// MaxDeletePercent limits removals as a percentage of each receiving side's
	// scanned entries, excluding the root. 0 disables the limit; valid range is 0–100.
	MaxDeletePercent int
	Delete           bool // Remove destination-only entries after copying.
	// Checksum compares file contents using hashes. Saved hashes are reused
	// when the file size and modification time match the historical record.
	Checksum bool
	// StateStore enables loading and saving confirmed state when non-nil.
	StateStore StateStore

	Skipper    Skipper         // Excludes paths on either side before planning.
	OnEvent    EventHandler    // Observes task execution; nil disables notifications.
	OnError    ErrorHandler    // Nil stops at the first I/O failure.
	OnConflict ConflictHandler // Nil uses Abort().
}

// Sync synchronizes / in each supplied filesystem. Use middleware.PrefixFS
// to scope either filesystem to a subdirectory. It preserves destination-only
// entries by default. Existing files are compared by size and modification
// time unless Checksum enables hash comparison. Differing
// files and type mismatches use OnConflict, defaulting to Abort() and ErrSyncConflict.
// ModeTwoWay requires StateStore.
// Filesystem ownership remains with the caller.
func Sync(ctx context.Context, src, dst cloudfs.FS, options ...Option) error {
	if len(options) > 1 {
		return fmt.Errorf("sync: expected at most one options value")
	}
	var opt Option
	if len(options) == 1 {
		opt = options[0]
	}
	if opt.Mode != ModeOneWay && opt.Mode != ModeTwoWay {
		return fmt.Errorf("sync: invalid mode %d", opt.Mode)
	}
	if opt.OnConflict == nil {
		opt.OnConflict = Abort()
	}
	if opt.MaxDelete < 0 || opt.MaxDeletePercent < 0 || opt.MaxDeletePercent > 100 {
		return fmt.Errorf("sync: invalid deletion limits")
	}
	if opt.Mode == ModeTwoWay && opt.StateStore == nil {
		return fmt.Errorf("%w: ModeTwoWay requires StateStore", ErrSyncState)
	}
	if opt.Concurrency < 0 {
		return fmt.Errorf("sync: invalid concurrency %d", opt.Concurrency)
	}
	if opt.MaxRetry <= 0 {
		opt.MaxRetry = 3
	}
	if opt.Concurrency > 1 {
		opt.OnEvent, opt.OnError = serialHandlers(opt.OnEvent, opt.OnError)
	}
	algorithm := crypto.SHA256
	if source, ok := src.(cloudfs.Hasher); ok {
		if target, ok := dst.(cloudfs.Hasher); ok {
			targetHashes := target.Hashes()
			for _, hash := range source.Hashes() {
				if hash.Available() && slices.Contains(targetHashes, hash) {
					algorithm = hash
					break
				}
			}
		}
	}
	worker := &syncer{
		state: &State{
			HashAlgorithm: algorithm,
			Source:        make(map[string]*Record),
			Target:        make(map[string]*Record),
			Version:       1,
		},
		srcFS:            &syncFS{FS: src, option: opt, algorithm: algorithm},
		dstFS:            &syncFS{FS: dst, option: opt, algorithm: algorithm},
		option:           opt,
		skippedConflicts: make(map[string]bool),
	}
	return worker.run(ctx)
}

type syncer struct {
	srcFS, dstFS     *syncFS
	srcTree, dstTree *syncTree
	state            *State
	option           Option
	skippedConflicts map[string]bool
}

// compare reports whether the two scanned files differ.
func (s *syncer) compare(ctx context.Context, src, dst *entry) (bool, error) {
	if s.option.Checksum {
		if err := src.loadHash(ctx); err != nil {
			return false, err
		}
		if err := dst.loadHash(ctx); err != nil {
			return false, err
		}
	}
	return !src.Equal(dst), nil
}

// isChanged compares a scanned entry with its own historical record.
func (s *syncer) isChanged(ctx context.Context, cur *entry) (bool, error) {
	if s.option.Checksum && cur.record != nil && !cur.IsDir() && !cur.record.IsDir {
		if err := cur.loadHash(ctx); err != nil {
			return false, err
		}
	}
	return cur.isChanged(), nil
}

func (s *syncer) isDeleted(cur *entry, record *Record) bool {
	return cur == nil && record != nil
}

func (s *syncer) init(ctx context.Context) error {
	if s.option.StateStore == nil {
		return nil
	}
	loaded, err := s.option.StateStore.Load(ctx)
	if err != nil {
		return err
	}
	if loaded == nil {
		return nil
	}
	if !loaded.Valid() {
		return fmt.Errorf("%w: incompatible synchronization state", ErrSyncState)
	}
	s.state = loaded
	s.srcFS.algorithm, s.dstFS.algorithm = loaded.HashAlgorithm, loaded.HashAlgorithm
	return nil
}

func (s *syncer) finish(ctx context.Context, results []taskResult) error {
	if s.option.StateStore == nil {
		return nil
	}
	state := &State{
		Source:        maps.Clone(s.state.Source),
		Target:        maps.Clone(s.state.Target),
		Version:       s.state.Version,
		HashAlgorithm: s.state.HashAlgorithm,
	}
	newRecord := func(current *entry) *Record {
		record := current.toRecord()
		if record.Hash == "" && current.record != nil && record.Equal(current.record) {
			record.Hash = current.record.Hash
		}
		return record
	}

	// Refresh pairs that agree or still match their respective baselines.
	s.srcTree.walk(func(path string, src *entry) error {
		if s.dstTree.isSkipped(path, false) || s.skippedConflicts[path] {
			if src.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		dst := s.dstTree.get(path)
		if dst == nil || (!src.Equal(dst) && (src.isChanged() || dst.isChanged())) {
			return nil
		}
		state.Source[path], state.Target[path] = newRecord(src), newRecord(dst)
		return nil
	})

	// Drop history only when both scans confirm absence. A skipped path or
	// ancestor can hide an entry from both scans.
	for _, records := range []map[string]*Record{state.Source, state.Target} {
		for path := range records {
			if s.srcTree.get(path) != nil || s.dstTree.get(path) != nil {
				continue
			}
			ancestor := path
			for ; ancestor != "."; ancestor = stdpath.Dir(ancestor) {
				if s.srcTree.isSkipped(ancestor, false) || s.dstTree.isSkipped(ancestor, false) || s.skippedConflicts[ancestor] {
					break
				}
			}
			if ancestor == "." {
				delete(records, path)
			}
		}
	}

	// Apply execution results last so they take precedence over the scans.
	state.apply(results)

	if reflect.DeepEqual(s.state.Source, state.Source) && reflect.DeepEqual(s.state.Target, state.Target) {
		return nil
	}
	if err := state.UpdateRevision(); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC()
	if ctx.Err() != nil {
		// Give already confirmed operations a bounded chance to reach storage.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
	}
	return s.option.StateStore.Save(ctx, state)
}

func (s *syncer) run(ctx context.Context) error {
	s.notifyPhase(PhaseScan, StatusStarted, nil)
	if err := s.init(ctx); err != nil {
		s.notifyPhase(PhaseScan, StatusFailed, err)
		return err
	}
	srcTree, err := s.srcFS.scan(ctx, s.state.Source)
	if err != nil {
		s.notifyPhase(PhaseScan, StatusFailed, err)
		return err
	}
	dstTree, err := s.dstFS.scan(ctx, s.state.Target)
	if err != nil {
		s.notifyPhase(PhaseScan, StatusFailed, err)
		return err
	}
	s.srcTree = srcTree
	s.dstTree = dstTree
	s.notifyPhase(PhaseScan, StatusSucceeded, nil)

	s.notifyPhase(PhasePlan, StatusStarted, nil)
	var tasks []syncTask
	switch s.option.Mode {
	case ModeOneWay:
		tasks, err = s.oneWayTasks(ctx)
	case ModeTwoWay:
		tasks, err = s.twoWayTasks(ctx)
	}
	if err != nil {
		s.notifyPhase(PhasePlan, StatusFailed, err)
		return err
	}
	s.notifyPhase(PhasePlan, StatusSucceeded, nil)
	exec := &executor{
		srcFS:  s.srcFS,
		dstFS:  s.dstFS,
		option: s.option,
	}
	s.notifyPhase(PhaseExecute, StatusStarted, nil)
	if s.option.DryRun {
		err := exec.dryRun(ctx, tasks)
		s.notifyPhase(PhaseExecute, StatusSucceeded, err)
		return err
	}
	results, syncErr := exec.run(ctx, tasks)
	s.notifyPhase(PhaseExecute, StatusSucceeded, syncErr)
	// Save confirmed successes even after cancellation; no further tasks execute.
	s.notifyPhase(PhaseFinish, StatusStarted, nil)
	finishErr := s.finish(ctx, results)
	s.notifyPhase(PhaseFinish, StatusSucceeded, finishErr)
	if syncErr == nil && !errors.Is(finishErr, ctx.Err()) {
		syncErr = ctx.Err()
	}
	return errors.Join(syncErr, finishErr)
}

func (s *syncer) oneWayTasks(ctx context.Context) ([]syncTask, error) {
	tasks := make([]syncTask, 0)
	if err := s.srcTree.walk(func(path string, src *entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.skippedConflicts[path] || s.dstTree.isSkipped(path, false) {
			if src.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		dst := s.dstTree.get(path)
		var (
			err    error
			policy = ConflictPreferSrc
		)
		switch {
		case dst == nil:
		case src.IsDir() != dst.IsDir():
			if dst.IsDir() && s.dstTree.isSkipped(path, true) {
				s.skippedConflicts[path] = true
				return nil
			}
			policy, err = s.handleConflict(ctx, path, src, dst)
		case src.IsDir():
			return nil
		default:
			if changed, err := s.compare(ctx, src, dst); err != nil || !changed {
				return err
			}
			if !src.isChanged() && !dst.isChanged() {
				return nil
			}
			policy, err = s.handleConflict(ctx, path, src, dst)
		}
		if err != nil {
			return err
		}
		switch policy {
		case ConflictPreferSrc:
		case ConflictPreferDst, ConflictSkip:
			s.skippedConflicts[path] = true
			if src.IsDir() {
				return fs.SkipDir
			}
			return nil
		default:
			if dst != nil && src.IsDir() != dst.IsDir() {
				return fmt.Errorf("%w: /%s: file/directory mismatch", ErrSyncConflict, path)
			}
			return fmt.Errorf("%w: /%s", ErrSyncConflict, path)
		}
		if dst != nil && src.IsDir() != dst.IsDir() {
			op := OpRemove
			if dst.IsDir() {
				op = OpRmdir
			}
			tasks = append(tasks, syncTask{
				op:      op,
				path:    path,
				record:  dst.toRecord(),
				replace: true,
				reverse: false,
			})
		}
		op := OpCopy
		if src.IsDir() {
			op = OpMkdir
		}
		tasks = append(tasks, syncTask{
			op:      op,
			path:    path,
			record:  src.toRecord(),
			reverse: false,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	if s.option.Delete {
		if err := s.dstTree.walk(func(path string, dst *entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if s.skippedConflicts[path] || s.srcTree.isSkipped(path, false) {
				if dst.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if src := s.srcTree.get(path); src != nil {
				// Prune obsolete descendants when a file replaced the target directory.
				if dst.IsDir() && !src.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if dst.IsDir() && s.dstTree.isSkipped(path, true) {
				s.skippedConflicts[path] = true
				return fs.SkipDir
			}
			op := OpRemove
			if dst.IsDir() {
				op = OpRmdir
			}
			tasks = append(tasks, syncTask{
				op:      op,
				path:    path,
				record:  dst.toRecord(),
				reverse: false,
			})
			if dst.IsDir() {
				return fs.SkipDir
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err := checkDeleteLimits(tasks, s.srcTree, s.dstTree, s.option.MaxDelete, s.option.MaxDeletePercent); err != nil {
		return nil, err
	}
	return tasks, nil
}

// twoWayTasks compares source paths first, followed by target-only paths.
func (s *syncer) twoWayTasks(ctx context.Context) ([]syncTask, error) {
	tasks := make([]syncTask, 0)
	taken := make(map[string]bool)
	srcState := stateTree(s.state.Source)
	dstState := stateTree(s.state.Target)
	// Resolve deletions against the surviving subtree. Other conflicts retain
	// the caller's policy.
	onConflict := func(path string, src, dst *entry) (ConflictPolicy, error) {
		if s.isDeleted(src, s.state.Source[path]) || s.isDeleted(dst, s.state.Target[path]) {
			current, previous := s.srcTree, srcState
			policy := ConflictPreferDst
			if src == nil {
				current, previous = s.dstTree, dstState
				policy = ConflictPreferSrc
			}
			if (src != nil && src.IsDir() || dst != nil && dst.IsDir()) && current.isSkipped(path, true) {
				return ConflictSkip, nil
			}
			changed := false
			compareChild := func(childPath string, _ *entry) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if child := current.get(childPath); child == nil {
					changed = true
				} else {
					var err error
					changed, err = s.isChanged(ctx, child)
					if err != nil {
						return err
					}
				}
				if changed {
					return fs.SkipAll
				}
				return nil
			}
			for _, tree := range []*syncTree{current, previous} {
				if err := tree.walkSubtree(path, compareChild); err != nil {
					return ConflictAbort, err
				}
				if changed {
					return s.handleConflict(ctx, path, src, dst)
				}
			}
			return policy, nil
		}
		policy, err := s.handleConflict(ctx, path, src, dst)
		if err != nil {
			return policy, err
		}
		switch policy {
		case ConflictPreferSrc:
			if dst != nil && dst.IsDir() && s.dstTree.isSkipped(path, true) {
				return ConflictSkip, nil
			}
		case ConflictPreferDst:
			if src != nil && src.IsDir() && s.srcTree.isSkipped(path, true) {
				return ConflictSkip, nil
			}
		}
		return policy, nil
	}
	for _, tree := range []*syncTree{s.srcTree, s.dstTree} {
		if err := tree.walk(func(path string, current *entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			var src, dst *entry
			if tree == s.srcTree {
				src, dst = current, s.dstTree.get(path)
			} else {
				src, dst = s.srcTree.get(path), current
			}
			if s.skippedConflicts[path] ||
				src == nil && s.srcTree.isSkipped(path, false) ||
				dst == nil && s.dstTree.isSkipped(path, false) {
				if current.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			// Shared paths were already planned during the source traversal.
			if tree == s.dstTree && src != nil {
				if current.IsDir() && !src.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			var (
				err    error
				policy = ConflictPreferSrc
			)
			switch {
			case src == nil:
				policy = ConflictPreferDst
				if s.isDeleted(src, s.state.Source[path]) {
					policy, err = onConflict(path, src, dst)
				}
			case dst == nil:
				if s.isDeleted(dst, s.state.Target[path]) {
					policy, err = onConflict(path, src, dst)
				}
			case src.IsDir() != dst.IsDir():
				policy, err = onConflict(path, src, dst)
			case src.IsDir():
				return nil
			default:
				if changed, err := s.compare(ctx, src, dst); err != nil || !changed {
					return err
				}
				srcChanged, dstChanged := src.isChanged(), dst.isChanged()
				switch {
				case !srcChanged && !dstChanged:
					return nil
				case !srcChanged:
					policy = ConflictPreferDst
				case dstChanged:
					policy, err = onConflict(path, src, dst)
				}
			}
			if err != nil {
				return err
			}

			var (
				srcTree = s.srcTree
				reverse = false
			)
			switch policy {
			case ConflictKeepBoth:
				var backupTasks []syncTask
				backupTasks, policy, err = s.keepBothTasks(path, src, dst, taken)
				if err != nil {
					return err
				}
				tasks = append(tasks, backupTasks...)
				if policy == ConflictPreferSrc {
					dst = nil
				} else {
					src = nil
				}
				fallthrough
			case ConflictPreferSrc, ConflictPreferDst:
				if policy == ConflictPreferDst {
					src, dst = dst, src
					srcTree = s.dstTree
					reverse = true
				}
			case ConflictSkip:
				s.skippedConflicts[path] = true
				if current.IsDir() {
					return fs.SkipDir
				}
				return nil
			case ConflictAbort:
				return fmt.Errorf("%w: /%s", ErrSyncConflict, path)
			default:
				return fmt.Errorf("sync: invalid conflict policy %d for /%s", policy, path)
			}

			if dst != nil && (src == nil || src.IsDir() != dst.IsDir()) {
				op := OpRemove
				if dst.IsDir() {
					op = OpRmdir
				}
				tasks = append(tasks, syncTask{
					op:      op,
					path:    path,
					record:  dst.toRecord(),
					replace: src != nil,
					root:    path,
					reverse: reverse,
				})
			}
			if src != nil {
				if !src.IsDir() {
					tasks = append(tasks, syncTask{
						op:      OpCopy,
						path:    path,
						record:  src.toRecord(),
						root:    path,
						reverse: reverse,
					})
				} else if err := srcTree.walkSubtree(path, func(childPath string, child *entry) error {
					op := OpCopy
					if child.IsDir() {
						op = OpMkdir
					}
					tasks = append(tasks, syncTask{
						op:      op,
						path:    childPath,
						record:  child.toRecord(),
						root:    path,
						reverse: reverse,
					})
					return ctx.Err()
				}); err != nil {
					return err
				}
			}
			if current.IsDir() {
				return fs.SkipDir
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if err := checkDeleteLimits(tasks, s.srcTree, s.dstTree, s.option.MaxDelete, s.option.MaxDeletePercent); err != nil {
		return nil, err
	}
	return tasks, nil
}

// keepBothTasks plans a rename and a cross-side backup copy. Directories stay
// at the original path in type conflicts.
func (s *syncer) keepBothTasks(path string, src, dst *entry, taken map[string]bool) ([]syncTask, ConflictPolicy, error) {
	switch {
	case src == nil:
		return nil, ConflictPreferDst, nil
	case dst == nil:
		return nil, ConflictPreferSrc, nil
	}
	file, reverse, policy := dst, false, ConflictPreferSrc
	if !src.IsDir() && dst.IsDir() {
		file, reverse, policy = src, true, ConflictPreferDst
	}
	backup := s.conflictName(path, taken)
	if s.option.Skipper != nil {
		backupEntry := *file
		backupEntry.path, backupEntry.hash = backup, ""
		if s.option.Skipper(backupEntry.FileInfo()) {
			return nil, ConflictAbort, fmt.Errorf("%w: conflict copy is filtered: /%s", ErrSyncConflict, backup)
		}
	}
	// These tasks share an owned snapshot, updated after the rename succeeds.
	backupRecord := *file.toRecord()
	return []syncTask{
		{
			op:      OpRename,
			path:    path,
			newPath: backup,
			record:  &backupRecord,
			root:    path,
			reverse: reverse,
		},
		{
			op:      OpCopy,
			path:    backup,
			record:  &backupRecord,
			root:    path,
			reverse: !reverse,
		},
	}, policy, nil
}

// conflictName reserves an unused sibling path for this plan.
func (s *syncer) conflictName(path string, taken map[string]bool) string {
	name, ext := stdpath.Base(path), stdpath.Ext(path)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem, ext = name, ""
	}
	for number := 1; ; number++ {
		suffix := ".conflict"
		if number > 1 {
			suffix = fmt.Sprintf(".conflict-%d", number)
		}
		backup := stdpath.Join(stdpath.Dir(path), stem+suffix+ext)
		if !taken[backup] && s.srcTree.get(backup) == nil && s.dstTree.get(backup) == nil &&
			s.state.Source[backup] == nil && s.state.Target[backup] == nil &&
			!s.srcTree.isSkipped(backup, false) && !s.dstTree.isSkipped(backup, false) {
			taken[backup] = true
			return backup
		}
	}
}

// notifyPhase derives failed completion from err and uses the shared event handler.
func (s *syncer) notifyPhase(phase Phase, status Status, err error) {
	if s.option.OnEvent == nil {
		return
	}
	if err != nil {
		status = StatusFailed
	}
	s.option.OnEvent(PhaseEvent{Phase: phase, Status: status, Err: err})
}

func (s *syncer) handleConflict(ctx context.Context, path string, src, dst *entry) (ConflictPolicy, error) {
	conflict := Conflict{
		Source:      src.FileInfo(),
		Target:      dst.FileInfo(),
		SourceState: s.state.Source[path].FileInfo("/" + path),
		TargetState: s.state.Target[path].FileInfo("/" + path),
	}
	return s.option.OnConflict(ctx, conflict)
}
