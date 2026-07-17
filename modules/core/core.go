// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"github.com/harness/cli/modules/core/auth"
	"github.com/harness/cli/modules/core/mgmt"
	"github.com/harness/cli/pkg/registry"
)

const (
	// auth
	loginHandlerID       = "login"
	loginSSOHandlerID    = "loginsso"
	logoutHandlerID      = "logout"
	statusHandlerID      = "status"
	setHandlerID         = "set"
	envHandlerID         = "env"
	tokenHandlerID       = "token"
	ssoRefreshHandlerID  = "sso_refresh"
	ssoStatusHandlerID   = "sso_status"
	debugSSOLogHandlerID = "debug_sso_log"
	profilesFetchFnID    = "profiles_fetch"

	// mgmt
	debugUpdateCheckHandlerID = "debug_update_check"
	versionHandlerID          = "version"
	askHandlerID              = "ask"
	installCLIHandlerID       = "install_cli"
	installModuleHandlerID    = "install_module"
	listModulesFetchFnID      = "list_modules_fetch"
	getModuleHandlerID        = "get_module"
	listNounsFetchFnID        = "list_nouns_fetch"
	getNounHandlerID          = "get_noun"
)

func ModuleInit(reg registry.ModuleRegistrar) {
	reg.RegisterWorkflow(loginHandlerID, auth.LoginHandler)
	reg.RegisterWorkflow(loginSSOHandlerID, auth.LoginSSOHandler)
	reg.RegisterWorkflow(logoutHandlerID, auth.LogoutHandler)
	reg.RegisterWorkflow(statusHandlerID, auth.StatusHandler)
	reg.RegisterWorkflow(setHandlerID, auth.SetHandler)
	reg.RegisterWorkflow(envHandlerID, auth.EnvHandler)
	reg.RegisterWorkflow(tokenHandlerID, auth.TokenHandler)
	reg.RegisterWorkflow(ssoRefreshHandlerID, auth.SSORefreshHandler)
	reg.RegisterWorkflow(ssoStatusHandlerID, auth.SSOStatusHandler)
	reg.RegisterWorkflow(debugSSOLogHandlerID, auth.DebugSSOLogHandler)
	reg.RegisterFetchFn(profilesFetchFnID, auth.ProfilesFetchFn)
	reg.RegisterWorkflow(debugUpdateCheckHandlerID, mgmt.DebugUpdateCheckHandler)
	reg.RegisterWorkflow(versionHandlerID, mgmt.VersionHandler)
	reg.RegisterWorkflow(askHandlerID, mgmt.AskHandler)
	reg.RegisterWorkflow(installCLIHandlerID, mgmt.InstallCLIHandler)
	reg.RegisterWorkflow(installModuleHandlerID, mgmt.InstallModuleHandler)
	reg.RegisterFetchFn(listModulesFetchFnID, mgmt.ListModulesFetchFn)
	reg.RegisterWorkflow(getModuleHandlerID, mgmt.GetModuleHandler)
	reg.RegisterFetchFn(listNounsFetchFnID, mgmt.ListNounsFetchFn)
	reg.RegisterWorkflow(getNounHandlerID, mgmt.GetNounHandler)
}
