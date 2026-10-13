package deploys

import (
	"cmp"
	"strconv"
	"time"

	"github.com/17twenty/flow/internal/core"
)

// The states GitHub puts on a deployment status (spec 10 §2). They are the
// provider's words; core's four are flow's.
const (
	ghDeploySuccess    = "success"
	ghDeployFailure    = "failure"
	ghDeployError      = "error"
	ghDeployInProgress = "in_progress"
	ghDeployQueued     = "queued"
	ghDeployPending    = "pending"
	// ghDeployInactive marks a record a later deployment superseded. It
	// says nothing about whether this one worked.
	ghDeployInactive = "inactive"
)

// deployState maps a GitHub status state onto core's, and reports whether it
// is one flow knows. An unknown state is not read as a failure or a success:
// guessing would either hide a deployment or invent one, so the caller refuses
// and says what it saw.
func deployState(state string) (string, bool) {
	switch state {
	case ghDeploySuccess:
		return core.DeploySuccess, true
	case ghDeployFailure, ghDeployError:
		return core.DeployFailure, true
	case ghDeployInProgress:
		return core.DeployInProgress, true
	case ghDeployQueued, ghDeployPending:
		return core.DeployPending, true
	default:
		return "", false
	}
}

// ghDeployment is GET /repos/{owner}/{repo}/deployments, which GitHub serves
// newest first.
type ghDeployment struct {
	ID          int64     `json:"id"`
	SHA         string    `json:"sha"`
	Ref         string    `json:"ref"`
	Environment string    `json:"environment"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// toDeployment converts the payload. The state is not in it: it comes from the
// record's statuses.
func (d ghDeployment) toDeployment() core.Deployment {
	return core.Deployment{
		ID:          strconv.FormatInt(d.ID, 10),
		Environment: d.Environment,
		SHA:         d.SHA,
		CreatedAt:   d.CreatedAt,
		UpdatedAt:   cmp.Or(d.UpdatedAt, d.CreatedAt),
		URL:         d.URL,
		Description: d.Description,
	}
}

// ghDeploymentStatus is GET /repos/{owner}/{repo}/deployments/{id}/statuses,
// which GitHub serves newest first.
type ghDeploymentStatus struct {
	State       string `json:"state"`
	Description string `json:"description"`
	// LogURL is where the deployer's run can be read; TargetURL is the
	// same address under the name the API used to give it.
	LogURL    string    `json:"log_url"`
	TargetURL string    `json:"target_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// The states GitHub puts on a commit status (spec 10 §2). There are four, and
// none of them is "in progress": a commit status says a check is pending or
// what it decided, so a deployment reported this way has no running state to
// report.
const (
	ghStatusSuccess = "success"
	ghStatusFailure = "failure"
	ghStatusError   = "error"
	ghStatusPending = "pending"
)

// statusState maps a GitHub commit status state onto core's, and reports
// whether it is one flow knows. An unknown state is refused for the same
// reason a deployment status's is: guessing would either hide a deployment or
// invent one.
func statusState(state string) (string, bool) {
	switch state {
	case ghStatusSuccess:
		return core.DeploySuccess, true
	case ghStatusFailure, ghStatusError:
		return core.DeployFailure, true
	case ghStatusPending:
		return core.DeployPending, true
	default:
		return "", false
	}
}

// ghRepository is GET /repos/{owner}/{repo}, read for one field: which branch
// the trunk is. Configuration does not name it (spec 03 §1), and the
// repository does.
type ghRepository struct {
	DefaultBranch string `json:"default_branch"`
}

// ghTrunkCommit is GET /repos/{owner}/{repo}/commits, which GitHub serves
// newest first. The deployment sources need the name of each commit and its
// date, and nothing else about it.
type ghTrunkCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

// ghCommitStatus is GET /repos/{owner}/{repo}/commits/{sha}/statuses, which
// GitHub serves newest first.
type ghCommitStatus struct {
	ID          int64     `json:"id"`
	Context     string    `json:"context"`
	State       string    `json:"state"`
	Description string    `json:"description"`
	TargetURL   string    `json:"target_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
