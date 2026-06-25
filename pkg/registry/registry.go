// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/harness/harness-cli/pkg/auth"
	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/pkg/console"
	"github.com/harness/harness-cli/pkg/endpoint"
	"github.com/harness/harness-cli/pkg/exprenv"
	"github.com/harness/harness-cli/pkg/format"
	"github.com/harness/harness-cli/pkg/hbase"
	"github.com/harness/harness-cli/pkg/hlog"
	"github.com/harness/harness-cli/pkg/plugin"
	"github.com/harness/harness-cli/pkg/spec"
	"github.com/harness/harness-cli/pkg/strutil"
)

// Token names for use in spec.EndpointSpec fields.
//
// In Path, embed tokens as {token}, e.g. "/har/api/v1/spaces/{auth:scope}/+/registries".
// In StaticQueryParams values, use the bare token name, e.g. "auth:scope" (no braces).
//
//	auth:account  — resolved account ID from the active auth profile
//	auth:org      — resolved org ID
//	auth:project  — resolved project ID
//	auth:scope    — account/org/project joined with "/" (non-empty parts only)
//	ctx:id        — positional <id> argument (RequiresId verbs: get, update, delete, execute)
//	ctx:parentid  — optional positional [parentid] argument (AllowsParentId verbs: list)

// FlagCompletionFn is a custom completion function for a flag. It receives the resolved
// auth, the positional args already typed, and the full flag set so it can read sibling
// flags (e.g. --stage when completing --step). Returns candidate strings or an error.
type FlagCompletionFn func(a *auth.ResolvedAuth, args []string, flags *pflag.FlagSet) ([]string, error)

// Registry holds all registered CommandSpecs and builds the cobra command tree.
type Registry struct {
	StrictYAML           bool // when true, loadSpecs rejects unknown YAML fields
	IsMainBinary         bool // when true, commands owned by external modules are exec'd to their plugin binary
	specs                map[string][]*spec.CommandSpec
	nouns                map[string]spec.NounDef
	nounAliases          map[string]string // alias name → canonical noun name
	moduleMetas          []spec.ModuleMeta
	workflows            map[string]WorkflowFn
	textFormatters       map[string]cmdctx.TextFormatterFn
	bodyFns              map[string]cmdctx.CreateBodyFn
	followFns            map[string]cmdctx.FollowFn
	fetchFns             map[string]cmdctx.FetchFn
	flagCompletionFns    map[string]FlagCompletionFn
	endpointValidatorFns map[string]cmdctx.EndpointValidatorFn
	initErrs             []string
}

func New() *Registry {
	r := &Registry{
		StrictYAML:           os.Getenv(hbase.EnvCheckSpecs) == "1",
		specs:                map[string][]*spec.CommandSpec{},
		nouns:                map[string]spec.NounDef{},
		nounAliases:          map[string]string{},
		workflows:            map[string]WorkflowFn{},
		textFormatters:       map[string]cmdctx.TextFormatterFn{},
		bodyFns:              map[string]cmdctx.CreateBodyFn{},
		followFns:            map[string]cmdctx.FollowFn{},
		fetchFns:             map[string]cmdctx.FetchFn{},
		flagCompletionFns:    map[string]FlagCompletionFn{},
		endpointValidatorFns: map[string]cmdctx.EndpointValidatorFn{},
	}
	r.registerCoreFormatters()
	return r
}

// SetModuleMeta stores metadata for a module loaded from a spec file.
func (r *Registry) SetModuleMeta(m spec.ModuleMeta) {
	r.moduleMetas = append(r.moduleMetas, m)
}

// externalBinaryFor returns the ExternalBinary name for the given module, or "".
// Returns "" when IsMainBinary is false.
func (r *Registry) externalBinaryFor(module string) string {
	if !r.IsMainBinary {
		return ""
	}
	for _, m := range r.moduleMetas {
		if m.Name == module {
			return m.ExternalBinary
		}
	}
	return ""
}

// GetModuleMetas returns metadata for all loaded modules, sorted by spec.ModuleOrder.
func (r *Registry) GetModuleMetas() []spec.ModuleMeta {
	out := make([]spec.ModuleMeta, len(r.moduleMetas))
	copy(out, r.moduleMetas)
	spec.SortModules(out)
	return out
}

// GetSpecsForModule returns all registered CommandSpecs belonging to the given module.
func (r *Registry) GetSpecsForModule(module string) []*spec.CommandSpec {
	var out []*spec.CommandSpec
	for _, specs := range r.specs {
		for _, cs := range specs {
			if cs.Module == module {
				out = append(out, cs)
			}
		}
	}
	return out
}

// GetAllSpecs returns every registered CommandSpec across all modules.
func (r *Registry) GetAllSpecs() []*spec.CommandSpec {
	var out []*spec.CommandSpec
	for _, specs := range r.specs {
		out = append(out, specs...)
	}
	return out
}

