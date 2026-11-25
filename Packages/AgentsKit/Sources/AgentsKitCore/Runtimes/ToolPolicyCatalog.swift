import Foundation

// The policies, one per runtime this app knows how to start.
//
// Total over `RuntimeCatalog.builtIn`, and a unit test says so. A runtime without an
// entry here is a bug rather than a runtime that keeps everything: the failure of a
// missing policy is silent — an agent quietly wider than every other agent — so the only
// safe place for it to show up is a red test.
//
// Every value below was measured on 2026-09-19 against the versions installed on this
// Mac. See `specs/015-runtime-tool-scoping/research.md` for what was sent and what came
// back, and `contracts/runtime-launch.md` for the exact wire shapes these turn into.
public enum ToolPolicyCatalog {
    /// The policy for a runtime, or words alone for one we have never scoped.
    ///
    /// An unknown runtime is scoped by words rather than silently by nothing: it carries
    /// no residue we can name, so it gets no residue line, but the briefing still tells
    /// it what this app owns. The check script is what turns it into a real entry.
    public static func policy(for runtimeID: String) -> ToolPolicy {
        builtIn.first { $0.runtimeID == runtimeID } ?? ToolPolicy(runtimeID: runtimeID, lever: .words)
    }

    /// Claude: a denial list, appended to the adapter's own.
    ///
    /// The adapter reads `_meta.claudeCode.options` into its user-provided options and
    /// merges `disallowedTools` with what it already denies, so this takes tools away
    /// without replacing the preset — which is why a deny list and not the `tools`
    /// allowlist beside it: an allowlist takes precedence over the preset, and a work
    /// tool added upstream next month would silently never arrive (Research R2).
    ///
    /// The two connectors are named whole rather than tool by tool. A document store and
    /// a drive are artefact stores either way round, the read half of one is only useful
    /// for writing to it, and eighty tool names in this file would be stale within the
    /// month (Research R3). The person's other connectors — Crustdata, Dice,
    /// ZipRecruiter — search the world and duplicate nothing of ours, so FR-010 leaves
    /// them alone.
    public static let claude = ToolPolicy(
        runtimeID: RuntimeCatalog.claude.id,
        removed: [
            RemovedTool(name: "Workflow", category: .standingArrangements),
            RemovedTool(name: "CronCreate", category: .standingArrangements),
            RemovedTool(name: "CronList", category: .standingArrangements),
            RemovedTool(name: "CronDelete", category: .standingArrangements),
            RemovedTool(name: "ScheduleWakeup", category: .standingArrangements),
            RemovedTool(name: "Monitor", category: .standingArrangements),
            RemovedTool(name: "RemoteTrigger", category: .standingArrangements),
            RemovedTool(name: "PushNotification", category: .escalation),
            RemovedTool(name: "ReportFindings", category: .artefacts),
            RemovedTool(name: "DesignSync", category: .artefacts),
            RemovedTool(name: "mcp__claude_ai_Claude_Docs", category: .artefacts),
            RemovedTool(name: "mcp__claude_ai_Google_Drive", category: .artefacts),
            // Claude Code's own worktree tools move the session somewhere the app cannot
            // see: its Changes, its resume and its cleanup would all stay behind (053).
            RemovedTool(name: "EnterWorktree", category: .workingFolder),
            RemovedTool(name: "ExitWorktree", category: .workingFolder),
        ],
        lever: .sessionMetaDenyList(path: ["claudeCode", "options", "disallowedTools"]),
        // The Mac's Claude sign-in, relayed (056, research R3–R7). No files: Claude takes its
        // base URL and a stand-in subscription token from the environment, and trusts the
        // relay's CA through NODE_EXTRA_CA_CERTS. A cloud provider or key in the server's
        // environment would win over the relay, so they go for a relayed run.
        relay: SignInRelay(upstreamHost: "api.anthropic.com", macSignIn: .keychain(service: "Claude Code-credentials"),
                           pointing: .environment(["ANTHROPIC_BASE_URL": "https://127.0.0.1:{port}",
                                                   "CLAUDE_CODE_OAUTH_TOKEN": "{standIn}"]),
                           certificateVariable: "NODE_EXTRA_CA_CERTS",
                           clearedVariables: ["ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
                                              "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"],
                           ownSignInVariables: ["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"],
                           ownSignInFile: ".claude/.credentials.json"))

