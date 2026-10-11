package core

// Desired item ids (spec 06 §1). They are also the doctor check ids (spec 07
// §5) and the keys of the lock's `observed:` map (spec 03 §3), because they
// name the same things: one vocabulary for one subject.
//
// They live in core rather than in the engine because they are that
// vocabulary: the engine builds desired items out of them and an adapter is
// asked for one by name, and an adapter may not import the engine (spec 01
// §2).
const (
	// ItemTrackerProject is the board itself.
	ItemTrackerProject = "tracker.project"
	// ItemTrackerStatusOptions is the set of columns on it.
	ItemTrackerStatusOptions = "tracker.status_options"
	// ItemTrackerLabels is the labels flow's rules read.
	ItemTrackerLabels = "tracker.labels"
	// ItemTrackerWIP is the work-in-progress limits.
	ItemTrackerWIP = "tracker.wip"
	// ItemTrackerExpedite is the view that shows expedited work.
	ItemTrackerExpedite = "tracker.expedite"
	// ItemForgeDefaultBranch is the trunk.
	ItemForgeDefaultBranch = "forge.default_branch"
	// ItemForgeProtection is the protection on it.
	ItemForgeProtection = "forge.protection"
	// ItemForgeRequiredStatus is the checks a merge must pass.
	ItemForgeRequiredStatus = "forge.required_status"
	// ItemForgeMergeSettings is how a pull request is allowed to land.
	ItemForgeMergeSettings = "forge.merge_settings"
	// ItemForgeFiles is the files flow proposes to the repository.
	ItemForgeFiles = "forge.files"
	// ItemDeploysRecords is the deployment history flow reads.
	ItemDeploysRecords = "deploys.records"
)

// NativeItemPrefix begins the id of the item for one transition the tracker's
// own automation is to perform: tracker.native.pr_merged.
const NativeItemPrefix = "tracker.native."

// NativeItem returns the desired item id for a native transition on event.
func NativeItem(event string) string { return NativeItemPrefix + event }
