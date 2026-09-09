package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/apecloud/kubeblocks/cmd/upgrader/steps"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// Parse command-line arguments.
	var modulesFlag string
	flag.StringVar(&modulesFlag, "modules", "core", "modules to run, separated by commas: core,clickhouse,dbfix")
	flag.Parse()

	modules := make(map[string]bool)
	for _, m := range strings.FieldsFunc(modulesFlag, func(r rune) bool { return r == ',' || r == 0xFF0C }) {
		m = strings.TrimSpace(m)
		if m != "" {
			modules[m] = true
		}
	}
	modules["core"] = true

	// Create the working directory.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}

	workDir := filepath.Join(home, ".kb-upgrader", "workdir")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return fmt.Errorf("failed to create working directory: %w", err)
	}

	// Print the startup banner.
	var moduleNames []string
	for m := range modules {
		moduleNames = append(moduleNames, m)
	}
	sort.Strings(moduleNames)

	fmt.Println("========================================")
	fmt.Println("  KubeBlocks Upgrader (v0.8.x -> v0.9.3)")
	fmt.Printf("  Modules: %s\n", strings.Join(moduleNames, ", "))
	fmt.Println("========================================")

	// Register all steps.
	allSteps := steps.RegisterAll(modules)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := steps.RunOptions{
		Ctx:     ctx,
		WorkDir: workDir,
	}

	for i, step := range allSteps {
		fmt.Printf("\n[%d/%d] %s - %s\n", i+1, len(allSteps), step.Name(), step.Description())

		if skip, err := step.Check(opts); err != nil {
			return fmt.Errorf("check failed for %s: %w\nupgrade paused; fix the issue and run the upgrader again", step.Name(), err)
		} else if skip {
			fmt.Println("       already in the expected state; skipping")
			continue
		}

		if err := step.Run(opts); err != nil {
			return fmt.Errorf("step %s failed: %w\nupgrade paused; fix the issue and run the upgrader again", step.Name(), err)
		}

		fmt.Println("       completed")
	}

	fmt.Printf("\n========================================\n")
	fmt.Printf("  All %d steps completed.\n", len(allSteps))
	fmt.Println("========================================")

	if err := os.RemoveAll(workDir); err != nil {
		return fmt.Errorf("failed to remove working directory: %w", err)
	}
	return nil
}
