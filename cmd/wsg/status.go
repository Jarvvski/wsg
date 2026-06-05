package main

// WorkerStatus is the lifecycle a pool worker traverses: idle -> busy ->
// done|failed, with Reset bringing any state back to idle. The four
// constants are the only values written to disk; encoding/json marshals
// them as the bare lowercase string the wire format (and jj-wsx, the Bun
// TUI that reads worker-N.json) already expects, so this typed seam is
// wire-compatible with the previous bare-string field.
type WorkerStatus string

const (
	WorkerStatusIdle   WorkerStatus = "idle"
	WorkerStatusBusy   WorkerStatus = "busy"
	WorkerStatusDone   WorkerStatus = "done"
	WorkerStatusFailed WorkerStatus = "failed"
)

func (s WorkerStatus) IsTerminal() bool {
	return s == WorkerStatusDone || s == WorkerStatusFailed
}

func (s WorkerStatus) IsActive() bool {
	return s == WorkerStatusBusy
}

// IsRemovable reports whether the worker can be torn down by Pool.Remove
// or Pool.shrink without aborting an in-flight run.
func (s WorkerStatus) IsRemovable() bool {
	return s != WorkerStatusBusy
}

// SubIssueStatus is the lifecycle a sub-issue inside a DispatchGroup
// traverses from initial discovery (pending) through dispatch and a
// terminal state (done, failed, or skipped). The five constants are the
// only values written to dispatch-*.json on disk.
type SubIssueStatus string

const (
	SubIssueStatusPending    SubIssueStatus = "pending"
	SubIssueStatusDispatched SubIssueStatus = "dispatched"
	SubIssueStatusDone       SubIssueStatus = "done"
	SubIssueStatusFailed     SubIssueStatus = "failed"
	SubIssueStatusSkipped    SubIssueStatus = "skipped"
)

func (s SubIssueStatus) IsTerminal() bool {
	return s == SubIssueStatusDone || s == SubIssueStatusFailed || s == SubIssueStatusSkipped
}

func (s SubIssueStatus) IsActive() bool {
	return s == SubIssueStatusPending || s == SubIssueStatusDispatched
}

// Unblocks reports whether this sub-issue's terminal state satisfies a
// downstream dependency. Done and skipped unblock; failed does not.
func (s SubIssueStatus) Unblocks() bool {
	return s == SubIssueStatusDone || s == SubIssueStatusSkipped
}