// GetVerbInfos returns display metadata for all verbs that have at least one registered command,
// in canonical VerbOrder.
func (r *Registry) GetVerbInfos() []spec.VerbInfo {
	var out []spec.VerbInfo
	for _, verb := range VerbOrder {
		if len(r.specs[verb]) == 0 {
			continue
		}
		vs := verbRegistry[verb]
		out = append(out, spec.VerbInfo{
			Verb:      verb,
			ShortDesc: vs.ShortDesc,
			Gerund:    vs.Gerund,
		})
	}
	return out
}

// RegisterNoun registers a noun definition. Returns an error on duplicate or invalid fields.
func (r *Registry) RegisterNoun(nd spec.NounDef) error {
	if _, exists := r.nouns[nd.Noun]; exists {
		return fmt.Errorf("duplicate noun %q", nd.Noun)
	}
	for _, f := range nd.Fields {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("noun %q: %w", nd.Noun, err)
		}
	}
	for _, alias := range nd.NounAliases {
		if _, exists := r.nouns[alias]; exists {
			return fmt.Errorf("noun %q: alias %q conflicts with existing noun (module %q)", nd.Noun, alias, r.moduleForNoun(alias))
		}
		if owner, exists := r.nounAliases[alias]; exists {
			return fmt.Errorf("noun %q: alias %q already claimed by noun %q (module %q)", nd.Noun, alias, owner, r.moduleForNoun(owner))
		}
	}
	r.nouns[nd.Noun] = nd
	for _, alias := range nd.NounAliases {
		r.nounAliases[alias] = nd.Noun
	}
	return nil
}

// moduleForNoun returns the module name that owns the given noun, or "unknown" if not found.
// Used only in error paths; walks all registered specs.
func (r *Registry) moduleForNoun(noun string) string {
	for _, specs := range r.specs {
		for _, cs := range specs {
			if cs.Noun == noun && cs.Module != "" {
				return cs.Module
			}
		}
	}
	return "unknown"
}

// GetNoun returns the NounDef for a noun, or nil if not registered.
func (r *Registry) GetNoun(noun string) *spec.NounDef {
	if nd, ok := r.nouns[noun]; ok {
		return &nd
	}
	return nil
}

// ResolveTextFormatter implements cmdctx.Resolver.
func (r *Registry) ResolveTextFormatter(id string) cmdctx.TextFormatterFn {
	return r.textFormatters[id]
}

// ResolveBodyFn implements cmdctx.Resolver.
func (r *Registry) ResolveBodyFn(id string) cmdctx.CreateBodyFn {
	return r.bodyFns[id]
}

// ResolveFetchFn implements cmdctx.Resolver.
func (r *Registry) ResolveFetchFn(id string) (cmdctx.FetchFn, error) {
	fn, ok := r.fetchFns[id]
	if !ok {
		return nil, fmt.Errorf("fetch_fn %q not registered", id)
	}
	return fn, nil
}

// RegisterFetchFn registers a fully-qualified fetch function ID.
func (r *Registry) RegisterFetchFn(id string, fn cmdctx.FetchFn) {
	if _, ok := r.fetchFns[id]; ok {
		panic(fmt.Sprintf("registry: duplicate fetch fn %q", id))
	}
	r.fetchFns[id] = fn
}

// RegisterFlagCompletionFn registers a fully-qualified flag completion function.
func (r *Registry) RegisterFlagCompletionFn(id string, fn FlagCompletionFn) {
	if _, ok := r.flagCompletionFns[id]; ok {
		panic(fmt.Sprintf("registry: duplicate flag completion fn %q", id))
	}
	r.flagCompletionFns[id] = fn
}

// RegisterBodyFn registers a fully-qualified body constructor ID.
func (r *Registry) RegisterBodyFn(id string, fn cmdctx.CreateBodyFn) {
	if _, ok := r.bodyFns[id]; ok {
		panic(fmt.Sprintf("registry: duplicate body fn %q", id))
	}
	r.bodyFns[id] = fn
}

// RegisterEndpointValidatorFn registers a fully-qualified endpoint validator ID.
func (r *Registry) RegisterEndpointValidatorFn(id string, fn cmdctx.EndpointValidatorFn) {
	if _, ok := r.endpointValidatorFns[id]; ok {
		panic(fmt.Sprintf("registry: duplicate endpoint validator fn %q", id))
	}
	r.endpointValidatorFns[id] = fn
}

// ResolveEndpointValidator implements cmdctx.Resolver.
func (r *Registry) ResolveEndpointValidator(id string) cmdctx.EndpointValidatorFn {
	return r.endpointValidatorFns[id]
}

// RegisterFollowFn registers a fully-qualified follow function ID.
func (r *Registry) RegisterFollowFn(id string, fn cmdctx.FollowFn) {
	if _, ok := r.followFns[id]; ok {
		panic(fmt.Sprintf("registry: duplicate follow fn %q", id))
	}
	r.followFns[id] = fn
}