    /// Grok: a deny list, as a profile, plus a config overlay on disk.
    ///
    /// The profile's `disallowedTools` is the lever: the documented `--disallowed-tools`
    /// flag does nothing over `agent stdio` (Research R6). The profile needs its `name`
    /// and `description` too; without them Grok falls back to its stock profile and every
    /// tool comes back.
    ///
    /// It was an allow list, `agentProfile.tools`, until Grok 1.0.52 stopped giving the
    /// sub-agent tools it named, under their own names or Grok's ids (#1447, measured
    /// 2026-10-18). The deny list also takes `workflow` and `monitor`, which a stock
    /// profile added before the allow list applied, so they are no longer residue.
    ///
    /// Plan mode (`enter_plan_mode`, `exit_plan_mode`), MCP tool search (`search_tool`,
    /// `use_tool`), X search (`x_*_search`, `x_thread_fetch`) and `image_edit` stay: none
    /// of them does a job of the app's. `image_edit` was given under the allow list too,
    /// and no user config file can switch it off as `image_gen` is.
    public static let grok = ToolPolicy(
        runtimeID: RuntimeCatalog.grok.id,
        removed: [
            RemovedTool(name: "scheduler_create", category: .standingArrangements),
            RemovedTool(name: "scheduler_delete", category: .standingArrangements),
            RemovedTool(name: "scheduler_list", category: .standingArrangements),
            RemovedTool(name: "workflow", category: .standingArrangements),
            RemovedTool(name: "monitor", category: .standingArrangements),
            RemovedTool(name: "send_feedback", category: .escalation),
        ],
        lever: .sessionMetaDenyList(
            path: ["agentProfile", "disallowedTools"],
            extra: ["name": .string("agents-app"),
                    "description": .string("An agent hosted by the Agents app.")]))

    /// Copilot: three flags at launch.
    ///
    /// One of them does most of the work. `software-factory` is the person's own MCP
    /// server and a second implementation of this app's entire remit — escalations,
    /// artefacts, suggested prompts, agents and a task queue — so disabling the server
    /// removes a rival in all five categories at once. It keeps running; this app simply
    /// does not attach it to the agents it starts.
    ///
    /// `search_code_subagent` is residue because its real id is unknown. The model lists
    /// it as `functions.search_code_subagent`, and the flag rejected all nine spellings
    /// tried — `search_code_subagent`, `code_search_subagent`, `subagent`, `search_code`,
    /// `code_search`, `search_agent`, `search_codebase`, `codebase_search`,
    /// `semantic_search` — so nobody needs to re-run that experiment (Research R5). The
    /// general subagent tool, `task`, used to be excluded too; on 2026-09-29 sub-agents
    /// came back for every runtime, so the flag is down to one name.
    ///
    /// Copilot 1.0.105 added `dynamic_workflows_manage` and `run_dynamic_workflow`: workflows
    /// of its own, which run without the app seeing them, as Claude's `Workflow` does. The
    /// app owns standing arrangements (`manage_workflows`), so the flag takes both (#3139).
    public static let copilot = ToolPolicy(
        runtimeID: RuntimeCatalog.copilot.id,
        removed: [
            RemovedTool(name: "session_store_sql", category: .artefacts),
            RemovedTool(name: "dynamic_workflows_manage", category: .standingArrangements),
            RemovedTool(name: "run_dynamic_workflow", category: .standingArrangements),
        ],
        lever: .launchArguments(flag: "--excluded-tools", repeatsFlag: false,
                                extra: ["--disable-mcp-server", "software-factory",
                                        "--disable-builtin-mcps"]))

    /// Cursor: there is no lever.
    ///
    /// Its permission vocabulary is `Shell`, `Read`, `Write`, `Delete` and `Mcp` — there
    /// is no rule kind that names a built-in tool, so `Task`, `CreateGoal` and
    /// `UpdateGoal` cannot be denied, let alone hidden. `CURSOR_CONFIG_DIR` would let the
    /// app supply a whole config directory, but that directory also holds the
    /// credentials, so pointing Cursor at ours would sign the person out of theirs
    /// (Research R7).
    ///
    /// So there is nothing here to remove and nothing left over: sub-agents and goals
    /// came back on 2026-09-29, which leaves Cursor with no residue at all and the
    /// briefing with nothing to warn about.
    ///
    /// `AskQuestion` is kept and named: Cursor raises it as `cursor/ask_question`,
    /// which this app draws as a form elicitation the phone can answer. Composer
    /// sessions sometimes omit it from the tool catalogue; the briefing also names
    /// `ask_question` as the fallback the app always serves.
    public static let cursor = ToolPolicy(
        runtimeID: RuntimeCatalog.cursor.id,
        lever: .words)

