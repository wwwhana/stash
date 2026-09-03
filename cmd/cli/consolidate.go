package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alash3al/stash/internal/brain"
	"github.com/urfave/cli/v3"
)

func consolidateRunCmd(ctx context.Context, cmd *cli.Command) error {
	namespaces, err := explicitConsolidationNamespaces(cmd.StringSlice("namespaces"))
	if err != nil {
		return err
	}
	dryRun := cmd.Bool("dry-run")

	bc := getBootstrap(cmd)

	if dryRun {
		return printJSON(map[string]any{
			"namespaces": namespaces,
			"status":     "dry-run requested",
		})
	}

	ids, err := bc.Brain.ResolveNamespaceIDs(ctx, namespaces)
	if err != nil {
		return fmt.Errorf("resolve namespaces: %w", err)
	}

	var results []brain.ConsolidationResult
	for _, id := range ids {
		result, err := bc.Brain.ConsolidateByID(ctx, id)
		if err != nil {
			log.Printf("Consolidation failed for namespace ID %d: %v", id, err)
			continue
		}
		results = append(results, result)
		bc.Logger.Info("consolidation completed", "result", result)
	}

	return printJSON(results)
}

func consolidateServeCmd(ctx context.Context, cmd *cli.Command) error {
	interval := cmd.Duration("interval")
	namespaces, err := explicitConsolidationNamespaces(cmd.StringSlice("namespaces"))
	if err != nil {
		return err
	}

	bc := getBootstrap(cmd)

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Printf("Starting consolidation service with interval %s", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ids, err := bc.Brain.ResolveNamespaceIDs(ctx, namespaces)
			if err != nil {
				log.Printf("Failed to resolve namespaces: %v", err)
				continue
			}
			for _, id := range ids {
				result, err := bc.Brain.ConsolidateByID(ctx, id)
				if err != nil {
					log.Printf("Consolidation failed for namespace ID %d: %v", id, err)
					continue
				}
				b, err := json.Marshal(result)
				if err != nil {
					log.Printf("failed to encode consolidation result for %s: %v", result.Namespace, err)
					continue
				}
				log.Printf("Consolidation completed for %s: %s", result.Namespace, string(b))
			}
		case <-ctx.Done():
			log.Printf("Consolidation service shutting down")
			return nil
		}
	}
}

func explicitConsolidationNamespaces(values []string) ([]string, error) {
	seen := make(map[string]struct{})
	namespaces := make([]string, 0, len(values))
	for _, value := range values {
		for _, namespace := range strings.Split(value, ",") {
			namespace = strings.TrimSpace(namespace)
			if namespace == "" {
				continue
			}
			if namespace == "/" {
				return nil, fmt.Errorf("consolidation namespace / is not allowed; select one or more non-root namespaces")
			}
			if !strings.HasPrefix(namespace, "/") {
				return nil, fmt.Errorf("consolidation namespace %q must start with /", namespace)
			}
			if _, ok := seen[namespace]; ok {
				continue
			}
			seen[namespace] = struct{}{}
			namespaces = append(namespaces, namespace)
		}
	}
	if len(namespaces) == 0 {
		return nil, fmt.Errorf("at least one non-root consolidation namespace is required")
	}
	return namespaces, nil
}