// RegisterTextFormatter registers a fully-qualified text formatter ID.
func (r *Registry) RegisterTextFormatter(id string, fn cmdctx.TextFormatterFn) {
	if _, ok := r.textFormatters[id]; ok {
		panic(fmt.Sprintf("registry: duplicate text formatter %q", id))
	}
	r.textFormatters[id] = fn
}

// RegisterWorkflow registers a fully-qualified workflow handler. Panics on duplicate.
func (r *Registry) RegisterWorkflow(id string, fn WorkflowFn) {
	if _, ok := r.workflows[id]; ok {
		panic(fmt.Sprintf("registry: duplicate workflow %q", id))
	}
	r.workflows[id] = fn
}

// GetSpec returns the CommandSpec for the given verb and noun, or nil if not found.
// Pass an empty string for noun when looking up leaf verbs (e.g. "version", "ask").
// For variant commands, pass the full "noun:variant" string.
func (r *Registry) GetSpec(verb, noun string) *spec.CommandSpec {
	for _, cs := range r.specs[verb] {
		if cs.FullNoun() == noun {
			return cs
		}
	}
	return nil
}

// Register adds a spec to the registry. Returns an error if the verb is not in
// the allowed set, if verb/noun constraints are violated, or if a duplicate exists.
func (r *Registry) Register(cs *spec.CommandSpec) error {
	if cs.DevOnly && !hbase.IsDev() {
		return nil
	}
	if cs.External {
		return fmt.Errorf("command %q: External must not be set before registration", cs.Command)
	}
	if cs.VerbHandler == "" {
		cs.VerbHandler = cs.Verb
	}
	vs, ok := verbRegistry[cs.Verb]
	if !ok {
		return fmt.Errorf("verb %q is not in the allowed verb set", cs.Verb)
	}
	if err := validateSpec(cs, vs); err != nil {
		return err
	}
	if r.IsMainBinary && r.externalBinaryFor(cs.Module) != "" {
		cs.External = true
	}
	existing := r.specs[cs.Verb]
	if vs.Kind == VerbKindLeaf && len(existing) > 0 {
		return fmt.Errorf("duplicate leaf verb %q: only one registration allowed", cs.Verb)
	}
	for _, s := range existing {
		if s.FullNoun() == cs.FullNoun() {
			return fmt.Errorf("duplicate command: %s %s", cs.Verb, cs.FullNoun())
		}
	}
	r.specs[cs.Verb] = append(existing, cs)
	return nil
}

// BuildCommands returns the cobra commands for all registered specs.
func (r *Registry) BuildCommands() []*cobra.Command {
	verbCmds := r.buildVerbCommands()

	for verb, specs := range r.specs {
		vs := verbRegistry[verb]
		if vs.Kind == VerbKindLeaf {
			continue
		}
		for _, cs := range specs {
			verbCmds[verb].AddCommand(r.buildCmd(cs))
			nd := r.GetNoun(cs.Noun)
			if nd != nil {
				for _, alias := range nd.NounAliases {
					verbCmds[verb].AddCommand(r.buildAliasCmd(cs, alias))
				}
			}
		}
	}

	out := make([]*cobra.Command, 0, len(verbCmds))
	for _, vc := range verbCmds {
		out = append(out, vc)
	}
	return out
}

// buildVerbCommands creates one top-level cobra.Command for each verb that has
// at least one registered spec. Leaf verbs are built directly from their spec.
func (r *Registry) buildVerbCommands() map[string]*cobra.Command {
	setup := func(cmd *cobra.Command, args []string) error {
		return hbase.EnsureHarnessHome()
	}
	verbCmds := make(map[string]*cobra.Command, len(r.specs))
	for verb, specs := range r.specs {
		vs := verbRegistry[verb]
		var vc *cobra.Command
		if vs.Kind == VerbKindLeaf {
			vc = r.buildCmd(specs[0])
		} else {
			verbCopy := verb
			vc = &cobra.Command{
				Use:    string(verb),
				Short:  vs.ShortDesc,
				Hidden: vs.HideGroup,
				// Suppress Cobra's flag-parse errors so that typos in the noun
				// (e.g. "create piepline -f x") produce "unknown noun" instead of
				// "unknown shorthand flag: 'f' in -f".
				FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
				RunE: func(cmd *cobra.Command, args []string) error {
					if len(args) == 0 {
						return cmd.Help()
					}
					return r.unknownNounError(verbCopy, args[0])
				},
			}
		}
		addSetupFn(vc, vs, setup)
		verbCmds[verb] = vc
	}
	return verbCmds
}

