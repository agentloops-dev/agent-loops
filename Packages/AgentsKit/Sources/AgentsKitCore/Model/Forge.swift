import Foundation

/// `forge/issues`, `forge/pulls`, `forge/issue` and `forge/pull` (#3129): a machine's issues and
/// pull requests as its forges say them, one model for GitHub, GitLab, Linear and Jira (#3260), so
/// one page draws each. The data behind Work ▸ Issues and Pull requests and their pages (#3119).
public extension DaemonAPI {
    /// A label, with its `#rrggbb` colour where the forge gives one (#2956).
    struct ForgeLabel: Codable, Sendable, Hashable {
        public var name: String
        public var color: String?

        public init(name: String, color: String? = nil) {
            self.name = name
            self.color = color
        }
    }

    /// An issue or a pull request (a GitLab merge request). `node` is its lineage node; `state`
    /// is open, closed or merged; `status` is the tracker's own name for its step, where its
    /// workflow has steps (a Jira status, as In Progress); `folder` is a folder on the machine on its repo. A pull
    /// request's own: `checks` is passing, failing, running or none; `review` is approved,
    /// changes_requested or review_required; `mergeable` is mergeable, conflicting or unknown.
    struct ForgeItem: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var title: String
        public var state: String
        /// Why a closed issue closed: completed, not_planned or duplicate, where the forge says
        /// it (`ForgeCapabilities.stateReason`); nil while it is open and on a pull request (#4365).
        public var stateReason: String?
        public var status: String?
        public var url: String?
        public var folder: String?
        public var labels: [ForgeLabel]
        public var author: String?
        /// The author's picture, a URL that only `forge/image` reads (#4363).
        public var authorAvatar: String?
        public var assignees: [String]
        /// The wire's, for the standing epics' queries; the app does not show it (#3878).
        public var milestone: String?
        /// The milestone's due date, where the forge says it (#5688); no client shows it yet.
        public var milestoneDue: Date?
        /// high, medium or low; nil for none (#5688). The forge's own priority field gives it
        /// where it has one, else the labels `priority:…` (#5747).
        public var priority: String?
        /// On hold (#5747): Bitbucket's state on hold, else the label `on hold`; nil is not on
        /// hold. No client shows it yet.
        public var onHold: Bool?
        public var updatedAt: Date
        /// When it opened, and when it closed or merged (nil while open), where the forge says
        /// them (`ForgeCapabilities.createdAt` and `closedAt`; #4373).
        public var createdAt: Date?
        public var closedAt: Date?
        public var comments: Int
        public var draft: Bool?
        public var checks: String?
        public var review: String?
        public var mergeable: String?
        public var autoMerge: Bool?
        /// A pull request's branches: the one it merges from and the one it merges into, where the
        /// forge says them (`ForgeCapabilities.branches`); a merged one's `mergedBy` and
        /// `mergeCommit` (#4409).
        public var headRef: String?
        public var baseRef: String?
        public var mergedBy: String?
        public var mergeCommit: String?
        /// The hierarchy (#3267): the issue it is a sub-issue of; its level (epic, story, task or
        /// spike, from its labels); its feature (the name of its `feature:` label); and a
        /// parent's closed children of all of them, where the forge says it.
        public var parent: LineageNode?
        public var level: String?
        public var feature: String?
        public var progress: ForgeProgress?
        /// It has the label `standing`: a standing epic, whose plan is a query (#3659).
        public var standing: Bool?

