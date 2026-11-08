package syncer

import (
	"context"
	"errors"
	"sync"
)

// serialHandlers prevents user callback code from running concurrently.
func serialHandlers(onEvent EventHandler, onError ErrorHandler) (EventHandler, ErrorHandler) {
	var gate sync.Mutex
	var event EventHandler
	if onEvent != nil {
		event = func(notification Event) {
			gate.Lock()
			defer gate.Unlock()
			onEvent(notification)
		}
	}
	var failure ErrorHandler
	if onError != nil {
		failure = func(ctx context.Context, failure FileError) (ErrorPolicy, error) {
			gate.Lock()
			defer gate.Unlock()
			return onError(ctx, failure)
		}
	}
	return event, failure
}

var (
	// ErrSourceChanged means a file no longer matches its scanned snapshot.
	// It is not confirmed in state; a later sync must scan the file again.
	ErrSourceChanged = errors.New("sync source changed")

	// ErrTooManyDeletes means a planned deletion limit was exceeded before writes.
	ErrTooManyDeletes = errors.New("too many sync deletions")

	// ErrSyncConflict indicates incompatible changes. Conflicts detected while
	// planning prevent any planned changes from being applied.
	ErrSyncConflict = errors.New("sync conflict")

	// ErrSyncState indicates missing, incompatible, or invalid synchronization state.
	ErrSyncState = errors.New("invalid sync state")

	errSkipOperation = errors.New("skip sync operation")
)

// FileError describes a failed operation and its expected file snapshot.
type FileError struct {
	FileInfo FileInfo
	Op       Op
	// Reverse is false for writes to target and true for writes to source.
	Reverse bool
	// NewPath is the rename destination relative to the synchronization root.
	// Other operations leave it empty.
	NewPath string
	// Attempt starts at 1 and increases for each retry.
	Attempt int
	Err     error
}

// Error preserves the underlying operation's error text.
func (failure FileError) Error() string {
	return failure.Err.Error()
}

// Unwrap returns the underlying operation error.
func (failure FileError) Unwrap() error {
	return failure.Err
}

// ErrorPolicy is the decision returned by an ErrorHandler.
type ErrorPolicy uint8

const (
	// ErrorAbort stops synchronization with the operation error.
	ErrorAbort ErrorPolicy = iota
	// ErrorSkip skips this operation and continues synchronization.
	ErrorSkip
	// ErrorRetry repeats this operation using the synchronization context, up to MaxRetry.
	ErrorRetry
)

// ConflictPolicy is the decision returned by a ConflictHandler.
type ConflictPolicy uint8

const (
	// ConflictAbort stops synchronization before any writes.
	ConflictAbort ConflictPolicy = iota
	// ConflictSkip leaves the path and its previous metadata unchanged.
	ConflictSkip
	// ConflictPreferSrc uses the source version, including source deletions.
	ConflictPreferSrc
	// ConflictPreferDst uses the target version, including target deletions.
	// ModeCopy and ModeMirror preserve the conflicting subtree without writing back to the source.
	ConflictPreferDst
	// ConflictKeepBoth preserves conflicting versions under separate paths in ModeTwoWay.
	// A surviving version wins over a deletion.
	ConflictKeepBoth
)

// Conflict describes current and previously confirmed file snapshots.
// Each non-nil FileInfo provides the same relative path. Nil means absent.
// Entries are read-only; directory decisions apply to the entire subtree.
type Conflict struct {
	Source, Target           FileInfo
	SourceState, TargetState FileInfo
}

