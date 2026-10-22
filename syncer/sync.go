// Package syncer synchronizes cloudfs filesystems in one or both directions.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	stdpath "path"
	"reflect"
	"strings"

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
	// Checksum reads files on demand during comparison,
	// using SHA-256 instead of size/modtime and without reusing historical hashes.
	Checksum bool
	// HashOnChange reuses historical hashes when size and modification time match.
	// Other needed files are hashed during planning. Requires StateStore; Checksum overrides it.
	HashOnChange bool
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
// time; Checksum and HashOnChange explicitly enable content reads. Differing
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
	if opt.HashOnChange && opt.StateStore == nil {
		return fmt.Errorf("%w: HashOnChange requires StateStore", ErrSyncState)
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
	worker := &syncer{
		state: &State{
			Type:    "sync",
			Source:  make(map[string]*Entry),
			Target:  make(map[string]*Entry),
			Version: 1,
		},
		srcFS:            &syncFS{FS: src, option: opt},
		dstFS:            &syncFS{FS: dst, option: opt},
		option:           opt,
		skippedConflicts: make(map[string]bool),
	}
	if opt.Mode == ModeTwoWay {
		worker.state.Type = "bisync"
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

func (s *syncer) isAdded(cur, prev *Entry) bool {
	return cur != nil && prev == nil
}

func (s *syncer) isDeleted(cur, prev *Entry) bool {
	return cur == nil && prev != nil
}

// isChanged compares snapshots without I/O. A newly known hash cannot confirm
// unchanged content against a historical snapshot without a hash.
func (s *syncer) isChanged(cur, prev *Entry) bool {
	return !cur.Equal(prev) || cur != nil && cur.Hash != "" && prev.Hash == ""
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
	if loaded.Type != s.state.Type || !loaded.Valid() {
		return fmt.Errorf("%w: incompatible synchronization state", ErrSyncState)
	}
	s.state = loaded.withPathPrefix("/")
	return nil
}

func (s *syncer) finish(ctx context.Context, state *State) error {
	if s.option.StateStore == nil {
		return nil
	}
	prevState := s.state
	srcTree, dstTree := s.srcTree, s.dstTree
	skip := func(path string) bool {
		return srcTree.isSkipped(path, false) || dstTree.isSkipped(path, false) || s.skippedConflicts[path]
	}
	preserveHash := func(entry, previous *Entry) *Entry {
		if entry.Hash == "" && previous != nil && previous.Hash != "" && entry.Equal(previous) {
			copiedEntry := *entry
			copiedEntry.Hash = previous.Hash
			return &copiedEntry
		}
		return entry
	}
	srcTree.walk(func(path string, entry *Entry) error {
		if skip(path) {
			if entry.IsDir {
				return fs.SkipDir
			}
			return nil
		}
		dst := dstTree.get(path)
		if dst == nil {
			return nil
		}
		sourceEntry, targetEntry := state.Source[path], state.Target[path]
		if !entry.Equal(dst) {
			if s.isChanged(entry, sourceEntry) {
				return nil
			}
			if s.isChanged(dst, targetEntry) {
				return nil
			}
		}
		state.Source[path], state.Target[path] = preserveHash(entry, sourceEntry), preserveHash(dst, targetEntry)
		return nil
	})
	// Traverse the original baselines so excluded directories prune all their
	// historical descendants. The candidate may already contain type changes.
	for side, previous := range []map[string]*Entry{prevState.Source, prevState.Target} {
		entries := state.Source
		if side == 1 {
			entries = state.Target
		}
		newSyncTree(previous).walk(func(path string, entry *Entry) error {
			if skip(path) {
				if entry.IsDir {
					return fs.SkipDir
				}
				return nil
			}
			if srcTree.get(path) == nil && dstTree.get(path) == nil {
				delete(entries, path)
			}
			return nil
		})
	}
	if reflect.DeepEqual(prevState.Source, state.Source) && reflect.DeepEqual(prevState.Target, state.Target) {
		return nil
	}
	return state.withPathPrefix("").save(ctx, s.option.StateStore)
}

func (s *syncer) run(ctx context.Context) error {
	s.notifyPhase(PhaseScan, StatusStarted, nil)
	srcTree, err := s.srcFS.scan(ctx)
	if err != nil {
		s.notifyPhase(PhaseScan, StatusFailed, err)
		return err
	}
	dstTree, err := s.dstFS.scan(ctx)
	if err != nil {
		s.notifyPhase(PhaseScan, StatusFailed, err)
		return err
	}
	s.srcTree = srcTree
	s.dstTree = dstTree
	s.notifyPhase(PhaseScan, StatusSucceeded, nil)

	s.notifyPhase(PhasePlan, StatusStarted, nil)
	if err := s.init(ctx); err != nil {
		s.notifyPhase(PhasePlan, StatusFailed, err)
		return err
	}
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
	state := &State{
		Type:    s.state.Type,
		Version: s.state.Version,
		Source:  maps.Clone(s.state.Source),
		Target:  maps.Clone(s.state.Target),
	}
	exec.state = state
	syncErr := exec.run(ctx, tasks)
	s.notifyPhase(PhaseExecute, StatusSucceeded, syncErr)
	// Save confirmed successes even after cancellation; no further tasks execute.
	s.notifyPhase(PhaseFinish, StatusStarted, nil)
	finishErr := s.finish(ctx, state)
	s.notifyPhase(PhaseFinish, StatusSucceeded, finishErr)
	if syncErr == nil && !errors.Is(finishErr, ctx.Err()) {
		syncErr = ctx.Err()
	}
	return errors.Join(syncErr, finishErr)
}

func (s *syncer) oneWayTasks(ctx context.Context) ([]syncTask, error) {
	tasks := make([]syncTask, 0)
	if err := s.srcTree.walk(func(path string, entry *Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.skippedConflicts[path] || s.dstTree.isSkipped(path, false) {
			if entry.IsDir {
				return fs.SkipDir
			}
			return nil
		}
		src, dst := entry, s.dstTree.get(path)
		var (
			err    error
			policy = ConflictPreferSrc
		)
		switch {
		case dst == nil:
		case src.IsDir != dst.IsDir:
			if dst.IsDir && s.dstTree.isSkipped(path, true) {
				s.skippedConflicts[path] = true
				return nil
			}
			policy, err = s.handleConflict(ctx, path, src, dst)
		case src.IsDir:
			return nil
		default:
			src, err = s.srcFS.readHash(ctx, s.srcTree, path, s.state.Source[path])
			if err != nil {
				return err
			}
			dst, err = s.dstFS.readHash(ctx, s.dstTree, path, s.state.Target[path])
			if err != nil {
				return err
			}
			if src.Equal(dst) || !s.isChanged(src, s.state.Source[path]) && !s.isChanged(dst, s.state.Target[path]) {
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
			if entry.IsDir {
				return fs.SkipDir
			}
			return nil
		default:
			if dst != nil && src.IsDir != dst.IsDir {
				return fmt.Errorf("%w: %s: file/directory mismatch", ErrSyncConflict, path)
			}
			return fmt.Errorf("%w: %s", ErrSyncConflict, path)
		}
		if dst != nil && src.IsDir != dst.IsDir {
			op := OpRemove
			if dst.IsDir {
				op = OpRmdir
			}
			tasks = append(tasks, syncTask{
				op:      op,
				path:    path,
				entry:   dst,
				replace: true,
				reverse: false,
			})
		}
		op := OpCopy
		if src.IsDir {
			op = OpMkdir
		}
		tasks = append(tasks, syncTask{
			op:      op,
			path:    path,
			entry:   src,
			reverse: false,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	if s.option.Delete {
		if err := s.dstTree.walk(func(path string, entry *Entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if s.skippedConflicts[path] || s.srcTree.isSkipped(path, false) {
				if entry.IsDir {
					return fs.SkipDir
				}
				return nil
			}
			if src := s.srcTree.get(path); src != nil {
				// Prune obsolete descendants when a file replaced the target directory.
				if entry.IsDir && !src.IsDir {
					return fs.SkipDir
				}
				return nil
			}
			if entry.IsDir && s.dstTree.isSkipped(path, true) {
				s.skippedConflicts[path] = true
				return fs.SkipDir
			}
			op := OpRemove
			if entry.IsDir {
				op = OpRmdir
			}
			tasks = append(tasks, syncTask{
				op:      op,
				path:    path,
				entry:   entry,
				reverse: false,
			})
			if entry.IsDir {
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
	sourceState := newSyncTree(s.state.Source)
	targetState := newSyncTree(s.state.Target)
	// Resolve deletions against the surviving subtree. Other conflicts retain
	// the caller's policy.
	onConflict := func(path string, src, dst *Entry) (ConflictPolicy, error) {
		if s.isDeleted(dst, s.state.Target[path]) || s.isDeleted(src, s.state.Source[path]) {
			current, previous, fsys := s.srcTree, sourceState, s.srcFS
			policy := ConflictPreferDst
			if src == nil {
				current, previous, fsys = s.dstTree, targetState, s.dstFS
				policy = ConflictPreferSrc
			}
			if (src != nil && src.IsDir || dst != nil && dst.IsDir) && current.isSkipped(path, true) {
				return ConflictSkip, nil
			}
			changed := false
			compare := func(childPath string, _ *Entry) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				cur, prev := current.get(childPath), previous.get(childPath)
				if cur != nil && prev != nil && !cur.IsDir && !prev.IsDir {
					var err error
					cur, err = fsys.readHash(ctx, current, childPath, prev)
					if err != nil {
						return err
					}
				}
				changed = s.isChanged(cur, prev)
				if changed {
					return fs.SkipAll
				}
				return nil
			}
			for _, tree := range []*syncTree{current, previous} {
				if err := tree.walkSubtree(path, compare); err != nil {
					return ConflictAbort, err
				}
				if changed {
					return s.handleConflict(ctx, path, s.srcTree.get(path), s.dstTree.get(path))
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
			if dst != nil && dst.IsDir && s.dstTree.isSkipped(path, true) {
				return ConflictSkip, nil
			}
		case ConflictPreferDst:
			if src != nil && src.IsDir && s.srcTree.isSkipped(path, true) {
				return ConflictSkip, nil
			}
		}
		return policy, nil
	}
	for _, tree := range []*syncTree{s.srcTree, s.dstTree} {
		if err := tree.walk(func(path string, entry *Entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			var src, dst *Entry
			if tree == s.srcTree {
				src, dst = entry, s.dstTree.get(path)
			} else {
				src, dst = s.srcTree.get(path), entry
			}
			if s.skippedConflicts[path] ||
				src == nil && s.srcTree.isSkipped(path, false) ||
				dst == nil && s.dstTree.isSkipped(path, false) {
				if entry.IsDir {
					return fs.SkipDir
				}
				return nil
			}
			// Shared paths were already planned during the source traversal.
			if tree == s.dstTree && src != nil {
				if entry.IsDir && !src.IsDir {
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
			case src.IsDir != dst.IsDir:
				policy, err = onConflict(path, src, dst)
			case src.IsDir:
				return nil
			default:
				src, err = s.srcFS.readHash(ctx, s.srcTree, path, s.state.Source[path])
				if err != nil {
					return err
				}
				dst, err = s.dstFS.readHash(ctx, s.dstTree, path, s.state.Target[path])
				if err != nil {
					return err
				}
				if src.Equal(dst) {
					return nil
				}
				if !s.isChanged(src, s.state.Source[path]) {
					if !s.isChanged(dst, s.state.Target[path]) {
						return nil
					}
					policy = ConflictPreferDst
				} else if s.isChanged(dst, s.state.Target[path]) {
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
				if entry.IsDir {
					return fs.SkipDir
				}
				return nil
			case ConflictAbort:
				return fmt.Errorf("%w: %s", ErrSyncConflict, path)
			default:
				return fmt.Errorf("sync: invalid conflict policy %d for %s", policy, path)
			}

			if dst != nil && (src == nil || src.IsDir != dst.IsDir) {
				op := OpRemove
				if dst.IsDir {
					op = OpRmdir
				}
				tasks = append(tasks, syncTask{
					op:      op,
					path:    path,
					entry:   dst,
					replace: src != nil,
					root:    path,
					reverse: reverse,
				})
			}
			if src != nil {
				if !src.IsDir {
					tasks = append(tasks, syncTask{
						op:      OpCopy,
						path:    path,
						entry:   srcTree.get(path),
						root:    path,
						reverse: reverse,
					})
				} else if err := srcTree.walkSubtree(path, func(childPath string, child *Entry) error {
					op := OpCopy
					if child.IsDir {
						op = OpMkdir
					}
					tasks = append(tasks, syncTask{
						op:      op,
						path:    childPath,
						entry:   child,
						root:    path,
						reverse: reverse,
					})
					return ctx.Err()
				}); err != nil {
					return err
				}
			}
			if entry.IsDir {
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
func (s *syncer) keepBothTasks(path string, src, dst *Entry, taken map[string]bool) ([]syncTask, ConflictPolicy, error) {
	switch {
	case src == nil:
		return nil, ConflictPreferDst, nil
	case dst == nil:
		return nil, ConflictPreferSrc, nil
	}
	file, reverse, policy := s.dstTree.get(path), false, ConflictPreferSrc
	if !src.IsDir && dst.IsDir {
		file, reverse, policy = s.srcTree.get(path), true, ConflictPreferDst
	}
	backup := s.conflictName(path, taken)
	if s.option.Skipper != nil {
		info := *file
		info.Hash = ""
		if s.option.Skipper(info.FileInfo(backup)) {
			return nil, ConflictAbort, fmt.Errorf("%w: conflict copy is filtered: %s", ErrSyncConflict, backup)
		}
	}
	// These tasks share an owned snapshot, updated after the rename succeeds.
	backupEntry := *file
	return []syncTask{
		{
			op:      OpRename,
			path:    path,
			newPath: backup,
			entry:   &backupEntry,
			root:    path,
			reverse: reverse,
		},
		{
			op:      OpCopy,
			path:    backup,
			entry:   &backupEntry,
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

func (s *syncer) handleConflict(ctx context.Context, path string, src, dst *Entry) (ConflictPolicy, error) {
	conflict := Conflict{
		Source:      src.FileInfo(path),
		Target:      dst.FileInfo(path),
		SourceState: s.state.Source[path].FileInfo(path),
		TargetState: s.state.Target[path].FileInfo(path),
	}
	return s.option.OnConflict(ctx, conflict)
}