// unknownNounError returns a descriptive error for an unrecognized noun under verb.
// It distinguishes between nouns that exist globally but aren't supported by this verb
// vs. nouns that are simply unknown, and appends Levenshtein-based suggestions scoped
// to nouns that are actually valid for verb.
func (r *Registry) unknownNounError(verb, noun string) error {
	resolvedNoun := noun
	if canonical, ok := r.nounAliases[noun]; ok {
		resolvedNoun = canonical
	}
	nounExistsForVerb := false
	for _, cs := range r.specs[verb] {
		if cs.Noun == resolvedNoun || cs.FullNoun() == noun {
			nounExistsForVerb = true
			break
		}
	}
	_, nounRegistered := r.nouns[resolvedNoun]
	var msg string
	if nounRegistered && !nounExistsForVerb {
		msg = fmt.Sprintf("%q is not supported for %q", noun, verb)
	} else {
		msg = fmt.Sprintf("%q is not a valid noun for %q", noun, verb)
	}
	// Build candidates: every FullNoun and its aliases, scoped to this verb only.
	type candidate struct{ display, canonical string }
	var candidates []candidate
	for _, cs := range r.specs[verb] {
		candidates = append(candidates, candidate{cs.FullNoun(), cs.FullNoun()})
		if nd := r.GetNoun(cs.Noun); nd != nil {
			for _, alias := range nd.NounAliases {
				candidates = append(candidates, candidate{alias, cs.FullNoun()})
			}
		}
	}
	bestDist := map[string]int{}
	for _, c := range candidates {
		d := strutil.Levenshtein(noun, c.display)
		if d <= 3 {
			if cur, ok := bestDist[c.canonical]; !ok || d < cur {
				bestDist[c.canonical] = d
			}
		}
	}
	suggestions := make([]string, 0, len(bestDist))
	for canonical := range bestDist {
		suggestions = append(suggestions, canonical)
	}
	sort.Slice(suggestions, func(i, j int) bool {
		di, dj := bestDist[suggestions[i]], bestDist[suggestions[j]]
		if di != dj {
			return di < dj
		}
		return suggestions[i] < suggestions[j]
	})
	if len(suggestions) > 0 {
		msg += "\n\nDid you mean: " + strings.Join(suggestions, ", ") + "?"
	}
	return errors.New(msg)
}

func addSetupFn(cmd *cobra.Command, vs VerbSpec, setup func(*cobra.Command, []string) error) {
	if vs.SkipSetup {
		return
	}
	cmd.PersistentPreRunE = setup
}

// AttachGlobalAuthFlags adds --profile, --org, and --project as persistent flags
// on cmd (intended to be the root command) so they can appear anywhere in the
// command line and are inherited by every subcommand.
func (r *Registry) AttachGlobalAuthFlags(cmd *cobra.Command) {
	f := cmd.PersistentFlags()
	f.String("profile", "", "Auth profile to use")
	f.String("org", "", "Harness org identifier (overrides profile default)")
	f.String("project", "", "Harness project identifier (overrides profile default)")
	wireProfileCompletion(cmd)
	r.wireGlobalFlagCompletion(cmd, "org", "organization")
	r.wireGlobalFlagCompletion(cmd, "project", "project")
}

// wireGlobalFlagCompletion wires completion for a global auth flag using the list endpoint for noun.
// It resolves auth from --profile (and --org for the project flag) already typed on the command line.
func (r *Registry) wireGlobalFlagCompletion(cmd *cobra.Command, flag, noun string) {
	listSpec := r.GetSpec(VerbList, noun)
	if listSpec == nil || listSpec.Endpoint == nil || listSpec.Endpoint.Completion == nil {
		return
	}
	ep := listSpec.Endpoint
	cspec := ep.Completion
	cmd.RegisterFlagCompletionFunc(flag, func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		ctx, err := r.buildCompletionCtx(cmd, VerbList, noun, "")
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		items, err := fetchCompletionItems(ctx, ep)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		completions, err := extractCompletions(items, cspec, exprenv.Make(ctx))
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	})
}

// buildUseString constructs the cobra Use string for a command spec.
func buildUseString(cs *spec.CommandSpec, vspec VerbSpec) string {
	use := cs.Verb
	if cs.Noun != "" {
		use = cs.FullNoun()
	}
	if vspec.RequiresId && !cs.NoId {
		idLabel := "<id>"
		if cs.IdLabel != "" {
			idLabel = cs.IdLabel
		}
		use += " " + idLabel
		if cs.HasArgs && cs.ArgsLabel != "" {
			use += " " + cs.ArgsLabel
		}
	} else if vspec.AllowsId {
		idLabel := "id"
		if cs.IdLabel != "" {
			idLabel = cs.IdLabel
		}
		use += " [" + idLabel + "]"
	} else if vspec.AllowsParentId {
		if cs.RequiresParentId {
			parentIdLabel := "<parentid>"
			if cs.ParentIdLabel != "" {
				parentIdLabel = cs.ParentIdLabel
			}
			use += " " + parentIdLabel
		} else {
			parentIdLabel := "[parentid]"
			if cs.ParentIdLabel != "" {
				parentIdLabel = "[" + cs.ParentIdLabel + "]"
			}
			use += " " + parentIdLabel
		}
	}
	return use
}