    /// Codex: feature switches in `CODEX_CONFIG` (047, research R5).
    ///
    /// Codex's tools come from features in its config rather than a fixed list, and its
    /// ACP adapter merges the JSON object in `CODEX_CONFIG` into every session's config for
    /// that process. So the lever is an environment variable, and nothing in `~/.codex` is
    /// touched: `CODEX_HOME` would move `auth.json` too and sign the person out, which is
    /// Cursor's reason for having no lever at all.
    ///
    /// Measured on 0.156.1 with a real ChatGPT turn (research R5): the switches do take tools
    /// away (turning the shell features off empties the shell tools), so `sleep_tool` takes
    /// `clock.sleep`, and `goals`, `apps` and `in_app_local_automation` take Codex's own
    /// long-running goals, ChatGPT connectors and automations. Memories are left to
    /// Codex's own configuration.
    /// `computer_use` is off because the app has its own desktop control, `drive_screen`
    /// (#3004, FR-010): two tools driving one screen would fight over it.
    /// `multi_agent` is left on (Alex, 2026-09-26, 057): Codex's sub-agents are its own,
    /// and the app shows them as a list rather than refusing them. Off, it never took the
    /// `collaboration.*` tools away anyway (the model's catalog names them), so they were
    /// residue that every call was refused for.
    /// `goals` came back on 2026-09-29 with the rest of the task trackers, and is now
    /// written as `true` rather than left out: the switch is how this file states what it
    /// wants, and saying so is what the check script reads.
    /// `default_mode_request_user_input` is the opposite of the rest: it lets Codex's
    /// question tool, which the adapter raises as a form elicitation, ask outside plan mode.
    /// Measured again for #141 on 2026-10-03 (Codex 0.156.1, `codex-acp` 1.13.1, a scratch
    /// root's own toolset): `codex features list` knows the key ("under development");
    /// the adapter merges `CODEX_CONFIG` into every `thread/start`; and in the app's `agent`
    /// mode the model lists `request_user_input` and its question reaches the app as a
    /// form, answer and all. The #47 run that found it "not exposed" never called it.
    ///
    /// ChatGPT is offered before an API key when signing in (D1).
    public static let codex = ToolPolicy(
        runtimeID: RuntimeCatalog.codex.id,
        removed: [
            RemovedTool(name: "sleep", category: .standingArrangements),
            RemovedTool(name: "automations", category: .standingArrangements),
            RemovedTool(name: "apps", category: .artefacts),
            RemovedTool(name: "computer_use", category: .artefacts),
        ],
        lever: .environmentJSON(variable: "CODEX_CONFIG", value: .object([
            "features": .object([
                "sleep_tool": .bool(false),
                "goals": .bool(true),
                "in_app_local_automation": .bool(false),
                "apps": .bool(false),
                "computer_use": .bool(false),
                "multi_agent": .bool(true),
                "default_mode_request_user_input": .bool(true),
            ]),
        ])),
        preferredAuthMethods: ["chat-gpt", "chat-gpt-device-code", "api-key"],
        // The Mac's ChatGPT sign-in, relayed (research R12). Codex insists on HTTPS for it,
        // keeps only the origin for its model calls, and trusts CODEX_CA_CERTIFICATE.
        relay: SignInRelay(upstreamHost: "chatgpt.com", macSignIn: .file(".codex/auth.json"),
                           pointing: .home(homeVariable: "CODEX_HOME", configFile: "config.toml", signInFile: "auth.json",
                                           configTemplate: "chatgpt_base_url = \"https://127.0.0.1:{port}/backend-api/\"\n"),
                           certificateVariable: "CODEX_CA_CERTIFICATE"))