type (
	// Skipper excludes a path when it returns true. It runs synchronously during
	// each scan before hashing file contents; Hash is empty for scanned entries.
	// Excluded directories are not traversed. The root is not passed to Skipper.
	// ConflictKeepBoth also checks proposed copy paths before planning writes.
	// Skipping either side protects both sides and directory descendants, retaining
	// their previous metadata. Parent deletions or replacements affecting excluded
	// descendants are skipped too. The other side is scanned independently, so its
	// metadata may already have been listed. Callbacks must not modify filesystems.
	// Nil includes all paths.
	Skipper func(info FileInfo) bool

	// EventHandler receives SyncEvent, TaskEvent and ProgressEvent values.
	// Task events follow task order by default. Parallel copies preserve order within each task, while
	// different tasks may interleave. User callback code is serialized.
	// Use a type switch to inspect the event. It must not modify
	// the filesystems. Capture the caller context if needed; it has no return value.
	// Each attempted task emits started and one final event after any OnError retries.
	// Copies emit ProgressEvent values between them, including retry attempts.
	// Tasks blocked by an ignored prerequisite emit only skipped. Unchanged entries,
	// conflicts skipped during comparison, and tasks abandoned on termination emit
	// no task events. DryRun task events use StatusPlanned; phase events still
	// report phase starts and completions.
	// SyncEvent reports scanning, planning, execution and state finalization.
	EventHandler func(Event)

	// ErrorHandler selects a policy after each failed copy (including stream
	// closure), rename, directory creation, or removal. A nil OnError aborts.
	// FileError describes the failed attempt. ErrorRetry repeats the operation
	// with the same snapshot and synchronization context; the handler is called
	// again if it fails. MaxRetry bounds retries; the handler may select fewer
	// retries or use cancellation-aware delays.
	// Returning an error stops synchronization with that error, regardless of policy.
	// Scanning, target metadata confirmation, state persistence, cancellation errors,
	// and execution conflicts bypass the handler. It must not modify filesystems.
	ErrorHandler func(context.Context, FileError) (ErrorPolicy, error)

	// ConflictHandler selects a policy for each conflicting path during comparison.
	// A nil OnConflict option defaults to Abort().
	// It must not modify entries or filesystems. Errors stop the call before writes.
	ConflictHandler func(context.Context, Conflict) (ConflictPolicy, error)
)

func fixedConflict(policy ConflictPolicy) ConflictHandler {
	return func(context.Context, Conflict) (ConflictPolicy, error) {
		return policy, nil
	}
}

// Abort stops synchronization before writes when a conflict is found.
// It is the default handler when OnConflict is nil.
func Abort() ConflictHandler {
	return fixedConflict(ConflictAbort)
}

// Skip leaves conflicting paths and their previous state unchanged.
func Skip() ConflictHandler {
	return fixedConflict(ConflictSkip)
}

// PreferSrc selects the source version, including source deletions.
func PreferSrc() ConflictHandler {
	return fixedConflict(ConflictPreferSrc)
}

// PreferDst selects the target version, including target deletions.
// ModeCopy and ModeMirror preserve the conflicting path without writing back to the source.
func PreferDst() ConflictHandler {
	return fixedConflict(ConflictPreferDst)
}

// KeepBoth preserves conflicting versions at separate paths in ModeTwoWay.
// ModeCopy and ModeMirror reject this decision when a conflict is found.
func KeepBoth() ConflictHandler {
	return fixedConflict(ConflictKeepBoth)
}

// PreferNewer selects the existing file with the later modification time.
// A surviving entry wins over deletion; equal known times prefer the source.
// If both entries exist and either is a directory or has an unknown time, it
// selects ConflictAbort because those entries cannot be ordered reliably.
func PreferNewer() ConflictHandler {
	return func(_ context.Context, conflict Conflict) (ConflictPolicy, error) {
		source, target := conflict.Source, conflict.Target
		switch {
		case source == nil && target == nil:
			return ConflictAbort, nil
		case source == nil:
			return ConflictPreferDst, nil
		case target == nil:
			return ConflictPreferSrc, nil
		case source.IsDir() || target.IsDir() || source.ModTime().IsZero() || target.ModTime().IsZero():
			return ConflictAbort, nil
		case target.ModTime().After(source.ModTime()):
			return ConflictPreferDst, nil
		default:
			return ConflictPreferSrc, nil
		}
	}
}
