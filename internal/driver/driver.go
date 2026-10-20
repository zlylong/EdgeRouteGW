// Package driver defines how the reconciler talks to one external component.
// A Driver turns the difference between two Artifacts into a Plan (what it
// would do) and then executes that Plan. Plans are also what the API shows for
// a dry run, so they must be computed without side effects.
package driver

import (
	"context"

	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/render"
)

// ActionKind classifies a planned step.
type ActionKind string

const (
	ActionNoop    ActionKind = "noop"    // nothing to do
	ActionWrite   ActionKind = "write"   // rewrite a file on disk
	ActionHot     ActionKind = "hot"     // apply without restarting the process
	ActionRestart ActionKind = "restart" // restart the unit
	ActionStart   ActionKind = "start"   // unit was down; start it
	ActionExec    ActionKind = "exec"    // run a command (nft -f, ip rule)
	ActionSkipped ActionKind = "skipped" // component not applicable in this mode
)

// Action is one planned step.
type Action struct {
	Kind   ActionKind `json:"kind"`
	Detail string     `json:"detail"`
}

// Plan is what a driver intends to do for one generation.
type Plan struct {
	Component model.Component `json:"component"`
	Actions   []Action        `json:"actions"`
	// Diff is a human-readable summary of what changed (file diff or a
	// sentence), for the UI's change preview.
	Diff string `json:"diff,omitempty"`
	// State is opaque driver data computed during Plan and handed back to
	// Apply (for example the outbounds the running instance holds). It is
	// never serialised.
	State any `json:"-"`
}

// Changes reports whether the plan does anything.
func (p Plan) Changes() bool {
	for _, a := range p.Actions {
		if a.Kind != ActionNoop && a.Kind != ActionSkipped {
			return true
		}
	}
	return false
}

// Summary joins the action kinds for logs and apply_log.
func (p Plan) Summary() string {
	if len(p.Actions) == 0 {
		return string(ActionNoop)
	}
	s := ""
	for i, a := range p.Actions {
		if i > 0 {
			s += "+"
		}
		s += string(a.Kind)
	}
	return s
}

// Driver is implemented once per component.
type Driver interface {
	Component() model.Component
	// Plan compares prev (the last artifacts this process applied, or nil
	// after a restart, in which case the driver inspects disk/runtime) with
	// next and returns the minimal steps. It must not change anything.
	Plan(ctx context.Context, prev, next *render.Artifacts) (Plan, error)
	// Apply executes a plan produced for next.
	Apply(ctx context.Context, plan Plan, next *render.Artifacts) error
	// Health reports the component's current state for /state.
	Health(ctx context.Context) model.ComponentHealth
}
