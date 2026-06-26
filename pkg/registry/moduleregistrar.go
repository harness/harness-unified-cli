// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"strings"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/spec"
)

// WorkflowFn is a named workflow handler registered in the Registry.
type WorkflowFn func(ctx *cmdctx.Ctx) error

// ModuleRegistrar is the interface passed to each module's ModuleInit. All
// IDs passed to RegisterWorkflow and RegisterTextFormatter are short (no colon)
// and are automatically namespaced as "module:id". Register stamps the module
// name onto spec.Module and rewrites WorkflowID / TextFormatter to their
// fully-qualified form.
type ModuleRegistrar interface {
	Register(cs *spec.CommandSpec) error
	RegisterWorkflow(shortID string, fn WorkflowFn)
	RegisterTextFormatter(shortID string, fn cmdctx.TextFormatterFn)
	RegisterBodyFn(shortID string, fn cmdctx.CreateBodyFn)
	RegisterQueryParamsFn(shortID string, fn cmdctx.QueryParamsFn)
	RegisterFollowFn(shortID string, fn cmdctx.FollowFn)
	RegisterFetchFn(shortID string, fn cmdctx.FetchFn)
	RegisterFlagCompletionFn(shortID string, fn FlagCompletionFn)
	RegisterFlagResolveFn(shortID string, fn cmdctx.FlagResolveFn)
	RegisterEndpointValidatorFn(shortID string, fn cmdctx.EndpointValidatorFn)
}

// moduleRegistrar is the concrete impl returned by Registry.Module.
type moduleRegistrar struct {
	module string
	reg    *Registry
}

// qualify resolves id into a fully-qualified "module:name" form.
// context describes what is being qualified (e.g. `command "create pipeline" workflow_id`)
// and is included in any error message. Returns "" on error (so the caller can skip the assignment).
// When use is true the id is coming from a spec reference and "core:" is allowed as a passthrough.
// When use is false the id is being registered by a module and "core:" is rejected.
func (m *moduleRegistrar) qualify(id, context string, use bool) string {
	if !strings.Contains(id, ":") {
		return m.module + ":" + id
	}
	prefix, _, _ := strings.Cut(id, ":")
	if prefix == corePrefix {
		if !use {
			m.reg.initErrs = append(m.reg.initErrs, fmt.Sprintf("module %q: %s: cannot register %q with reserved prefix %q", m.module, context, id, corePrefix))
			return ""
		}
		return id
	}
	if prefix != m.module {
		m.reg.initErrs = append(m.reg.initErrs, fmt.Sprintf("module %q: %s: %q has prefix %q but module is %q", m.module, context, id, prefix, m.module))
		return ""
	}
	if strings.Count(id, ":") > 1 {
		m.reg.initErrs = append(m.reg.initErrs, fmt.Sprintf("module %q: %s: %q cannot contain more than one colon", m.module, context, id))
		return ""
	}
	return id
}

func (m *moduleRegistrar) Register(cs *spec.CommandSpec) error {
	cs.Module = m.module
	cmd := fmt.Sprintf("command %q", cs.Command)
	if cs.WorkflowID != "" {
		cs.WorkflowID = m.qualify(cs.WorkflowID, cmd+" workflow_id", true)
	}
	if cs.Endpoint != nil && cs.Endpoint.TextFormatter != "" {
		cs.Endpoint.TextFormatter = m.qualify(cs.Endpoint.TextFormatter, cmd+" text_formatter", true)
	}
	if cs.Endpoint != nil && cs.Endpoint.BodyFn != "" {
		cs.Endpoint.BodyFn = m.qualify(cs.Endpoint.BodyFn, cmd+" body_fn", true)
	}
	if cs.Endpoint != nil && cs.Endpoint.QueryParamsFn != "" {
		cs.Endpoint.QueryParamsFn = m.qualify(cs.Endpoint.QueryParamsFn, cmd+" query_params_fn", true)
	}
	if cs.FollowFn != "" {
		cs.FollowFn = m.qualify(cs.FollowFn, cmd+" follow_fn", true)
	}
	if cs.Endpoint != nil && cs.Endpoint.FetchFn != "" {
		cs.Endpoint.FetchFn = m.qualify(cs.Endpoint.FetchFn, cmd+" fetch_fn", true)
	}
	if cs.Endpoint != nil {
		for i, id := range cs.Endpoint.ValidatorsEndpoint {
			cs.Endpoint.ValidatorsEndpoint[i] = m.qualify(id, fmt.Sprintf("%s validators_endpoint[%d]", cmd, i), true)
		}
	}
	for i := range cs.Flags {
		if cs.Flags[i].CompletionFn != "" {
			cs.Flags[i].CompletionFn = m.qualify(cs.Flags[i].CompletionFn, fmt.Sprintf("%s flag %q completion_fn", cmd, cs.Flags[i].Name), true)
		}
		if cs.Flags[i].FlagResolveFn != "" {
			cs.Flags[i].FlagResolveFn = m.qualify(cs.Flags[i].FlagResolveFn, fmt.Sprintf("%s flag %q flag_resolve_fn", cmd, cs.Flags[i].Name), true)
		}
	}
	return m.reg.Register(cs)
}