        public init(node: LineageNode, title: String, state: String, url: String? = nil, folder: String? = nil,
                    labels: [ForgeLabel] = [], author: String? = nil, assignees: [String] = [], milestone: String? = nil,
                    updatedAt: Date, comments: Int = 0, draft: Bool? = nil, checks: String? = nil, review: String? = nil,
                    mergeable: String? = nil, autoMerge: Bool? = nil, parent: LineageNode? = nil, level: String? = nil,
                    feature: String? = nil, progress: ForgeProgress? = nil, status: String? = nil, standing: Bool? = nil,
                    authorAvatar: String? = nil, stateReason: String? = nil, createdAt: Date? = nil,
                    closedAt: Date? = nil, headRef: String? = nil, baseRef: String? = nil, mergedBy: String? = nil,
                    mergeCommit: String? = nil) {
            self.node = node
            self.title = title
            self.state = state
            self.stateReason = stateReason
            self.status = status
            self.url = url
            self.folder = folder
            self.labels = labels
            self.author = author
            self.authorAvatar = authorAvatar
            self.assignees = assignees
            self.milestone = milestone
            self.updatedAt = updatedAt
            self.createdAt = createdAt
            self.closedAt = closedAt
            self.comments = comments
            self.draft = draft
            self.checks = checks
            self.review = review
            self.mergeable = mergeable
            self.autoMerge = autoMerge
            self.headRef = headRef
            self.baseRef = baseRef
            self.mergedBy = mergedBy
            self.mergeCommit = mergeCommit
            self.parent = parent
            self.level = level
            self.feature = feature
            self.progress = progress
            self.standing = standing
        }
    }

    /// A parent issue's children (#3267): `done` are closed, of `total`.
    struct ForgeProgress: Codable, Sendable, Hashable {
        public var done: Int
        public var total: Int

        public init(done: Int, total: Int) {
            self.done = done
            self.total = total
        }
    }

    /// `forge/issues` and `forge/pulls`' question: every folder's on the machine, or one
    /// folder's. `state` is open (when nil), closed or all; `limit` is 1 to 100, 50 when nil;
    /// `feature` (a name: work for the label feature:work) narrows it to one feature (#3267);
    /// `board` (a board's url, from `forge/boards`) to one board (#3282).
    struct ForgeListRequest: Codable, Sendable, Hashable {
        public var folder: String?
        public var state: String?
        public var label: String?
        public var assignee: String?
        public var query: String?
        public var cursor: String?
        public var limit: Int?
        public var feature: String?
        public var board: String?
        /// Only the items that changed at or after it (#6349); an earlier daemon lists every item.
        public var updatedSince: Date?
        /// The order (#6538): `priority` (High, then Medium, then Low, then none, each newest first), else the last
        /// updated first; an earlier daemon lists the last updated first.
        public var sort: String?
        /// A Refresh (#6642): the daemon reads the forges, not the answer it keeps for a minute.
        public var fresh: Bool?

        public init(folder: String? = nil, state: String? = nil, label: String? = nil, assignee: String? = nil,
                    query: String? = nil, cursor: String? = nil, limit: Int? = nil, feature: String? = nil,
                    board: String? = nil, updatedSince: Date? = nil, sort: String? = nil, fresh: Bool? = nil) {
            self.folder = folder
            self.state = state
            self.label = label
            self.assignee = assignee
            self.query = query
            self.cursor = cursor
            self.limit = limit
            self.feature = feature
            self.board = board
            self.updatedSince = updatedSince
            self.sort = sort
            self.fresh = fresh
        }
    }

    /// A forge a list could not read (not connected, a rate limit): github, gitlab, forgejo, bitbucket, azuredevops, linear, jira or azureboards.
    struct ForgeListError: Codable, Sendable, Hashable {
        public var forge: String
        public var message: String

        public init(forge: String, message: String) {
            self.forge = forge
            self.message = message
        }
    }

    /// What one forge on a list's page fills (#4361), so a client hides what it does not.
    /// `commentCount` says how to read an item's `comments`: `exact`; `capped` at `commentsMost`
    /// (show 50+ there); or `detail`, only on the item's own page (hide it on the row). A string,
    /// so a way a later daemon adds does not break this decoder. `createdAt` and `closedAt` say
    /// its items have those dates; `children`, `linkedPulls` and `events` that an issue's read has
    /// them (#4373); `total` that it counts a list's items (#4396); `releases` that it has releases
    /// (#4426); a pull request's `branches`, `mergedBy`, `mergeCommit`, `size`, `reviewers`,
    /// `closes` and `checkTimes` that it fills those (#4409); `priority` and `hold` that
    /// `forge/priority` and `forge/hold` set an issue's priority and on hold (#6091); each is nil
    /// (none) from an earlier daemon.
    struct ForgeCapabilities: Codable, Sendable, Hashable {
        public var forge: String
        public var issues: Bool
        public var pulls: Bool
        public var labelColors: Bool
        public var avatars: Bool
        public var stateReason: Bool
        public var createdAt: Bool?
        public var closedAt: Bool?
        public var children: Bool?
        public var linkedPulls: Bool?
        public var events: Bool?
        public var total: Bool?
        public var releases: Bool?
        public var branches: Bool?
        public var mergedBy: Bool?
        public var mergeCommit: Bool?
        public var size: Bool?
        public var reviewers: Bool?
        public var closes: Bool?
        public var checkTimes: Bool?
        public var priority: Bool?
        public var hold: Bool?
        public var commentCount: String
        public var commentsMost: Int?

        public init(forge: String, issues: Bool = false, pulls: Bool = false, labelColors: Bool = false,
                    avatars: Bool = false, stateReason: Bool = false, commentCount: String = "exact",
                    commentsMost: Int? = nil, createdAt: Bool? = nil, closedAt: Bool? = nil, children: Bool? = nil,
                    linkedPulls: Bool? = nil, events: Bool? = nil, total: Bool? = nil, releases: Bool? = nil,
                    branches: Bool? = nil, mergedBy: Bool? = nil, mergeCommit: Bool? = nil, size: Bool? = nil,
                    reviewers: Bool? = nil, closes: Bool? = nil, checkTimes: Bool? = nil,
                    priority: Bool? = nil, hold: Bool? = nil) {
            self.forge = forge
            self.issues = issues
            self.pulls = pulls
            self.labelColors = labelColors
            self.avatars = avatars
            self.stateReason = stateReason
            self.createdAt = createdAt
            self.closedAt = closedAt
            self.children = children
            self.linkedPulls = linkedPulls
            self.events = events
            self.total = total
            self.releases = releases
            self.branches = branches
            self.mergedBy = mergedBy
            self.mergeCommit = mergeCommit
            self.size = size
            self.reviewers = reviewers
            self.closes = closes
            self.checkTimes = checkTimes
            self.priority = priority
            self.hold = hold
            self.commentCount = commentCount
            self.commentsMost = commentsMost
        }
    }

    /// A page of items, the last updated first. `cursor` is the next page; nil at the end.
    /// `forges` is what each forge with an item on the page fills (#4361); nil from an earlier daemon.
    /// `total` is how many items the whole list has, on every page (#4396); nil when a forge on the
    /// list gives no count or could not be read, and from an earlier daemon.
    struct ForgeList: Codable, Sendable, Hashable {
        public var items: [ForgeItem]
        public var cursor: String?
        public var total: Int?
        public var errors: [ForgeListError]?
        public var forges: [ForgeCapabilities]?
        /// What the list reads (#6641): forge:name per repo or project; nil from an earlier daemon.
        public var scopes: [String]?
        /// When the forges were read for it (#6642), also for an answer the daemon kept; nil from an earlier daemon.
        public var readAt: Date?

        public init(items: [ForgeItem] = [], cursor: String? = nil, errors: [ForgeListError]? = nil,
                    forges: [ForgeCapabilities]? = nil, total: Int? = nil, scopes: [String]? = nil, readAt: Date? = nil) {
            self.items = items
            self.cursor = cursor
            self.total = total
            self.errors = errors
            self.forges = forges
            self.scopes = scopes
            self.readAt = readAt
        }
    }

    /// `forge/issue` and `forge/pull`'s question: the item, and for an older page of its
    /// comments the cursor of the page after it.
    struct ForgeItemRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var cursor: String?

        public init(node: LineageNode, cursor: String? = nil) {
            self.node = node
            self.cursor = cursor
        }
    }

    /// A comment, as Markdown. `bot` says a bot or an app wrote it; `avatar` is the author's
    /// picture, a URL that only `forge/image` reads (#4363).
    struct ForgeComment: Codable, Sendable, Hashable {
        public var author: String
        public var avatar: String?
        public var body: String
        public var at: Date
        public var url: String?
        public var bot: Bool?

        public init(author: String, body: String, at: Date, url: String? = nil, bot: Bool? = nil, avatar: String? = nil) {
            self.author = author
            self.avatar = avatar
            self.body = body
            self.at = at
            self.url = url
            self.bot = bot
        }
    }

    /// One of a pull request's checks: passing, failing, running or skipped. `startedAt` and
    /// `finishedAt` are when it started and ended, where the forge says them
    /// (`ForgeCapabilities.checkTimes`, #4409); nil before it starts or ends, and on a commit status.
    struct ForgeCheck: Codable, Sendable, Hashable {
        public var name: String
        public var state: String
        public var url: String?
        public var startedAt: Date?
        public var finishedAt: Date?

        public init(name: String, state: String, url: String? = nil, startedAt: Date? = nil, finishedAt: Date? = nil) {
            self.name = name
            self.state = state
            self.url = url
            self.startedAt = startedAt
            self.finishedAt = finishedAt
        }
    }

    /// A reviewer of a pull request (#4409): `state` is approved, changes_requested, commented or
    /// pending (asked, not answered), a string so a later daemon's new state does not break this
    /// decoder; `avatar` is their picture, a URL that only `forge/image` reads.
    struct ForgeReviewer: Codable, Sendable, Hashable {
        public var login: String
        public var avatar: String?
        public var state: String

        public init(login: String, state: String, avatar: String? = nil) {
            self.login = login
            self.avatar = avatar
            self.state = state
        }
    }

    /// How `forge/merge` merges a pull request (#3130).
    enum ForgeMergeMethod: String, Codable, Sendable, Hashable, CaseIterable {
        case merge, squash, rebase
    }

    /// The new-agent form for an issue (#3130, Start Agent), as a view's `agents/start-agent` fills it.
    struct ForgeStart: Codable, Sendable, Hashable {
        public var prompt: String
        public var labels: [String]
        public var worktree: String?

        public init(prompt: String, labels: [String] = [], worktree: String? = nil) {
            self.prompt = prompt
            self.labels = labels
            self.worktree = worktree
        }
    }

    /// A move an issue's workflow allows now (Jira, #3260): its `id`, which `forge/close` takes,
    /// its `name` (as Start Progress), and the status `category` it goes to: new, indeterminate or done.
    struct ForgeTransition: Codable, Sendable, Hashable {
        public var id: String
        public var name: String
        public var category: String?
    }

    /// One item with its body and a page of its comments: the newest page first, at most 50,
    /// oldest first in the page. `cursor` is the page before it; nil at the first comment. A pull
    /// request's `head` is the commit a merge or an approval names, and `mergeMethods` its repo's,
    /// the default first (#3130); an issue's `start` is its new-agent form, and `transitions` the
    /// moves its workflow allows (Jira, #3260), in place of Close and Reopen. `actions` are what
    /// the page offers now, by name (comment, label, close, reopen, transition, merge, autoMerge,
    /// approve, requestChanges, rerun, log, diff, addChild, blockedBy, assign, cancelAutoMerge, ready; #4361, #5857; an issue's priority and hold, #6092), as
    /// strings so a later daemon's new one does not break this decoder; nil from an earlier daemon. On an issue's read, where the
    /// forge says them (#4373): `children` are its sub-issues, `linkedPulls` the pull requests that
    /// close it, and `events` its timeline, oldest first; nil where the forge gives none. On a pull
    /// request's read (#4409): `additions`, `deletions` and `commits` are its size, `reviewers`
    /// each person asked or who reviewed, once, and `closes` the issues it closes when it merges.
    /// `viewer` is the login this machine reads and writes the forge as (#5804), the one
    /// `forge/approve` reviews as; nil where the connection does not say it.
    struct ForgeItemDetail: Codable, Sendable, Hashable {
        public var item: ForgeItem
        public var viewer: String?
        public var body: String
        public var comments: [ForgeComment]
        public var cursor: String?
        public var checks: [ForgeCheck]?
        public var reviewThreads: Int?
        public var filesChanged: Int?
        public var additions: Int?
        public var deletions: Int?
        public var commits: Int?
        public var reviewers: [ForgeReviewer]?
        public var closes: [ForgeLinked]?
        public var head: String?
        public var mergeMethods: [ForgeMergeMethod]?
        public var start: ForgeStart?
        public var transitions: [ForgeTransition]?
        public var actions: [String]?
        public var children: [ForgeLinked]?
        public var linkedPulls: [ForgeLinked]?
        public var events: [ForgeEvent]?

        public init(item: ForgeItem, body: String = "", comments: [ForgeComment] = [], cursor: String? = nil,
                    checks: [ForgeCheck]? = nil, reviewThreads: Int? = nil, filesChanged: Int? = nil,
                    head: String? = nil, mergeMethods: [ForgeMergeMethod]? = nil, start: ForgeStart? = nil,
                    transitions: [ForgeTransition]? = nil, actions: [String]? = nil, children: [ForgeLinked]? = nil,
                    linkedPulls: [ForgeLinked]? = nil, events: [ForgeEvent]? = nil, additions: Int? = nil,
                    deletions: Int? = nil, commits: Int? = nil, reviewers: [ForgeReviewer]? = nil,
                    closes: [ForgeLinked]? = nil, viewer: String? = nil) {
            self.item = item
            self.viewer = viewer
            self.body = body
            self.comments = comments
            self.cursor = cursor
            self.checks = checks
            self.reviewThreads = reviewThreads
            self.filesChanged = filesChanged
            self.additions = additions
            self.deletions = deletions
            self.commits = commits
            self.reviewers = reviewers
            self.closes = closes
            self.head = head
            self.mergeMethods = mergeMethods
            self.start = start
            self.transitions = transitions
            self.actions = actions
            self.children = children
            self.linkedPulls = linkedPulls
            self.events = events
        }
    }

    /// An item an issue's read names (#4373): a sub-issue, or a pull request that closes it; or an
    /// issue that a pull request closes (#4409).
    /// `state` is open, closed or merged.
    struct ForgeLinked: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var title: String
        public var state: String
        public var url: String?
    }

    /// One event on an issue's timeline (#4373). `kind` is labeled, unlabeled, assigned,
    /// unassigned, closed, reopened or referenced, a string so a later daemon's new kind does not
    /// break this decoder; `label` or `assignee` is whom it names; `source` is the item that
    /// referenced it, or the pull request that closed it.
    struct ForgeEvent: Codable, Sendable, Hashable {
        public var kind: String
        public var actor: String?
        public var at: Date
        public var label: ForgeLabel?
        public var assignee: String?
        public var source: LineageNode?

        public init(kind: String, actor: String? = nil, at: Date, label: ForgeLabel? = nil, assignee: String? = nil,
                    source: LineageNode? = nil) {
            self.kind = kind
            self.actor = actor
            self.at = at
            self.label = label
            self.assignee = assignee
            self.source = source
        }
    }

    // MARK: Writes (#3130)

    /// `forge/labels`, `forge/rerun` and `forge/diff`'s question: the issue or pull request.
    struct ForgeNodeRequest: Codable, Sendable, Hashable {
        public var node: LineageNode

        public init(node: LineageNode) { self.node = node }
    }

    /// `forge/comment`: a comment, as Markdown.
    struct ForgeCommentRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var body: String

        public init(node: LineageNode, body: String) {
            self.node = node
            self.body = body
        }
    }

    /// `forge/label`: labels to add, and labels to take off, by name.
    struct ForgeLabelRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var add: [String]?
        public var remove: [String]?

        public init(node: LineageNode, add: [String]? = nil, remove: [String]? = nil) {
            self.node = node
            self.add = add
            self.remove = remove
        }
    }

    /// `forge/assign` (#4403): people to assign, and people to take off, by their login on the
    /// forge; at most 10 of each. Offered where the item's `actions` has `assign`.
    struct ForgeAssignRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var add: [String]?
        public var remove: [String]?

        public init(node: LineageNode, add: [String]? = nil, remove: [String]? = nil) {
            self.node = node
            self.add = add
            self.remove = remove
        }
    }

    /// `forge/add-child` (#3271): a new child (`title`, with its `level`) or one that is there
    /// (`issue`: #12, owner/repo#12, its URL, or a key such as ENG-4), added to the epic `node`.
    struct ForgeAddChildRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var title: String?
        public var level: String?
        public var issue: String?

        public init(node: LineageNode, title: String? = nil, level: String? = nil, issue: String? = nil) {
            self.node = node
            self.title = title
            self.level = level
            self.issue = issue
        }
    }

    /// `forge/blocked-by` (#3271): issues that block the child `node`, to add and to take off.
    struct ForgeBlockedByRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var add: [LineageNode]?
        public var remove: [LineageNode]?

        public init(node: LineageNode, add: [LineageNode]? = nil, remove: [LineageNode]? = nil) {
            self.node = node
            self.add = add
            self.remove = remove
        }
    }

    /// Why `forge/close` closes an issue (#4404): `notPlanned` only where the item's `actions`
    /// have `closeNotPlanned`.
    enum ForgeCloseReason: String, Codable, Sendable, Hashable, CaseIterable {
        case completed
        case notPlanned = "not_planned"
    }

    /// `forge/close`: close it, or with `reopen` open it again; with `transition`, move a Jira
    /// issue by that transition of its workflow, by its id (#3260). `reason` is why an issue
    /// closes (#4404).
    struct ForgeCloseRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var reopen: Bool?
        public var transition: String?
        public var reason: ForgeCloseReason?

        public init(node: LineageNode, reopen: Bool? = nil, transition: String? = nil, reason: ForgeCloseReason? = nil) {
            self.node = node
            self.reopen = reopen
            self.transition = transition
            self.reason = reason
        }
    }

    /// `forge/merge`: merge at `sha`, the head the page shows, by `method` (the repo's default
    /// when nil); with `auto`, once its checks pass.
    struct ForgeMergeRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var sha: String
        public var method: ForgeMergeMethod?
        public var auto: Bool?

        public init(node: LineageNode, sha: String, method: ForgeMergeMethod? = nil, auto: Bool? = nil) {
            self.node = node
            self.sha = sha
            self.method = method
            self.auto = auto
        }
    }

    /// `forge/approve`: approve at `sha`, the head the page shows.
    struct ForgeApproveRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var sha: String

        public init(node: LineageNode, sha: String) {
            self.node = node
            self.sha = sha
        }
    }

    /// `forge/request-changes`: request changes at `sha`, the head the page shows. `body` is the
    /// review text, and it is required (#5857).
    struct ForgeRequestChangesRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var sha: String
        public var body: String

        public init(node: LineageNode, sha: String, body: String) {
            self.node = node
            self.sha = sha
            self.body = body
        }
    }

    /// `forge/priority` (#6091): set an issue's priority to `high`, `medium` or `low`, or to none
    /// when `priority` is nil, in the forge's own field where it has one, else by the labels
    /// `priority:<priority>`.
    struct ForgePriorityRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var priority: String?

        public init(node: LineageNode, priority: String? = nil) {
            self.node = node
            self.priority = priority
        }
    }

    /// `forge/hold` (#6091): put an issue on hold (`on`) or take it off hold, in the forge's own
    /// field where it has one, else by the label on hold.
    struct ForgeHoldRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var on: Bool

        public init(node: LineageNode, on: Bool) {
            self.node = node
            self.on = on
        }
    }

    /// `forge/log`: the failed checks' logs, or one check's by name.
    struct ForgeLogRequest: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var check: String?

        public init(node: LineageNode, check: String? = nil) {
            self.node = node
            self.check = check
        }
    }

    /// What a write did: `url` is the comment or the item; `auto` says a merge waits for the
    /// checks; `reran` is how many runs or pipelines ran again. `item` is the item as the forge
    /// has it after the write, with its `updatedAt` (#6351): a cache replaces its optimistic value
    /// with it. On `forge/add-child` it is the epic; absent when the read after the write failed.
    struct ForgeDone: Codable, Sendable, Hashable {
        public var url: String?
        public var auto: Bool?
        public var reran: Int?
        public var item: ForgeItem?

        public init(url: String? = nil, auto: Bool? = nil, reran: Int? = nil, item: ForgeItem? = nil) {
            self.url = url
            self.auto = auto
            self.reran = reran
            self.item = item
        }
    }

    /// `forge/labels`: the labels it can have, at most 100.
    struct ForgeLabels: Codable, Sendable, Hashable {
        public var labels: [ForgeLabel]
    }

    /// `forge/log`: the last lines of each failed check's log.
    struct ForgeLog: Codable, Sendable, Hashable {
        public var text: String
    }

    /// One @@ block of a file's diff.
    struct ForgeDiffHunk: Codable, Sendable, Hashable {
        public var oldStart: Int
        public var newStart: Int
        public var lines: [DiffLine]

        public init(oldStart: Int, newStart: Int, lines: [DiffLine]) {
            self.oldStart = oldStart
            self.newStart = newStart
            self.lines = lines
        }
    }

    /// One file a pull request changes; `note` says why it has no hunks, when it has none.
    struct ForgeDiffFile: Codable, Sendable, Hashable {
        public var path: String
        public var previousPath: String?
        public var change: String
        public var additions: Int
        public var deletions: Int
        public var hunks: [ForgeDiffHunk]
        public var note: String?

        public init(path: String, previousPath: String? = nil, change: String = "modified", additions: Int = 0,
                    deletions: Int = 0, hunks: [ForgeDiffHunk] = [], note: String? = nil) {
            self.path = path
            self.previousPath = previousPath
            self.change = change
            self.additions = additions
            self.deletions = deletions
            self.hunks = hunks
            self.note = note
        }
    }

    /// `forge/diff`: a pull request's files at its head, as the app's own diff draws them (#1341).
    struct ForgeDiff: Codable, Sendable, Hashable {
        public var url: String?
        public var files: [ForgeDiffFile]
        public var cut: Bool

        public init(url: String? = nil, files: [ForgeDiffFile] = [], cut: Bool = false) {
            self.url = url
            self.files = files
            self.cut = cut
        }

        /// The same diff as a view's `agents/open-diff` gives it, for the app's diff page; site is the forge it is on.
        public func appViewDiff(title: String, site: String = "GitHub") -> AppViewDiff {
            AppViewDiff(title: title, url: url.flatMap(URL.init(string:)), files: files.map { f in
                AppViewDiff.File(path: f.path, previousPath: f.previousPath, change: f.change, additions: f.additions,
                                 deletions: f.deletions, hunks: f.hunks.map { h in
                                     AppViewDiff.Hunk(oldStart: max(1, h.oldStart), newStart: max(1, h.newStart), lines: h.lines.map {
                                         AppViewDiff.Line(kind: AppViewDiff.Kind(rawValue: $0.kind.rawValue) ?? .context, text: $0.text)
                                     })
                                 }, note: f.note)
            }, cut: cut, site: site)
        }
    }
}

