// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package har

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/harness/harness-cli/pkg/cmdctx"
	"github.com/harness/harness-cli/modules/har/pkg/har/migrate"
	"github.com/harness/harness-cli/modules/har/pkg/har/migrate/types"
)

const executeRegistryMigrateHandlerID = "execute_registry_migrate"

func executeRegistryMigrateHandler(ctx *cmdctx.Ctx) error {
	a := ctx.Auth
	filePath := cmdctx.GetString(ctx.FlagValues, "config")
	concurrencyStr := cmdctx.GetString(ctx.FlagValues, "concurrency")
	overwrite := cmdctx.GetBool(ctx.FlagValues, "overwrite")
	dryRun := cmdctx.GetBool(ctx.FlagValues, "dry-run")

	cfg, err := types.LoadConfig(filePath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if concurrencyStr != "" {
		if n, err := strconv.Atoi(concurrencyStr); err == nil && n > 0 {
			cfg.Concurrency = n
		}
	}
	if overwrite {
		cfg.Overwrite = true
	}
	if dryRun {
		cfg.DryRun = true
	}

	// Thread auth context into the destination (HAR) registry config.
	cfg.Dest.AccountID = a.AccountID
	cfg.Dest.APIBaseURL = a.APIUrl

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("\nInterrupted. Shutting down gracefully...")
		cancel()
	}()

	svc, err := migrate.NewMigrationService(bgCtx, cfg)
	if err != nil {
		return fmt.Errorf("creating migration service: %w", err)
	}

	if err := svc.Run(bgCtx); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	fmt.Println("Migration completed successfully.")
	return nil
}
