---
diataxis: explanation
description: Proposed feature inventory and evidence gates for alpha, beta and general availability.
devices: [mac, iphone, ipad, web, server]
---

# Which features are ready for people to use?

Proposal for #6902, assessed on 23 November 2026 at commit `3dc1196ea`.

Count one feature when an owner can enable one outcome for a person, project or installation. For example, disabling Voice mode leaves typed chat available. Disabling GitHub work removes its issues, pull requests and checks together. GitHub sign-in and Codespaces each have a separate purpose and can stay enabled. Several features can use the same wire method. Never use a display flag as an access check.

## How a feature moves between levels

| Decision | Required evidence |
|---|---|
| Enter alpha | An agent completes the task in the inventory once through a supported entry point. Keep the result, build and date. For a service integration, use the actual service or a supported self-hosted instance. A fake alone leaves the feature unverified. |
| Exit alpha and enter beta | Alex completes the task in at least three separate real work sessions across at least two days. Each result must be visible. Close every known defect that prevents completion and have Alex repeat the failed task on the fixed build. |
| Exit beta and enter GA | Keep the beta evidence. At least one person other than Alex completes the same task without agent assistance in two real work sessions on separate days. Record the outcome of both attempts. Verify one successful agent walk on each supported client in the feature's tier. Resolve and recheck each blocking defect. |
| Keep or exit GA | Review after a change to the main task or a confirmed failure. One reproducible blocking failure suspends the rating. Restore it after the affected person repeats the task on the fixed build. Retire the feature when its supported entry points are removed. |

These counts are proposed thresholds. One session can supply evidence for two features only if it shows both outcomes. For infrequent tasks such as first sign-in, use separate real installations or projects. Keep dates, build, actor category, outcome and an access-controlled reference with the project. Publish aggregate counts only. Existing customers keep their access during review.

## What the records support

The current registry contains zero exceptions. Its default gives every capability GA. Use an explicit assessment per feature before changing that default or the client badges. Map each existing capability to its feature's gate. Migrate any pilot entry by checking its evidence. [Current registry](../../Packages/Wire/maturity.json) · [Current badge rules](versions.md#maturity)

The session records for *Feature maturity records*, *Mouse scrolling in long prompt input* and *Cursor usage limit message* contain person-originated prompts and answers on 22–23 November. They meet the proposed beta usage threshold for basic session chat. The scrolling defect in #6887 concerns long input. The records also show why a runtime session count cannot prove that its user chose that runtime or used voice. The GitHub deployment record identifies Alex for one approval. That supplies one instance of use.

Weekly runtime and workflow totals can identify candidates for review. Commit trailers identify the making session and requester. Neither establishes that the requester performed the action. For Dashboard, count a person's completed lookup. A background refresh does not establish one. Record zero uses only when the source covered every eligible attempt for the full period. Otherwise record “unknown.” No independent person's repeated use was established in this review. [Report sources and retention](../reference/reports.md) · [Data and trust](direction.md)

## Inventory

Of 84 proposed features, 1 reaches beta, 25 reach alpha, 3 have a recorded blocker, and 55 need more evidence to enter alpha. None reaches GA.

Each row names a proposed flag and the task used to score it. **Unverified** means the available evidence falls short of alpha. **Blocked** means a recorded failure prevents the task. Both are assessment states before a level can be awarded. A code reference identifies the implemented outcome and lets a reviewer run the missing check.


### Working with agents