/// `forge/plan` (#3267): an epic's children in steps, from the forge's sub-issues and "blocked
/// by" links, with its lead.
public extension DaemonAPI {
    /// `forge/plan`'s question: the epic, an issue.
    struct ForgePlanRequest: Codable, Sendable, Hashable {
        public var node: LineageNode

        public init(node: LineageNode) {
            self.node = node
        }
    }

    /// A child of an epic: the issues it is blocked by; its step (1 when no other child blocks
    /// it); and its open blockers outside the plan.
    struct ForgePlanChild: Codable, Sendable, Hashable {
        public var item: ForgeItem
        public var blockedBy: [LineageNode]
        public var blockedOutside: [LineageNode]?
        public var step: Int

        public init(item: ForgeItem, blockedBy: [LineageNode] = [], blockedOutside: [LineageNode]? = nil, step: Int) {
            self.item = item
            self.blockedBy = blockedBy
            self.blockedOutside = blockedOutside
            self.step = step
        }
    }

    /// A step of a plan: its number, from 1, and its children's nodes.
    struct ForgePlanStep: Codable, Sendable, Hashable {
        public var step: Int
        public var nodes: [LineageNode]

        public init(step: Int, nodes: [LineageNode]) {
            self.step = step
            self.nodes = nodes
        }
    }