    /// Antigravity: a deny list in `_meta.agy.disabledTools` on `session/new`, and again on
    /// `session/load` and `session/resume`, which is exactly the lever the server documents
    /// for an ACP client ("Clients pass the filter under `_meta.agy`"). Measured against
    /// `agy_acp_server` 1.2.1 on 2026-09-25 (049 research R7).
    ///
    /// Its built-in tools are `list_directory`, `search_directory`, `find_file`,
    /// `view_file`, `create_file`, `edit_file`, `run_command`, `ask_question`,
    /// `start_subagent`, `generate_image`, `search_web`, `read_url_content` and `finish`.
    /// `start_subagent` is its sub-agent tool and came back on 2026-09-29, which leaves
    /// nothing denied — so nothing rides in `_meta` at all, rather than an empty
    /// `disabledTools`, which would say we had thought about it and found nothing to hide.
    /// `ask_question` is the escalation tool: the server raises it as
    /// `session/request_permission` whose options are the answers, and allows it without a
    /// "Run ask_question?" prompt of its own.
    ///
    /// The `/plan` command writes "an implementation plan artifact" into the server's own
    /// folder under `GEMINI_HOME`. It is a command the person types, not a tool the model
    /// can call, so it is neither removed nor residue.
    ///
    /// A Google account, personal first, is the only way in (049 D3); the key methods the
    /// server also offers are hidden (`RuntimeLaunchCatalog.hiddenAuthMethods`).
    public static let antigravity = ToolPolicy(
        runtimeID: RuntimeCatalog.antigravity.id,
        lever: .sessionMetaDenyList(path: ["agy", "disabledTools"]),
        preferredAuthMethods: ["oauth-personal", "oauth-business"])

    /// What OpenCode's built-in subagents are refused (#7220): each permission that would ask.
    /// OpenCode's ACP server drops a child session's request, so the turn would hang for ever.
    private static let openCodeSubagent: JSONValue = .object(["permission": .object([
        "bash": .string("deny"),
        "doom_loop": .string("deny"),
        "edit": .string("deny"),
        "external_directory": .string("deny"),
        "read": .object([
            "*.env": .string("deny"), "*.env.*": .string("deny"), "*.env.example": .string("allow"),
        ]),
        "webfetch": .string("deny"),
    ])])

    /// OpenCode: its own configuration, inline, in `OPENCODE_CONFIG_CONTENT`, which OpenCode
    /// merges over the person's `opencode.json` for this process only (049 D4). Measured
    /// against OpenCode 1.18.33 on 2026-09-28 (research R3).
    ///
    /// - `task` is OpenCode's sub-agent tool, and came back on 2026-09-29: the `tools` object
    ///   that took it out of the model's list is gone with it. For the record, it was a real
    ///   lever — `opencode debug agent build` showed a deny rule, and a real turn stopped
    ///   listing `task` — so this is a decision reversed, not a lever that never worked.
    ///   `todowrite` was always kept, as Claude's to-do tool is: a list inside the turn,
    ///   which OpenCode reports as the plan.
    /// - `permission`: OpenCode's own default is `"*": "allow"`, so left alone it edits,
    ///   runs commands and fetches without asking. These make it ask, as every other runtime
    ///   does; **Always Approve** (061) answers for the person.
    /// - `autoupdate` and `share` off: the app's pinned copy is never replaced under a running
    ///   agent (D5), and no conversation is published to a web page (FR-017).
    ///
    /// Its `question` tool is off over ACP unless `OPENCODE_ENABLE_QUESTION_TOOL` is set,
    /// which `RuntimeLaunchCatalog.opencode` removes, so no escalation tool: a question goes
    /// through the app's own `ask_question`, or ends the turn as needs_answer.
    public static let opencode = ToolPolicy(
        runtimeID: RuntimeCatalog.opencode.id,
        lever: .environmentJSON(variable: "OPENCODE_CONFIG_CONTENT", value: .object([
            "agent": .object(["explore": openCodeSubagent, "general": openCodeSubagent]),
            "autoupdate": .bool(false),
            "permission": .object([
                "bash": .string("ask"),
                "edit": .string("ask"),
                "webfetch": .string("ask"),
            ]),
            "share": .string("disabled"),
        ])),
        preferredAuthMethods: ["opencode-login"])

    /// In the order `RuntimeCatalog` declares its runtimes, so the two read side by side.
    public static let builtIn: [ToolPolicy] = [claude, grok, copilot, cursor, codex, antigravity, opencode]
}
