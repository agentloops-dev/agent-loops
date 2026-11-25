package machine

// BothRolesRefused is why a machine that runs a team's control plane does not run agents
// too, and what to do instead (#7305). Alex, 2026-11-21 (#6608): both roles are for a
// single-user control plane only, since an agent runs as the same Unix user as the control
// plane and can read its key and its store.
const BothRolesRefused = "This machine runs the control plane, and more than one person uses it: agents here could read its key and its data. Run the agents on another machine: join that one instead."