// buildCmd constructs a single cobra.Command from a CommandSpec.
func (r *Registry) buildCmd(cs *spec.CommandSpec) *cobra.Command {
	vspec := verbRegistry[cs.Verb]
	use := buildUseString(cs, vspec)
	cmd := &cobra.Command{
		Use:    use,
		Short:  cs.Short,
		Long:   cs.Long,
		Hidden: cs.Hidden,
	}

	r.bindHandler(cmd, cs)
	nd := r.GetNoun(cs.Noun)
	isMultiLevelList := vspec.AllowsParentId && nd != nil && nd.MultiLevel
	if vspec.RequiresId || (vspec.AllowsParentId && (cs.CompletionNoun != "" || len(cs.CompletionSeq) > 0)) || isMultiLevelList {
		r.wireCompletion(cmd, cs)
	}
	r.wireFlagCompletions(cmd, cs)
	return cmd
}

// buildAliasCmd constructs a hidden cobra.Command that delegates to the same handler as cs
// but uses aliasNoun as the subcommand name. Alias commands are hidden from help output.
func (r *Registry) buildAliasCmd(cs *spec.CommandSpec, aliasNoun string) *cobra.Command {
	vspec := verbRegistry[cs.Verb]
	use := aliasNoun
	if vspec.RequiresId && !cs.NoId {
		idLabel := "<id>"
		if cs.IdLabel != "" {
			idLabel = cs.IdLabel
		}
		use += " " + idLabel
		if cs.HasArgs && cs.ArgsLabel != "" {
			use += " " + cs.ArgsLabel
		}
	} else if vspec.AllowsId {
		idLabel := "id"
		if cs.IdLabel != "" {
			idLabel = cs.IdLabel
		}
		use += " [" + idLabel + "]"
	} else if vspec.AllowsParentId {
		if cs.RequiresParentId {
			parentIdLabel := "<parentid>"
			if cs.ParentIdLabel != "" {
				parentIdLabel = cs.ParentIdLabel
			}
			use += " " + parentIdLabel
		} else {
			parentIdLabel := "[parentid]"
			if cs.ParentIdLabel != "" {
				parentIdLabel = "[" + cs.ParentIdLabel + "]"
			}
			use += " " + parentIdLabel
		}
	}
	cmd := &cobra.Command{
		Use:    use,
		Short:  cs.Short,
		Long:   cs.Long,
		Hidden: true,
	}
	r.bindHandler(cmd, cs)
	nd := r.GetNoun(cs.Noun)
	isMultiLevelList := vspec.AllowsParentId && nd != nil && nd.MultiLevel
	if vspec.RequiresId || (vspec.AllowsParentId && (cs.CompletionNoun != "" || len(cs.CompletionSeq) > 0)) || isMultiLevelList {
		r.wireCompletion(cmd, cs)
	}
	r.wireFlagCompletions(cmd, cs)
	return cmd
}

// bindHandler wires the appropriate handler (workflow or endpoint) onto cmd.
func (r *Registry) bindHandler(cmd *cobra.Command, cs *spec.CommandSpec) {
	if cs.External {
		r.bindExternalCmd(cmd, cs)
		return
	}
	switch cs.HandlerType {
	case spec.HandlerWorkflow:
		if fn, ok := r.workflows[cs.WorkflowID]; ok {
			r.bindWorkflowCmd(cmd, cs, fn)
		} else {
			misconfiguredCmd(cmd, cs, "workflow %q not registered", cs.WorkflowID)
		}
	case spec.HandlerEndpoint:
		if cs.Endpoint != nil {
			r.bindEndpointCmd(cmd, cs)
		} else {
			misconfiguredCmd(cmd, cs, "HandlerEndpoint declared but Endpoint is nil")
		}
	}
}

// execPluginRunE returns a RunE that delegates to plugin.Exec (platform-specific).
func execPluginRunE(extBin, moduleName string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		binPath, err := plugin.FindBinary(extBin)
		if err != nil {
			var nfe *plugin.NotFoundError
			if errors.As(err, &nfe) {
				noun := strings.Fields(cmd.Use)[0]
				return fmt.Errorf("%q is provided by the %q module, which is not installed\n\nTo install it, run:\n  harness install module %s", noun, moduleName, moduleName)
			}
			return err
		}
		hlog.Debug("module exec", "binary", binPath, "args", os.Args[1:])
		return plugin.Exec(binPath, os.Args[1:])
	}
}

func misconfiguredCmd(cmd *cobra.Command, cs *spec.CommandSpec, fmtStr string, args ...any) {
	detail := fmt.Sprintf(fmtStr, args...)
	source := cs.Module
	if source == "" {
		source = "unknown"
	}
	msg := fmt.Sprintf("misconfigured command %q (source: %s): %s", cmd.Use, source, detail)
	cmd.RunE = func(*cobra.Command, []string) error {
		return errors.New(msg)
	}
}