    /// An epic's lead session: the session labelled with the epic and `lead`.
    struct ForgeLead: Codable, Sendable, Hashable {
        public var session: UUID
        public var title: String?
        public var state: AgentState
    }

    /// An epic and its plan: its children, step by step; the steps; and its lead, if any. A
    /// standing epic's (#3661) has `standing`, and its children are its query's open issues, in
    /// order, all in step 1.
    struct ForgePlan: Codable, Sendable, Hashable {
        public var item: ForgeItem
        public var children: [ForgePlanChild]
        public var steps: [ForgePlanStep]
        public var lead: ForgeLead?
        public var standing: ForgeStanding?

        public init(item: ForgeItem, children: [ForgePlanChild] = [], steps: [ForgePlanStep] = [], lead: ForgeLead? = nil,
                    standing: ForgeStanding? = nil) {
            self.item = item
            self.children = children
            self.steps = steps
            self.lead = lead
            self.standing = standing
        }
    }

    /// A standing epic's query (#3659, #3661), as the daemon read it from the epic's body: the
    /// issue has all of `labels`, one of `any` and none of `not`; `milestone` is none, any or a
    /// name; `parent` is none for only issues that are no other issue's sub-issue (#4335, #4348);
    /// `lanes` its lead runs at once. `error` says why there is no query.
    struct ForgeStanding: Codable, Sendable, Hashable {
        public var labels: [String]
        public var any: [String]
        public var not: [String]
        public var milestone: String?
        public var parent: String?
        public var lanes: Int
        public var error: String?

