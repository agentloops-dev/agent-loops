package machine

// BothRolesRefused is why a machine that runs a team's control plane does not run agents
// too, and what to do instead (#7305). Alex, 2026-11-21 (#6608): both roles are for a
// single-user control plane only, since an agent runs as the same Unix user as the control
// plane and can read its key and its store.
const BothRolesRefused = "This machine runs the control plane, and more than one person uses it: agents here could read its key and its data. Run the agents on another machine: join that one instead."

// SecondPersonRefused is why a control plane on a machine that also runs agents takes no
// second person (#7305; Alex, 2026-11-25), and what to do instead.
const SecondPersonRefused = "This control plane's machine also runs agents, which could read its key and its data, so only one person can use it. To add a person, an admin first removes that machine's agents in Machines, or moves the control plane to another machine."

// BothRolesWarning is what a team's control plane logs at each start while its own machine
// runs agents from before #7305: it stops none of them.
const BothRolesWarning = "This machine runs agents and the control plane of more than one person: agents here can read its key and its data. Move the agents to another machine and remove this one in Machines, or move the control plane."