// RunEndpoint implements cmdctx.Resolver.
func (r *Registry) RunEndpoint(ctx *cmdctx.Ctx, ep *spec.EndpointSpec) (any, error) {
	if ctx.VerbHandler == VerbList {
		return nil, RunListEndpoint(ctx, ep)
	}
	return RunEndpoint(ctx, ep)
}

// FormatList renders rows through the standard list formatting pipeline.
func (r *Registry) FormatList(ctx *cmdctx.Ctx, rows []any, fields []spec.FieldDef, columnIDs []string) error {
	tspec := buildTspec(columnIDs, fields)
	exprEnv := exprenv.Make(ctx)
	return format.FormatArrayOutput(ctx.FormatFlags, ctx.IsPty, rows, "it", tspec, fields, exprEnv, nil)
}

// FetchItems implements cmdctx.Resolver.
func (r *Registry) FetchItems(ctx *cmdctx.Ctx, ep *spec.EndpointSpec, pf cmdctx.PagingFlags) ([]any, error) {
	items, _, err := endpoint.FetchItems(ctx, ep, pf)
	return items, err
}

// bindExternalCmd registers flags for an external-module command and sets RunE
// to delegate to the plugin binary. Flags are registered via the normal bind
// path so --help shows the full flag list.
func (r *Registry) bindExternalCmd(cmd *cobra.Command, cs *spec.CommandSpec) {
	extBin := r.externalBinaryFor(cs.Module)
	if extBin == "" {
		panic(fmt.Sprintf("bindExternalCmd: no external binary registered for module %q", cs.Module))
	}
	switch cs.HandlerType {
	case spec.HandlerWorkflow:
		r.bindWorkflowCmd(cmd, cs, func(*cmdctx.Ctx) error { return nil })
	case spec.HandlerEndpoint:
		if cs.Endpoint != nil {
			r.bindEndpointCmd(cmd, cs)
		}
	}
	cmd.RunE = execPluginRunE(extBin, cs.Module)
}

// bindWorkflowCmd wires flags and RunE for a workflow-backed command.
func (r *Registry) bindWorkflowCmd(cmd *cobra.Command, cs *spec.CommandSpec, fn WorkflowFn) {
	addFlags(cmd.Flags(), specFormat, specJson, specYaml, specOut, specRaw)
	if cs.VerbHandler == VerbList {
		addFlags(cmd.Flags(), specColumns, specNoHeaders, specListColumns)
	}
	if cs.BuiltinFlags.Set {
		cmd.Flags().StringArray("set", nil, "Set a field value as key=value (repeatable)")
	}
	if cs.BuiltinFlags.Del {
		cmd.Flags().StringArray("del", nil, "Delete a field or field member (repeatable)")
	}
	if cs.BuiltinFlags.UI {
		addFlag(cmd.Flags(), specUI)
	}
	if nd := r.GetNoun(cs.Noun); nd != nil && nd.MultiLevel {
		addFlag(cmd.Flags(), specLevel)
		cmd.RegisterFlagCompletionFunc("level", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return specLevelValues, cobra.ShellCompDirectiveNoFileComp
		})
	}
	for _, f := range cs.Flags {
		if f.IsBool {
			if f.Short != "" {
				cmd.Flags().BoolP(f.Name, f.Short, false, f.Description)
			} else {
				cmd.Flags().Bool(f.Name, false, f.Description)
			}
		} else if f.IsMulti {
			cmd.Flags().StringArray(f.Name, nil, f.Description)
		} else {
			if f.Short != "" {
				cmd.Flags().StringP(f.Name, f.Short, f.Default, f.Description)
			} else {
				cmd.Flags().String(f.Name, f.Default, f.Description)
			}
		}
		if f.Hidden {
			cmd.Flags().MarkHidden(f.Name) //nolint:errcheck
		}
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx, err := buildCtx(cmd, cs, args, r)
		if err != nil {
			return err
		}
		return fn(ctx)
	}
}