        public init(labels: [String] = [], any: [String] = [], not: [String] = [], milestone: String? = nil,
                    parent: String? = nil, lanes: Int = 1, error: String? = nil) {
            self.labels = labels
            self.any = any
            self.not = not
            self.milestone = milestone
            self.parent = parent
            self.lanes = lanes
            self.error = error
        }
    }

    /// `forge/boards`' question (#3282): the boards of every folder's project on the machine,
    /// or of one folder's.
    struct ForgeBoardsRequest: Codable, Sendable, Hashable {
        public var folder: String?

        public init(folder: String? = nil) { self.folder = folder }
    }

    /// `forge/boards`' answer: each board once, by url.
    struct ForgeBoards: Codable, Sendable, Hashable {
        public var boards: [ProjectBoard]

        public init(boards: [ProjectBoard] = []) { self.boards = boards }
    }

    /// `forge/image`'s question (#4009): the url of an image on a forge page.
    struct ForgeImageRequest: Codable, Sendable, Hashable {
        public var url: String

        public init(url: String) { self.url = url }
    }

    /// `forge/image`'s answer: the image's bytes and their type (PNG, JPEG, GIF or WebP), so a
    /// client never loads the page's remote URL.
    struct ForgeImage: Codable, Sendable, Hashable {
        public var mimeType: String
        public var data: Data
    }
}

