package core

import "slices"

// Adapter kinds. Every capability declaration names one, and it decides which
// capability names the declaration may use (spec 05 §1).
const (
	// KindTracker is the ticket system: the board and its items.
	KindTracker = "tracker"
	// KindForge is the Git host: pull requests, protection, statuses.
	KindForge = "forge"
	// KindDeploys is the source of deployment records.
	KindDeploys = "deploys"
)

// Tracker capability names (spec 05 §2).
const (
	// CapProject is resolving the board itself.
	CapProject = "project"
	// CapStatusOptions is the set of columns or statuses.
	CapStatusOptions = "status_options"
	// CapStatusCategories is the todo/in-progress/done grouping behind them.
	CapStatusCategories = "status_categories"
	// CapWIPLimits is a limit the tracker enforces on a column.
	CapWIPLimits = "wip_limits"
	// CapExpediteView is a saved view for expedited work.
	CapExpediteView = "expedite_view"
	// CapLabels is reading and writing labels on items.
	CapLabels = "labels"
	// CapFollowUpTickets is filing a ticket for work flow cannot do itself.
	CapFollowUpTickets = "follow_up_tickets"
	// CapTicketMove is moving an item to another column.
	CapTicketMove = "ticket_move"
	// CapLinkedBranchesRead is the tracker's own link between a ticket and
	// the branches somebody started for it. It is a reading capability: a
	// tracker that does not offer one leaves flow with nothing but the
	// branch's name, and guessing a ticket out of a branch name is the user
	// regex P12 refuses.
	CapLinkedBranchesRead = "linked_branches_read"
)

// Forge capability names (spec 05 §2).
const (
	// CapCommitStatusWrite is publishing the policy gate's verdict.
	CapCommitStatusWrite = "commit_status_write"
	// CapBranchProtection is reading and writing protection on a branch.
	CapBranchProtection = "branch_protection"
	// CapRequiredStatusChecks is which checks a branch requires.
	CapRequiredStatusChecks = "required_status_checks"
	// CapMergeSettings is the repository's allowed merge strategies.
	CapMergeSettings = "merge_settings"
	// CapSquashText is where a squash commit takes its header and body
	// from. It is separate from the strategies because a forge can offer
	// one without the other: Gitea allows a repository to choose its merge
	// styles through the API and composes the squash commit itself.
	CapSquashText = "squash_text"
	// CapFileProposal is proposing a file change as a pull request.
	CapFileProposal = "file_proposal"
	// CapAutoMerge is arming a pull request to land itself once the checks
	// it is waiting for have passed.
	CapAutoMerge = "auto_merge"
	// CapPRComments is flow's own marked comments on a pull request.
	CapPRComments = "pr_comments"
	// CapUpdateBranch is bringing a pull request's branch up to date with its
	// base, so one that is ready to land is not held up by a merge it is
	// behind (spec 20 §3, issue #234).
	CapUpdateBranch = "update_branch"
	// CapDeploymentsRead is reading deployment records.
	CapDeploymentsRead = "deployments_read"
	// CapLinkedIssuesRead is the forge's own link between a PR and a ticket.
	CapLinkedIssuesRead = "linked_issues_read"
)

// Deploys capability names (spec 05 §2).
const (
	// CapDeployRecords is the deployment history for an environment.
	CapDeployRecords = "deploy_records"
)

// NativeTransitionPrefix begins the capability name for an event the tracker's
// own automation can perform: native_transition_<event> (spec 04 §5).
const NativeTransitionPrefix = "native_transition_"

// Native transition capability names. The event vocabulary in spec 04 §4 is
// larger than this list: an event with no name here is one no tracker offers
// an automation for, and so can only be owned by flow.
const (
	CapNativeTransitionPROpenedDraft    = NativeTransitionPrefix + "pr_opened_draft"
	CapNativeTransitionPRReadyForReview = NativeTransitionPrefix + "pr_ready_for_review"
	CapNativeTransitionChangesRequested = NativeTransitionPrefix + "changes_requested"
	CapNativeTransitionPRMerged         = NativeTransitionPrefix + "pr_merged"
	CapNativeTransitionIssueClosed      = NativeTransitionPrefix + "issue_closed"
	CapNativeTransitionDeployed         = NativeTransitionPrefix + "deployed"
)

// trackerCapabilities is every capability a tracker declaration must describe,
// in the order spec 05 §2 lists them.
var trackerCapabilities = []string{
	CapProject,
	CapStatusOptions,
	CapStatusCategories,
	CapWIPLimits,
	CapExpediteView,
	CapLabels,
	CapFollowUpTickets,
	CapTicketMove,
	CapLinkedBranchesRead,
	CapNativeTransitionPROpenedDraft,
	CapNativeTransitionPRReadyForReview,
	CapNativeTransitionChangesRequested,
	CapNativeTransitionPRMerged,
	CapNativeTransitionIssueClosed,
	CapNativeTransitionDeployed,
}

// forgeCapabilities is every capability a forge declaration must describe.
var forgeCapabilities = []string{
	CapCommitStatusWrite,
	CapBranchProtection,
	CapRequiredStatusChecks,
	CapMergeSettings,
	CapSquashText,
	CapAutoMerge,
	CapFileProposal,
	CapPRComments,
	CapUpdateBranch,
	CapDeploymentsRead,
	CapLinkedIssuesRead,
}

// deployCapabilities is every capability a deploys declaration must describe.
var deployCapabilities = []string{CapDeployRecords}

// AdapterKinds are the kinds a declaration may claim, in registry order.
func AdapterKinds() []string { return []string{KindTracker, KindForge, KindDeploys} }

// CapabilityNames returns the capabilities of one adapter kind, in registry
// order. An unknown kind has none.
func CapabilityNames(kind string) []string {
	switch kind {
	case KindTracker:
		return slices.Clone(trackerCapabilities)
	case KindForge:
		return slices.Clone(forgeCapabilities)
	case KindDeploys:
		return slices.Clone(deployCapabilities)
	default:
		return nil
	}
}

// KnownCapability reports whether name is a capability of that kind. Code asks
// about capabilities by the constants above, and a test asserts that every
// name in the registry and in every declaration agrees with the other, so a
// typo cannot quietly become an unsupported outcome (spec 05 §2).
func KnownCapability(kind, name string) bool {
	return slices.Contains(CapabilityNames(kind), name)
}

// NativeTransitionCapability is the capability name for an event the tracker's
// automation would perform. The second result is false for an event no tracker
// can be configured for, which spec 04 §5 leaves to owner: flow.
func NativeTransitionCapability(event string) (string, bool) {
	name := NativeTransitionPrefix + event
	return name, KnownCapability(KindTracker, name)
}