// bindEndpointCmdFlags registers all flags for an endpoint-backed command.
func (r *Registry) bindEndpointCmdFlags(cmd *cobra.Command, cs *spec.CommandSpec) {
	ep := cs.Endpoint

	switch cs.VerbHandler {
	case VerbList:
		addFlags(cmd.Flags(), specFormat, specJson, specYaml, specColumns, specNoHeaders, specRaw, specListColumns)
	case VerbGet:
		addFlags(cmd.Flags(), specFormat, specJson, specYaml, specRaw, specFields, specListFields)
		cmd.RegisterFlagCompletionFunc("fields", func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			fields := r.ResolveCommandFields(cs)
			ids := make([]string, 0, len(fields))
			for _, f := range fields {
				ids = append(ids, f.ID)
			}
			return ids, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		})
	case VerbUpdate:
		if cs.BuiltinFlags.Set {
			addFlags(cmd.Flags(), specFormat, specJson, specYaml, specListFields)
		} else {
			addFlags(cmd.Flags(), specFormat, specJson, specYaml)
		}
	case VerbCreate:
		if ep.CreateStrategy == spec.CreateStrategySetFields {
			addFlags(cmd.Flags(), specFormat, specJson, specYaml, specListFields)
		} else {
			addFlags(cmd.Flags(), specFormat, specJson, specYaml)
		}
	default:
		addFlags(cmd.Flags(), specFormat, specJson, specYaml)
	}
	addFlag(cmd.Flags(), specOut)
	if cs.BuiltinFlags.Set {
		cmd.Flags().StringArray("set", nil, "Set a field value as key=value (repeatable)")
	}
	if cs.BuiltinFlags.Del {
		cmd.Flags().StringArray("del", nil, "Delete a field or field member (repeatable)")
	}
	if ep.FileBody == spec.FileBodyRequired {
		addFlag(cmd.Flags(), specFile)
		cmd.MarkFlagRequired("file")
	} else if ep.FileBody == spec.FileBodyOptional {
		addFlag(cmd.Flags(), specFile)
	}
	if cs.ConfirmMode != spec.ConfirmNone {
		cmd.Flags().Bool("force", false, "Skip confirmation prompt")
	}
	if nd := r.GetNoun(cs.Noun); nd != nil && nd.MultiLevel {
		addFlag(cmd.Flags(), specLevel)
		cmd.RegisterFlagCompletionFunc("level", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return specLevelValues, cobra.ShellCompDirectiveNoFileComp
		})
	}
	if cs.BuiltinFlags.Page {
		addFlag(cmd.Flags(), specPage)
	}
	if ep.Paging != nil {
		addFlags(cmd.Flags(), specOffset, specLimit, specAll, specCount)
		if !ep.Paging.IsCountable() {
			cmd.Flags().MarkHidden("all")
			cmd.Flags().MarkHidden("count")
		}
	}
	if cs.VerbHandler == VerbList && ep.Paging != nil {
		addFlag(cmd.Flags(), specUI)
	}
	if cs.VerbHandler == VerbGet && cs.BuiltinFlags.UI {
		addFlag(cmd.Flags(), specUI)
	}
	for _, f := range cs.Flags {
		if f.IsBool {
			if f.Short != "" {
				cmd.Flags().BoolP(f.Name, f.Short, false, f.Description)
			} else {
				cmd.Flags().Bool(f.Name, false, f.Description)
			}
		} else if f.IsMulti {
			cmd.Flags().StringArray(f.Name, nil, f.Description)
		} else {
			if f.Short != "" {
				cmd.Flags().StringP(f.Name, f.Short, f.Default, f.Description)
			} else {
				cmd.Flags().String(f.Name, f.Default, f.Description)
			}
		}
		if f.Hidden {
			cmd.Flags().MarkHidden(f.Name)
		}
	}
}

// bindEndpointCmd wires flags and RunE for an endpoint-backed command.
func (r *Registry) bindEndpointCmd(cmd *cobra.Command, cs *spec.CommandSpec) {
	r.bindEndpointCmdFlags(cmd, cs)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if cs.VerbHandler == VerbList {
			return r.runEndpointListCmd(cmd, cs, args)
		}
		return r.runEndpointCmd(cmd, cs, args)
	}
}

// runEndpointCmd executes an endpoint-backed command.
func (r *Registry) runEndpointCmd(cmd *cobra.Command, cs *spec.CommandSpec, args []string) error {
	ctx, err := buildCtx(cmd, cs, args, r)
	if err != nil {
		return err
	}
	if ctx.VerbHandler == VerbGet && cmdctx.GetBool(ctx.FlagValues, "ui") {
		id, err := RunUIPickerForGet(ctx, cs)
		if err != nil {
			return err
		}
		ctx.Id = id
	}
	if cs.ConfirmMode != spec.ConfirmNone {
		if err := runConfirmGate(cs.ConfirmMode, cs.Verb, cs.Noun, ctx.Id, ctx.IsPty, cmdctx.GetBool(ctx.FlagValues, "force")); err != nil {
			return err
		}
	}
	result, err := RunEndpoint(ctx, cs.Endpoint)
	if err != nil {
		return err
	}
	if cs.FollowFn != "" && cmdctx.GetBool(ctx.FlagValues, "follow") {
		if fn, ok := r.followFns[cs.FollowFn]; ok {
			return fn(ctx, result)
		}
	}
	return nil
}

