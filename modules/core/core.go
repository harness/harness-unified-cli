// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"github.com/harness/harness-cli/modules/core/auth"
	"github.com/harness/harness-cli/modules/core/mgmt"
	"github.com/harness/harness-cli/pkg/registry"
)

const (
	// auth
	loginHandlerID    = "login"
	loginSSOHandlerID = "loginsso"
	logoutHandlerID   = "logout"
	statusHandlerID   = "status"
	setHandlerID      = "set"
	envHandlerID      = "env"
	tokenHandlerID    = "token"
	profilesFetchFnID = "profiles_fetch"

	// mgmt
	versionHandlerID      = "version"
	askHandlerID          = "ask"
	installCLIHandlerID   = "install_cli"
	installModuleHandlerID = "install_module"
	listModulesFetchFnID  = "list_modules_fetch"
	getModuleHandlerID    = "get_module"
	listNounsFetchFnID    = "list_nouns_fetch"
	getNounHandlerID      = "get_noun"
)

func ModuleInit(reg registry.ModuleRegistrar) {
	reg.RegisterWorkflow(loginHandlerID, auth.LoginHandler)
	reg.RegisterWorkflow(loginSSOHandlerID, auth.LoginSSOHandler)
	reg.RegisterWorkflow(logoutHandlerID, auth.LogoutHandler)
	reg.RegisterWorkflow(statusHandlerID, auth.StatusHandler)
	reg.RegisterWorkflow(setHandlerID, auth.SetHandler)
	reg.RegisterWorkflow(envHandlerID, auth.EnvHandler)
	reg.RegisterWorkflow(tokenHandlerID, auth.TokenHandler)
	reg.RegisterFetchFn(profilesFetchFnID, auth.ProfilesFetchFn)
	reg.RegisterWorkflow(versionHandlerID, mgmt.VersionHandler)
	reg.RegisterWorkflow(askHandlerID, mgmt.AskHandler)
	reg.RegisterWorkflow(installCLIHandlerID, mgmt.InstallCLIHandler)
	reg.RegisterWorkflow(installModuleHandlerID, mgmt.InstallModuleHandler)
	reg.RegisterFetchFn(listModulesFetchFnID, mgmt.ListModulesFetchFn)
	reg.RegisterWorkflow(getModuleHandlerID, mgmt.GetModuleHandler)
	reg.RegisterFetchFn(listNounsFetchFnID, mgmt.ListNounsFetchFn)
	reg.RegisterWorkflow(getNounHandlerID, mgmt.GetNounHandler)
}