func (m *moduleRegistrar) RegisterWorkflow(shortID string, fn WorkflowFn) {
	if q := m.qualify(shortID, fmt.Sprintf("workflow %q", shortID), false); q != "" {
		m.reg.RegisterWorkflow(q, fn)
	}
}

func (m *moduleRegistrar) RegisterTextFormatter(shortID string, fn cmdctx.TextFormatterFn) {
	if q := m.qualify(shortID, fmt.Sprintf("text_formatter %q", shortID), false); q != "" {
		m.reg.RegisterTextFormatter(q, fn)
	}
}

func (m *moduleRegistrar) RegisterBodyFn(shortID string, fn cmdctx.CreateBodyFn) {
	if q := m.qualify(shortID, fmt.Sprintf("body_fn %q", shortID), false); q != "" {
		m.reg.RegisterBodyFn(q, fn)
	}
}

func (m *moduleRegistrar) RegisterQueryParamsFn(shortID string, fn cmdctx.QueryParamsFn) {
	if q := m.qualify(shortID, fmt.Sprintf("query_params_fn %q", shortID), false); q != "" {
		m.reg.RegisterQueryParamsFn(q, fn)
	}
}

func (m *moduleRegistrar) RegisterFollowFn(shortID string, fn cmdctx.FollowFn) {
	if q := m.qualify(shortID, fmt.Sprintf("follow_fn %q", shortID), false); q != "" {
		m.reg.RegisterFollowFn(q, fn)
	}
}

func (m *moduleRegistrar) RegisterFetchFn(shortID string, fn cmdctx.FetchFn) {
	if q := m.qualify(shortID, fmt.Sprintf("fetch_fn %q", shortID), false); q != "" {
		m.reg.RegisterFetchFn(q, fn)
	}
}

func (m *moduleRegistrar) RegisterFlagCompletionFn(shortID string, fn FlagCompletionFn) {
	if q := m.qualify(shortID, fmt.Sprintf("flag_completion_fn %q", shortID), false); q != "" {
		m.reg.RegisterFlagCompletionFn(q, fn)
	}
}

func (m *moduleRegistrar) RegisterFlagResolveFn(shortID string, fn cmdctx.FlagResolveFn) {
	if q := m.qualify(shortID, fmt.Sprintf("flag_resolve_fn %q", shortID), false); q != "" {
		m.reg.RegisterFlagResolveFn(q, fn)
	}
}

func (m *moduleRegistrar) RegisterEndpointValidatorFn(shortID string, fn cmdctx.EndpointValidatorFn) {
	if q := m.qualify(shortID, fmt.Sprintf("endpoint_validator_fn %q", shortID), false); q != "" {
		m.reg.RegisterEndpointValidatorFn(q, fn)
	}
}

// Module returns a ModuleRegistrar that namespaces all IDs under name.
func (r *Registry) Module(name string) ModuleRegistrar {
	return &moduleRegistrar{module: name, reg: r}
}