/// `forge/next` (#5689): the work that is ready, in one order, with the reason for each place.
public extension DaemonAPI {
    /// `forge/next`'s question: the folder; one epic's children, or the open epics (`level: "epic"`).
    struct ForgeNextRequest: Codable, Sendable, Hashable {
        public var folder: String
        public var epic: LineageNode?
        public var level: String?

        public init(folder: String, epic: LineageNode? = nil, level: String? = nil) {
            self.folder = folder
            self.epic = epic
            self.level = level
        }
    }

    /// A row: the item, its place (1 first), the reasons for it, and whether it is on hold.
    struct ForgeNextRow: Codable, Sendable, Hashable {
        public var item: ForgeItem
        public var place: Int
        public var reasons: [String]
        public var onHold: Bool

        public init(item: ForgeItem, place: Int, reasons: [String] = [], onHold: Bool = false) {
            self.item = item
            self.place = place
            self.reasons = reasons
            self.onHold = onHold
        }
    }

    /// The rows, at most 100, when they were read, and the forges that could not be read.
    struct ForgeNext: Codable, Sendable, Hashable {
        public var rows: [ForgeNextRow]
        public var readAt: Date
        public var errors: [ForgeListError]?

        public init(rows: [ForgeNextRow] = [], readAt: Date, errors: [ForgeListError]? = nil) {
            self.rows = rows
            self.readAt = readAt
            self.errors = errors
        }
    }
}

