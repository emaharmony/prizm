// Package main implements the `prizm workflow start` subcommand — the
// interactive trigger for the gated loop. It publishes a start request to a
// running Prizm serve instance over NATS, which runs the full 7-phase loop.
//
// Usage:
//
//	prizm workflow start --project <id> --prompt "build feature X"
//	prizm workflow start --prompt "..." --nats nats://127.0.0.1:4222
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/workstart"
	"github.com/nats-io/nats.go"
)

func executeWorkflowStart(args []string) {
	cmd := flag.NewFlagSet("workflow start", flag.ExitOnError)
	configPath := cmd.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	project := cmd.String("project", "", "Project ID to work on")
	prompt := cmd.String("prompt", "", "The task prompt that seeds the gated loop")
	repoPath := cmd.String("repo-path", "", "Repository path to work in (must be inside a configured write root)")
	pathAlias := cmd.String("path", "", "Alias for --repo-path")
	bootstrap := cmd.Bool("bootstrap", false, "Create the repo directory and run git init when the confirmed path is new")
	channel := cmd.String("channel", "", "Channel ID to post results to (defaults to the project channel)")
	natsURL := cmd.String("nats", "", "NATS URL (defaults to prizm.nats_url or nats://127.0.0.1:4222)")
	cmd.Parse(args)

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "Error: --prompt is required")
		fmt.Fprintln(os.Stderr, `Usage: prizm workflow start --project <id> --prompt "build feature X"`)
		os.Exit(1)
	}
	if *repoPath == "" {
		*repoPath = *pathAlias
	}

	var cfg *orchestrator.Config
	if loaded, err := orchestrator.LoadConfig(*configPath); err == nil {
		cfg = loaded
	}
	req := workstart.Request{
		Project:   *project,
		Prompt:    *prompt,
		RepoPath:  *repoPath,
		Channel:   *channel,
		Source:    "cli",
		Bootstrap: *bootstrap,
	}
	resolved, err := workstart.Resolve(cfg, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if resolved.NeedsLocation {
		fmt.Fprintln(os.Stderr, "Error: project location required before Prizm can create files.")
		if resolved.Recommendation != "" {
			fmt.Fprintf(os.Stderr, "Recommended path: %s\n", resolved.Recommendation)
			fmt.Fprintln(os.Stderr, "Retry with --path above, or choose another path inside prizm.write_roots.")
		}
		os.Exit(2)
	}
	req.Project = resolved.ProjectID
	req.RepoPath = resolved.RepoPath
	req.Channel = resolved.Channel
	req.Bootstrap = req.Bootstrap || resolved.Project == nil

	// Resolve the NATS URL from config when not given explicitly.
	url := *natsURL
	if url == "" {
		if cfg != nil && cfg.Prizm.NATSURL != "" {
			url = cfg.Prizm.NATSURL
		}
	}
	if url == "" {
		url = "nats://127.0.0.1:4222"
	}

	nc, err := nats.Connect(url, nats.Name("prizm-workflow-start"), nats.Timeout(5*time.Second))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not connect to NATS at %s: %v\n", url, err)
		fmt.Fprintln(os.Stderr, "Is `prizm serve` running with an external NATS (prizm.nats_url)?")
		os.Exit(1)
	}
	defer nc.Close()

	payload, _ := json.Marshal(req)
	if err := nc.Publish("prizm.workflow.start", payload); err != nil {
		fmt.Fprintf(os.Stderr, "Error publishing start request: %v\n", err)
		os.Exit(1)
	}
	if err := nc.FlushTimeout(5 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "Error flushing to NATS: %v\n", err)
		os.Exit(1)
	}

	target := req.Project
	if target == "" {
		target = "(default project)"
	}
	fmt.Printf("🔁 Gated loop start requested for %s\n", target)
	fmt.Printf("   repo: %s\n", req.RepoPath)
	fmt.Printf("   prompt: %s\n", req.Prompt)
	fmt.Println("   The running Prizm instance will execute the loop and post results.")
}
