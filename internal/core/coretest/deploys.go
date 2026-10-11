package coretest

import (
	"context"
	"slices"
	"sync"

	"github.com/17twenty/flow/internal/core"
)

// DeployFixture is the deployment history a source presents (spec 14).
type DeployFixture struct {
	// Kind is [core.SourceForgeDeployments] or [core.SourceCommitStatus].
	Kind string
	// Capabilities is what it declares it can do.
	Capabilities core.CapabilitySet
	// Deployments are the records it holds, in any order.
	Deployments []core.Deployment
}

// FakeDeploys is an in-memory deployment source that behaves. Failures are
// injected through its embedded [Faults].
type FakeDeploys struct {
	Faults

	mu          sync.Mutex
	kind        string
	caps        core.CapabilitySet
	deployments []core.Deployment
}

// NewFakeDeploys builds a source holding the fixture's records.
func NewFakeDeploys(fx DeployFixture) *FakeDeploys {
	if fx.Kind == "" {
		fx.Kind = core.SourceForgeDeployments
	}
	if fx.Capabilities.Adapter == "" {
		fx.Capabilities = FullCapabilities(fx.Kind, core.KindDeploys)
	}
	return &FakeDeploys{
		kind:        fx.Kind,
		caps:        fx.Capabilities,
		deployments: slices.Clone(fx.Deployments),
	}
}

// Kind is the source's kind.
func (d *FakeDeploys) Kind() string { return d.kind }

// Capabilities is what the fake declares it can do.
func (d *FakeDeploys) Capabilities() core.CapabilitySet { return d.caps }

// Deployments returns the records matching q, newest first.
func (d *FakeDeploys) Deployments(ctx context.Context, q core.DeployQuery) ([]core.Deployment, error) {
	if err := d.enter(ctx, "Deployments"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []core.Deployment
	for _, deployment := range d.deployments {
		if q.Environment != "" && deployment.Environment != q.Environment {
			continue
		}
		if q.SHA != "" && deployment.SHA != q.SHA {
			continue
		}
		if !q.Since.IsZero() && deployment.CreatedAt.Before(q.Since) {
			continue
		}
		out = append(out, deployment)
	}
	slices.SortStableFunc(out, func(a, b core.Deployment) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Record adds a deployment record, so that a test can change the world
// between calls.
func (d *FakeDeploys) Record(deployment core.Deployment) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deployments = append(d.deployments, deployment)
}

// FakeDeploys is the reference implementation of the interface.
var _ core.DeploySource = (*FakeDeploys)(nil)
