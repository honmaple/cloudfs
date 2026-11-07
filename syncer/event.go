package syncer

import "fmt"

// Op identifies a filesystem operation performed by synchronization.
type Op uint8

const (
	// OpCopy copies a file.
	OpCopy Op = iota
	// OpRename renames a file on the receiving side.
	OpRename
	// OpMkdir creates a directory.
	OpMkdir
	// OpRemove removes a file.
	OpRemove
	// OpRmdir removes a directory and its contents in one filesystem operation.
	OpRmdir
)

// String returns the operation name for logging.
func (op Op) String() string {
	switch op {
	case OpCopy:
		return "copy"
	case OpRename:
		return "rename"
	case OpMkdir:
		return "mkdir"
	case OpRemove:
		return "remove"
	case OpRmdir:
		return "rmdir"
	default:
		return fmt.Sprintf("Op(%d)", op)
	}
}

// Phase identifies a stage of the synchronization process.
type Phase uint8

const (
	// PhaseScan loads previous state, scans both filesystems, and applies path filters.
	PhaseScan Phase = iota
	// PhasePlan compares entries and generates tasks.
	PhasePlan
	// PhaseExecute executes tasks or reports them during a dry run.
	PhaseExecute
	// PhaseFinish reconciles and saves confirmed state. Dry runs omit this phase.
	PhaseFinish
)

// String returns the phase name for logging.
func (phase Phase) String() string {
	switch phase {
	case PhaseScan:
		return "scan"
	case PhasePlan:
		return "plan"
	case PhaseExecute:
		return "execute"
	case PhaseFinish:
		return "finish"
	default:
		return fmt.Sprintf("Phase(%d)", phase)
	}
}

// Status describes an execution event, not persisted synchronization state.
type Status uint8

const (
	// StatusStarted is emitted before a task or synchronization phase.
	StatusStarted Status = iota
	// StatusSucceeded means the task or phase completed successfully.
	StatusSucceeded
	// StatusFailed means execution stopped with an error.
	StatusFailed
	// StatusSkipped means OnError ignored the task or a prerequisite was skipped.
	StatusSkipped
	// StatusPlanned describes an operation in a dry run; nothing was executed.
	StatusPlanned
)

// String returns the event status name for logging.
func (status Status) String() string {
	switch status {
	case StatusStarted:
		return "started"
	case StatusSucceeded:
		return "succeeded"
	case StatusFailed:
		return "failed"
	case StatusSkipped:
		return "skipped"
	case StatusPlanned:
		return "planned"
	default:
		return fmt.Sprintf("Status(%d)", status)
	}
}

// Event is a synchronization notification. Both modes emit PhaseEvent and
// SyncEvent values; copies also emit ProgressEvent values. Use a type switch to inspect them.
// Event implementations are provided by this package.
type Event interface {
	fmt.Stringer
	event()
}

// baseEvent provides the package-private event marker for built-in events.
type baseEvent struct{}

func (baseEvent) event() {}

// PhaseEvent reports StatusStarted followed by StatusSucceeded or StatusFailed
// for one synchronization phase. Err is set for StatusFailed. Execution failures
// still proceed to PhaseFinish to save confirmed successes.
type PhaseEvent struct {
	baseEvent
	Err    error
	Phase  Phase
	Status Status
}

// String returns the phase, status and any error for logging.
func (event PhaseEvent) String() string {
	message := fmt.Sprintf("phase=%s status=%s", event.Phase, event.Status)
	if event.Err != nil {
		message += fmt.Sprintf(" err=%v", event.Err)
	}
	return message
}

// SyncEvent reports one task in either synchronization mode.
// FileInfo describes the copied source, the directory to create, or the object
// being renamed or removed. Its pre-execution snapshot can be retained.
// Success confirms this task only; state persistence can still fail later.
type SyncEvent struct {
	baseEvent
	Op Op
	// Err is set for StatusFailed; ignored operation errors go to OnError.
	Err      error
	Status   Status
	FileInfo FileInfo
	// NewPath is the absolute rename destination; empty for other operations.
	NewPath string
	// Reverse is false for writes to target and true for writes to source.
	Reverse bool
}

// String returns the operation, status, path, direction and any error for logging.
func (event SyncEvent) String() string {
	message := fmt.Sprintf("op=%s status=%s path=%q reverse=%t", event.Op, event.Status, event.FileInfo.Path(), event.Reverse)
	if event.NewPath != "" {
		message += fmt.Sprintf(" new_path=%q", event.NewPath)
	}
	if event.Err != nil {
		message += fmt.Sprintf(" err=%v", event.Err)
	}
	return message
}

// ProgressEvent reports bytes accepted by the receiving stream during a copy,
// not committed data. Each copy attempt starts with Written == 0.
// FileInfo describes the source at the task path; its Size is the total when known.
// Accessors perform no filesystem I/O, and the snapshot can be retained.
type ProgressEvent struct {
	baseEvent
	FileInfo FileInfo
	Written  int64
	// Reverse has the same meaning as in SyncEvent.
	Reverse bool
}

// String returns the copy path, byte counts and direction for logging.
func (event ProgressEvent) String() string {
	return fmt.Sprintf("progress path=%q written=%d total=%d reverse=%t", event.FileInfo.Path(), event.Written, event.FileInfo.Size(), event.Reverse)
}