/// The reads a client cache syncs by (#6349, epic #6348): what changed since a cursor that
/// each client keeps, the ids now (for what a changed-since read does not see), many pull
/// requests' checks at once, and the sources a machine reads, so a client asks one machine
/// for each.
public extension DaemonAPI {
    /// `forge/changes`'s question: the items of `source` (every source on the machine when nil)
    /// of `kinds` (`issue`, `pr`) that changed at or after `updatedSince` (every item when nil).
    struct ForgeChangesRequest: Codable, Sendable, Hashable {
        public var source: String?
        public var kinds: [String]
        public var updatedSince: Date?
        public var pageCursor: String?
        public var limit: Int?

        public init(source: String? = nil, kinds: [String], updatedSince: Date? = nil, pageCursor: String? = nil, limit: Int? = nil) {
            self.source = source
            self.kinds = kinds
            self.updatedSince = updatedSince
            self.pageCursor = pageCursor
            self.limit = limit
        }
    }

    /// A page of what changed. `highWater` is the latest `updatedAt` of the read so far; the
    /// last page's is the client's next cursor. It does not move while a source failed (`errors`).
    struct ForgeChanges: Codable, Sendable, Hashable {
        public var items: [ForgeItem]
        public var nextPageCursor: String?
        public var highWater: Date
        public var errors: [ForgeListError]?
        public var forges: [ForgeCapabilities]?

        public init(items: [ForgeItem] = [], nextPageCursor: String? = nil, highWater: Date,
                    errors: [ForgeListError]? = nil, forges: [ForgeCapabilities]? = nil) {
            self.items = items
            self.nextPageCursor = nextPageCursor
            self.highWater = highWater
            self.errors = errors
            self.forges = forges
        }
    }

    /// `forge/ids`'s question: one source's items of `kinds` in `states` (`open`, `closed`; both when nil).
    struct ForgeIdsRequest: Codable, Sendable, Hashable {
        public var source: String
        public var kinds: [String]
        public var states: [String]?
        public var pageCursor: String?

        public init(source: String, kinds: [String], states: [String]? = nil, pageCursor: String? = nil) {
            self.source = source
            self.kinds = kinds
            self.states = states
            self.pageCursor = pageCursor
        }
    }

    /// A page of the nodes the source has now, at most 1,000. `at` is when the read began: keep
    /// a cached item whose `updatedAt` is at or after it, as it can be missing from the pages.
    struct ForgeIds: Codable, Sendable, Hashable {
        public var at: Date
        public var nodes: [LineageNode]
        public var nextPageCursor: String?

        public init(at: Date, nodes: [LineageNode] = [], nextPageCursor: String? = nil) {
            self.at = at
            self.nodes = nodes
            self.nextPageCursor = nextPageCursor
        }
    }

    /// `forge/checks`'s question: one source's pull requests, by number, 1 to 100.
    struct ForgeChecksRequest: Codable, Sendable, Hashable {
        public var source: String
        public var numbers: [Int]

        public init(source: String, numbers: [Int]) {
            self.source = source
            self.numbers = numbers
        }
    }

    /// One pull request's checks summary, in `ForgeItem`'s words.
    struct ForgeChecksRow: Codable, Sendable, Hashable {
        public var node: LineageNode
        public var state: String?
        public var checks: String?
        public var review: String?
        public var mergeable: String?
        public var autoMerge: Bool?

