# Workspace Dispatch

wsg coordinates isolated jj workspaces and reusable workers to turn Linear tickets into changes and pull requests. Its language separates where work happens, what executes it, and how dependent tickets progress.

## Language

### Workspaces

**Repository**:
A jj project whose Workspace registrations and Worker Pool are managed as one unit.
_Avoid_: Current checkout, working directory

**Repository Root**:
The canonical location of a Repository and its Default Workspace, regardless of the Workspace from which wsg is invoked.
_Avoid_: Current Workspace root

**Workspace**:
A named jj working copy registered with a Repository.
_Avoid_: Worktree, checkout

**Default Workspace**:
The protected Workspace at the Repository root. Every Repository has exactly one.
_Avoid_: Primary workspace, root workspace

**Workspace Base Directory**:
The location where wsg expects every non-default Workspace belonging to a Repository.
_Avoid_: Repository Root

**Ad Hoc Workspace**:
A user-created Workspace that is independent of the Worker Pool and cannot receive a Dispatch.
_Avoid_: One-shot workspace, Worker Workspace

**Worker Workspace**:
A Workspace allocated to one Worker and reused across that Worker's Runs.
_Avoid_: Ad Hoc Workspace

**Missing Workspace**:
A registered non-default Workspace that is unavailable at its expected location.
_Avoid_: Unknown Workspace

### Execution

**Worker Pool**:
The Repository-scoped collection of reusable Workers available for parallel work.
_Avoid_: Queue, worker farm

**Worker**:
A reusable execution slot backed by a Worker Workspace. A Worker accepts at most one Ticket at a time and is distinct from the Agent Runtime that executes it.
_Avoid_: Agent, Workspace, process

**Worker ID**:
The stable identity of a Worker across Runs and resets.
_Avoid_: Worker Alias, Worker Name

**Worker Alias**:
An optional display label for a Worker that does not change its identity.
_Avoid_: Worker ID, Worker Name

**Agent Runtime**:
The external coding agent selected to execute a Run.
_Avoid_: Agent, Worker

**Run**:
One execution attempt by an Agent Runtime in a Worker Workspace. A Run may finish in Done or Failed, or be abandoned by Reset.
_Avoid_: Agent Session, Worker

**Agent Session**:
The conversational context maintained by an Agent Runtime. One Agent Session can continue across multiple Runs on the same Worker.
_Avoid_: Run, Worker Session

**Worker Status**:
The lifecycle state of a Worker: Idle, Busy, Done, or Failed. Reset returns a Worker to Idle.
_Avoid_: Sub-issue Status, Linear status

**Idle**:
The Worker Status for a Worker available to accept a Reservation.

**Busy**:
The Worker Status for a Worker reserved for a Ticket or executing a Run.
_Avoid_: Running

**Done**:
The Worker Status for a Worker whose latest Run completed successfully.
_Avoid_: Merged

**Failed**:
The Worker Status for a Worker whose latest Run did not complete successfully.

**Reservation**:
The capacity allocation that pairs Tickets with Idle Workers before their Runs start. A Reservation changes Worker availability but is not itself a Dispatch.
_Avoid_: Dispatch, launch

**Reset**:
The operation that clears a Worker's retained assignment and returns it to Idle, terminating an active Run if present.
_Avoid_: Reclaim, kill

**Follow-up**:
A new instruction sent directly to a Worker that is not Busy, outside a Ticket's initial Dispatch. It continues a prior Agent Session when possible and otherwise begins a fresh one.
_Avoid_: Resume

### Tickets and dispatch

**Ticket**:
A Linear work item selected for implementation and identified by a key such as `AMBA-42`.
_Avoid_: GitHub issue, task, job

**Ready Ticket**:
A Ticket selected from Linear because it is in the `Todo` workflow state and carries the configured dispatch label.
_Avoid_: Ready Sub-issue, ready issue

**Parent Ticket**:
A Ticket used to organize its direct Linear children into a Dispatch Group. When it has no children, it can receive a Direct Dispatch itself.
_Avoid_: Epic, parent issue

**Sub-issue**:
A direct Linear child of a Parent Ticket included in that Parent Ticket's Dispatch Group.
_Avoid_: Sub-ticket, child issue

**Blocker**:
A sibling Sub-issue identified as the prerequisite of another Sub-issue.
_Avoid_: Dependent

**Dependency**:
The directed relationship from a Blocker to the Sub-issue that waits for it.
_Avoid_: Blocker

**Dispatch**:
A request to route a Ticket into execution, either directly on a Worker or through a Dispatch Group.
_Avoid_: Reservation, launch

**Direct Dispatch**:
A Dispatch that assigns the requested Ticket to one Worker and starts its Run without building a Dispatch Group.
_Avoid_: Orchestrated Dispatch

**Orchestrated Dispatch**:
A Dispatch that organizes a Parent Ticket's Sub-issues into a Dispatch Group and directs their execution in dependency order. It does not run the Parent Ticket itself.
_Avoid_: Direct Dispatch

**Dispatch Group**:
The dependency-aware execution progress of the direct Sub-issues associated with one Parent Ticket.
_Avoid_: Batch, Worker Pool

**Dispatch Wave**:
A set of Sub-issues whose Dependencies permit them to receive concurrent Direct Dispatches.
_Avoid_: Batch

**Stacked Pull Request**:
A pull request based on a prerequisite Sub-issue's change rather than directly on the Repository's main line.
_Avoid_: Independent pull request