| Feature | Task to verify | Assessment | Evidence |
|---|---|---|---|
| Session chat | Send a prompt and read the answer | Beta | [Client](../../Web/src/views/Chat.tsx) and dated session records above |
| Voice mode | Dictate a prompt and hear the reply | Unverified | [Speech review](../../.agents/reviews/voice/2515-speech-transcriber.md) |
| Dashboard | Read a project tile on the folder Dashboard | Unverified | [Dashboard walks and implementation](../../specs/071-web-remote/walks/parity.md) |
| Work | Open a tracked issue and read its current state | Unverified | [Client](../../Web/src/views/Work.tsx) |
| Session review | Inspect the diff of completed work | Unverified | [Client](../../Web/src/views/Review.tsx) |
| Permission approvals | Answer a runtime permission request | Unverified | [Client](../../Web/src/views/Approval.tsx) |
| Questions | Send a selected answer to the waiting agent | Unverified | [Client](../../Web/src/views/Ask.tsx) |
| Live documents | Open a document and see a subsequent edit | Unverified | [Client](../../Web/src/views/LiveDocument.tsx) |
| Worktrees | Move a session to its branch and resume it | Alpha | [Session resumed on the proposal branch; client](../../Web/src/views/Worktrees.tsx) |
| Workflows | Trigger a workflow and inspect its result | Alpha | [Recorded walk](../../specs/071-web-remote/walks/parity/workflows-page.md) |
| Resource leases | Queue for a held resource and acquire it | Unverified | [Client](../../Web/src/views/Resources.tsx) |
| Agent browser | Follow a browser action in a session | Unverified | [Client](../../Web/src/views/AgentBrowserPane.tsx) |
| Simulator | Inspect a running simulator in the session | Unverified | [Client](../../Web/src/views/SimulatorPane.tsx) |
| Remote machines | Move work to a connected host | Unverified | [Client](../../Web/src/views/MoveToMachineDialog.tsx) |
| Warm pools | Start work on a prepared machine | Unverified | [Client](../../Web/src/views/WarmPools.tsx) |
| Secret management | Supply a stored secret after approval | Unverified | [Client](../../Web/src/views/SecretUseCard.tsx) |
| Account management | Select an account for a task | Unverified | [Client](../../Web/src/views/AccountsPage.tsx) |
| Connector setup | Prove a project connector with a read | Unverified | [Client](../../Web/src/views/ConnectorSetup.tsx) |
| Delivery tracking | Trace a deployment to its release | Unverified | [Client](../../Web/src/views/WorkDeploysPage.tsx) |
| Deploy approvals | Approve a waiting deployment and see it proceed | Alpha | [Recorded approval](../reference/integrations.md#mcp-and-github) |
| Reports | Read spend for a selected period | Unverified | [Client](../../Web/src/views/Reports.tsx) |
| Audit | Find an administrative action in the audit log | Alpha | [Recorded walk](../../specs/071-web-remote/walks/parity/events-and-resources.md) |
| Team administration | Set a person's group membership | Unverified | [Client](../../Web/src/views/Groups.tsx) |
| Project policy | Apply and read a project policy | Unverified | [Client](../../Web/src/views/ConsolePolicy.tsx) |
| First build | Take a new project to its first build | Unverified | [Client](../../Web/src/views/FirstBuild.tsx) |
| Session archive | Recover a completed session from archive | Unverified | [Client](../../Web/src/views/ArchivedSessionsPage.tsx) |
| File browsing | Open and read a project file | Unverified | [Client](../../Web/src/views/FilesPane.tsx) |
| Messages | Read a linked channel conversation | Unverified | [Client](../../Web/src/views/Messages.tsx) |
| Commit trailers | Identify the making session from a merged commit | Alpha | [Policy](../reference/policy.md) and merged commit 3dc1196ea |

### Runtime, model and machine integrations

| Feature | Task to verify | Assessment | Evidence |
|---|---|---|---|
| Claude | Start a session and receive a completed turn | Blocked | [broken (fails at handshake)](../reference/integrations.md#runtimes) |
| Grok | Start a session and receive a completed turn | Alpha | [tested on a real account](../reference/integrations.md#runtimes) |
| Copilot | Start a session and receive a completed turn | Alpha | [tested on a real account](../reference/integrations.md#runtimes) |
| Cursor | Start a session and receive a completed turn | Blocked | [broken (fails at turn)](../reference/integrations.md#runtimes) |
| Codex | Start a session and receive a completed turn | Alpha | [tested on a real account](../reference/integrations.md#runtimes) |
| Antigravity | Start a session and receive a completed turn | Alpha | [tested on a real account](../reference/integrations.md#runtimes) |
| OpenCode | Start a session and receive a completed turn | Alpha | [tested on a real account](../reference/integrations.md#runtimes) |
| Organisation model keys | Complete a turn through the configured endpoint | Unverified | [built](../reference/integrations.md#model-gateways) |
| Amazon Bedrock | Complete a turn through the configured endpoint | Unverified | [built (Claude only)](../reference/integrations.md#model-gateways) |
| Vertex AI | Complete a turn through the configured endpoint | Unverified | [built (Claude)](../reference/integrations.md#model-gateways) |
| Azure model gateway | Complete a turn through the configured endpoint | Unverified | [built](../reference/integrations.md#model-gateways) |
| Custom model endpoint | Complete a turn through the configured endpoint | Unverified | [built](../reference/integrations.md#model-gateways) |
| Docker | Start a machine and connect its host | Alpha | [walked on scratch](../reference/integrations.md#machine-providers) |
| Coder | Start a machine and connect its host | Alpha | [tested on a real account](../reference/integrations.md#machine-providers) |
| Codespaces | Start a machine and connect its host | Alpha | [tested on a real account](../reference/integrations.md#machine-providers) |
| DigitalOcean | Start a machine and connect its host | Alpha | [tested on a real account](../reference/integrations.md#machine-providers) |
| AWS EC2 | Start a machine and connect its host | Unverified | [built](../reference/integrations.md#machine-providers) |
| Google Compute Engine | Start a machine and connect its host | Alpha | [tested on a real account](../reference/integrations.md#machine-providers) |
| Google Cloud Workstations | Start a machine and connect its host | Blocked | [broken (Stop works, Start never brings the host back)](../reference/integrations.md#machine-providers) |

### Forge, communication and service integrations

| Feature | Task to verify | Assessment | Evidence |
|---|---|---|---|
| GitHub work | Open a repository issue and its pull request | Alpha | [tested on a real account](../reference/integrations.md#mcp-and-github) |
| GitLab work | Open a project issue and merge request | Alpha | [tested on a real account](../reference/integrations.md#mcp-and-github) |
| Forgejo | Read a repository issue | Unverified | [built, walked on a scratch Forgejo](../reference/integrations.md#mcp-and-github) |
| Bitbucket | Inspect a pull request | Unverified | [built, walked on a fake Bitbucket](../reference/integrations.md#mcp-and-github) |
| Azure Repos | Inspect a pull request | Unverified | [built, tested with a fake Azure DevOps](../reference/integrations.md#mcp-and-github) |
| Azure Boards | Read a work item | Unverified | [built, tested with a fake Azure DevOps](../reference/integrations.md#mcp-and-github) |
| Linear | Read a team issue | Alpha | [tested on a real account](../reference/integrations.md#mcp-and-github) |
| Jira | Read an issue from the connected project | Unverified | [walked on scratch](../reference/integrations.md#mcp-and-github) |
| CodeRabbit | Read a review on a pull request | Alpha | [tested on a real account](../reference/integrations.md#mcp-and-github) |
| Cloud Run | Trace a deployment to its revision | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| Email | Read a thread from the connected mailbox | Alpha | [built, tested on a real mailbox](../reference/integrations.md#mcp-and-github) |
| Slack | Receive a message and reply | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| Google Chat | Receive a message and reply | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| WhatsApp | Receive a message and reply | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| Sentry | Receive a new issue event | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| Better Stack | Trace an incident to its release | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| Confluence | Read a matching document | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| Notion | Read a matching document | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| Google Drive | Read a matching document | Unverified | [built](../reference/integrations.md#mcp-and-github) |
| GitHub MCP registry | Find a connector | Unverified | [built](../reference/integrations.md#marketplaces-and-catalogues) |
| Official MCP registry | Find a connector | Unverified | [built](../reference/integrations.md#marketplaces-and-catalogues) |
| skills.sh | Find a skill | Unverified | [built](../reference/integrations.md#marketplaces-and-catalogues) |
| Apple push notifications | Receive a notice on a device | Unverified | [built](../reference/integrations.md#notifications-and-links) |
| Web Push | Receive a browser notice | Unverified | [walked on scratch](../reference/integrations.md#notifications-and-links) |
| Google sign-in | Sign in and open the control plane | Unverified | [built, tested with a fake issuer](../reference/integrations.md#signing-a-person-in-to-the-control-plane) |
| Apple sign-in | Sign in and open the control plane | Unverified | [built, on: the capability on the App IDs and the entitlement (#2507)](../reference/integrations.md#signing-a-person-in-to-the-control-plane) |
| GitHub sign-in | Sign in and open the control plane | Unverified | [built, tested with a fake GitHub](../reference/integrations.md#signing-a-person-in-to-the-control-plane) |
| Generic OIDC | Sign in through a configured issuer | Alpha | [built, walked on a real issuer (Dex)](../reference/integrations.md#signing-a-person-in-to-the-control-plane) |
| Okta | Sign in through the organisation | Alpha | [built, walked on a real Okta org](../reference/integrations.md#signing-a-person-in-to-the-control-plane) |
| Microsoft Entra | Sign in through the tenant | Unverified | [built, tested with a fake provider](../reference/integrations.md#signing-a-person-in-to-the-control-plane) |
| Google Workspace | Sign in through the verified domain | Unverified | [built, tested with a fake provider](../reference/integrations.md#signing-a-person-in-to-the-control-plane) |
| SCIM provisioning | Provision and suspend a member | Unverified | [built](../reference/integrations.md#signing-a-person-in-to-the-control-plane) |
| Audit export | Receive an audit record at the sink | Alpha | [walked on scratch](../reference/integrations.md#control-plane-hosting) |
| OpenTelemetry | Receive report metrics at the collector | Alpha | [walked on scratch](../reference/integrations.md#control-plane-hosting) |
| BI read API | Read a scoped report | Alpha | [walked on scratch](../reference/integrations.md#control-plane-hosting) |
| Stripe billing | Change plan and confirm the entitlement | Unverified | [built, tested with a fake Stripe](../reference/integrations.md#control-plane-hosting) |

The inventory groups runtime sign-in, downloads and allowance detection with the runtime they serve. It groups a provider’s authentication, events and reads with the provider task. Infrastructure used only to deliver the product (such as DNS for the docs site), retired adapters and test doubles have no customer feature flag here. An installation option that customers can select independently can merit its own row.