        public init(node: LineageNode, state: String? = nil, checks: String? = nil, review: String? = nil,
                    mergeable: String? = nil, autoMerge: Bool? = nil) {
            self.node = node
            self.state = state
            self.checks = checks
            self.review = review
            self.mergeable = mergeable
            self.autoMerge = autoMerge
        }
    }

    /// The checks of the pull requests asked for, and when they were read.
    struct ForgeChecks: Codable, Sendable, Hashable {
        public var pulls: [ForgeChecksRow]
        public var at: Date

        public init(pulls: [ForgeChecksRow] = [], at: Date) {
            self.pulls = pulls
            self.at = at
        }
    }

    /// A forge source the machine reads: `github:owner/repo`, `jira:proj`, `linear:eng` (lower case, as the nodes' refs), or
    /// `linear` for a whole workspace; `kinds` is what it has (`issue`, `pr`).
    struct ForgeSource: Codable, Sendable, Hashable {
        public var source: String
        public var forge: String
        public var kinds: [String]
        public var folder: String?

        public init(source: String, forge: String, kinds: [String], folder: String? = nil) {
            self.source = source
            self.forge = forge
            self.kinds = kinds
            self.folder = folder
        }
    }

    /// The machine's sources, and the forges whose sources could not all be found.
    struct ForgeSources: Codable, Sendable, Hashable {
        public var sources: [ForgeSource]
        public var errors: [ForgeListError]?

        public init(sources: [ForgeSource] = [], errors: [ForgeListError]? = nil) {
            self.sources = sources
            self.errors = errors
        }
    }
}

public extension DaemonAPI.Method {
    /// `ForgeChangesRequest` → `ForgeChanges`, `ForgeIdsRequest` → `ForgeIds`,
    /// `ForgeChecksRequest` → `ForgeChecks`, `Empty` → `ForgeSources` (#6349).
    static let forgeChanges = "forge/changes"
    static let forgeIds = "forge/ids"
    static let forgeChecks = "forge/checks"
    static let forgeSources = "forge/sources"
    /// `ForgeListRequest` → `ForgeList` (#3129).
    static let forgeIssues = "forge/issues"
    static let forgePulls = "forge/pulls"
    /// `ForgeItemRequest` → `ForgeItemDetail` (#3129).
    static let forgeIssue = "forge/issue"
    static let forgePull = "forge/pull"
    /// The writes (#3130): `ForgeCommentRequest`, `ForgeLabelRequest`, `ForgeCloseRequest`,
    /// `ForgeMergeRequest`, `ForgeApproveRequest` and `ForgeNodeRequest` → `ForgeDone`.
    static let forgeComment = "forge/comment"
    static let forgeLabel = "forge/label"
    static let forgeClose = "forge/close"
    static let forgeMerge = "forge/merge"
    static let forgeApprove = "forge/approve"
    /// `ForgeRequestChangesRequest` → `ForgeDone` (#5857).
    static let forgeRequestChanges = "forge/request-changes"
    /// `ForgePriorityRequest` and `ForgeHoldRequest` → `ForgeDone` (#6091): an issue's priority
    /// and on hold. Offered where its forge's `ForgeCapabilities` has `priority`, `hold`.
    static let forgePriority = "forge/priority"
    static let forgeHold = "forge/hold"
    static let forgeRerun = "forge/rerun"
    /// `ForgeNodeRequest` → `ForgeDone` (#4410): turn off a pull request's auto-merge, and mark
    /// a draft ready for review. Offered where the page's `actions` has `cancelAutoMerge`, `ready`.
    static let forgeCancelAutoMerge = "forge/cancel-auto-merge"
    static let forgeReady = "forge/ready"
    /// `ForgeNodeRequest` → `ForgeLabels`, `ForgeLogRequest` → `ForgeLog`, `ForgeNodeRequest` → `ForgeDiff` (#3130).
    static let forgeLabels = "forge/labels"
    /// `ForgeAssignRequest` → `ForgeDone` (#4403).
    static let forgeAssign = "forge/assign"
    static let forgeLog = "forge/log"
    static let forgeDiff = "forge/diff"
    /// `ForgePlanRequest` → `ForgePlan` (#3267).
    static let forgePlan = "forge/plan"
    /// `ForgeNextRequest` → `ForgeNext` (#5689).
    static let forgeNext = "forge/next"
    /// `ForgeBoardsRequest` → `ForgeBoards` (#3282).
    static let forgeBoards = "forge/boards"
    /// `ForgeImageRequest` → `ForgeImage` (#4009).
    static let forgeImage = "forge/image"
    /// `ForgeReleasesRequest` → `ForgeReleases`, `ForgeReleaseRequest` → `ForgeReleaseDetail` (#4426).
    static let forgeReleases = "forge/releases"
    static let forgeRelease = "forge/release"
    /// The plan's writes (#3271): `ForgeAddChildRequest` and `ForgeBlockedByRequest` → `ForgeDone`.
    static let forgeAddChild = "forge/add-child"
    static let forgeBlockedBy = "forge/blocked-by"
}
