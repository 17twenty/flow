package core

import "time"

// Deploy source kinds (spec 13 §1). flow never talks to a cluster or a deploy
// tool: it reads records the forge holds about the application repository,
// written by whatever does the deploying.
const (
	// SourceForgeDeployments reads the forge's own deployment records.
	SourceForgeDeployments = "forge_deployments"
	// SourceCommitStatus reads a commit status with a configured context,
	// for forges that have no deployments API.
	SourceCommitStatus = "commit_status"
)

// Deployment states.
const (
	// DeploySuccess means the environment is running that commit.
	DeploySuccess = "success"
	// DeployFailure means the deployment did not work.
	DeployFailure = "failure"
	// DeployPending means it has not started.
	DeployPending = "pending"
	// DeployInProgress means it is under way.
	DeployInProgress = "in_progress"
)

// Deployment is one record that a commit reached an environment.
type Deployment struct {
	// ID is the provider's identifier for the record.
	ID string
	// Environment is what it deployed to.
	Environment string
	// SHA is the application commit that was deployed.
	SHA string
	// State is one of the deployment states above.
	State string
	// CreatedAt is when the deployment was recorded, and UpdatedAt when its
	// state last changed.
	CreatedAt time.Time
	UpdatedAt time.Time
	// URL is the web address of the record, or of the run behind it.
	URL string
	// Description is the one line the deployer left, shown as evidence.
	Description string
}

// Succeeded reports whether this record proves the commit is running.
func (d Deployment) Succeeded() bool { return d.State == DeploySuccess }

// DeployQuery selects deployment records.
type DeployQuery struct {
	// Environment is the environment to read, and is always set.
	Environment string
	// Since bounds the window; a zero time means no bound.
	Since time.Time
	// SHA limits the query to one commit.
	SHA string
	// Limit caps how many to return. Zero means no cap.
	Limit int
}