// runEndpointListCmd executes a list endpoint-backed command.
func (r *Registry) runEndpointListCmd(cmd *cobra.Command, cs *spec.CommandSpec, args []string) error {
	ep := cs.Endpoint
	ctx, err := buildCtx(cmd, cs, args, r)
	if err != nil {
		return err
	}
	if cmdctx.GetBool(ctx.FlagValues, "list-columns") {
		fields := resolveFieldsForCommand(ctx, ep)
		w, closeW, err := format.OpenWriter(ctx.FormatFlags.OutFile)
		if err != nil {
			return err
		}
		defer closeW()
		return PrintFieldTable(w, fields)
	}
	if ep.Paging != nil {
		pf, err := buildPagingFlags(cmd.Flags(), ep.Paging)
		if err != nil {
			return err
		}
		ctx.PagingFlags = pf
	}
	if cmdctx.GetBool(ctx.FlagValues, "ui") {
		if !console.IsBothTTY() {
			return fmt.Errorf("--ui requires an interactive terminal (TTY)")
		}
		if cs.RequiresParentId && ctx.ParentId == "" {
			label := cs.ParentIdLabel
			if label == "" {
				label = "<parentid>"
			}
			return fmt.Errorf("%s %s requires a positional %s argument", cs.Verb, cs.Noun, label)
		}
		return RunUITable(ctx, ep)
	}
	return RunListEndpoint(ctx, ep)
}

// buildFlagValues extracts typed values from cmd.Flags() for every flag declared in cs.Flags,
// plus a set of well-known builtins when present on the flag set:
//   - "file"         string  when the -f/--file flag is registered (endpoint file_body)
//   - "page"         int     (0-indexed) when cs.BuiltinFlags.Page is true
//   - "format"       string  always when the flag exists (gates PTY-sensitive expr rendering)
//   - "columns"      string  when the flag exists (list commands)
//   - "list-columns" bool    when the flag exists (list commands)
//   - "list-fields"  bool    when the flag exists (get/update commands)
//   - "profile", "org", "project" string when cs.NoAuth is true
func buildFlagValues(flags *pflag.FlagSet, cs *spec.CommandSpec) map[string]any {
	fv := make(map[string]any, len(cs.Flags)+1)
	for _, f := range cs.Flags {
		switch {
		case f.IsBool:
			v, _ := flags.GetBool(f.Name)
			fv[f.Name] = v
		case f.IsMulti:
			v, _ := flags.GetStringArray(f.Name)
			fv[f.Name] = v
		case f.IsArray:
			v, _ := flags.GetString(f.Name)
			fv[f.Name] = parseArrayFlag(v)
		default:
			v, _ := flags.GetString(f.Name)
			fv[f.Name] = v
		}
	}
	if cs.BuiltinFlags.Page {
		if n, err := flags.GetInt("page"); err == nil {
			fv["page"] = n - 1
		}
	}
	for _, name := range []string{"format", "columns"} {
		if f := flags.Lookup(name); f != nil {
			fv[name] = f.Value.String()
		}
	}
	for _, name := range []string{"list-columns", "list-fields", "ui", "force"} {
		if flags.Lookup(name) != nil {
			v, _ := flags.GetBool(name)
			fv[name] = v
		}
	}
	if flags.Lookup("file") != nil {
		v, _ := flags.GetString("file")
		fv["file"] = v
	}
	if cs.NoAuth {
		for _, name := range []string{"profile", "org", "project"} {
			if _, already := fv[name]; !already {
				v, _ := flags.GetString(name)
				fv[name] = v
			}
		}
	}
	return fv
}

// buildPagingFlags reads paging flags from the flag set and validates mutual exclusions.
func buildPagingFlags(flags *pflag.FlagSet, pg *spec.PagingSpec) (cmdctx.PagingFlags, error) {
	offset, _ := flags.GetInt("offset")
	limit, _ := flags.GetInt("limit")
	all, _ := flags.GetBool("all")
	count, _ := flags.GetBool("count")
	if offset < 0 {
		return cmdctx.PagingFlags{}, fmt.Errorf("--offset must be non-negative")
	}
	if limit < 0 {
		return cmdctx.PagingFlags{}, fmt.Errorf("--limit must be non-negative")
	}
	if all && !pg.IsCountable() {
		return cmdctx.PagingFlags{}, fmt.Errorf("--all is not supported for this resource")
	}
	if count && !pg.IsCountable() {
		return cmdctx.PagingFlags{}, fmt.Errorf("--count is not supported for this resource")
	}
	if all && (offset != 0 || limit != 0) {
		return cmdctx.PagingFlags{}, fmt.Errorf("--all is incompatible with --offset and --limit")
	}
	if count && (offset != 0 || limit != 0 || all) {
		return cmdctx.PagingFlags{}, fmt.Errorf("--count is incompatible with --offset, --limit, and --all")
	}
	pf := cmdctx.PagingFlags{
		Offset: offset,
		Limit:  limit,
		All:    all,
		Count:  count,
	}
	hlog.Debug("buildPagingFlags", "offset", pf.Offset, "limit", pf.Limit, "all", pf.All, "count", pf.Count)
	return pf, nil
}
