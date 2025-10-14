package main

import (
	"context"

	"github.com/sethvargo/go-githubactions"
	"github.com/yhabteab/backbot/backport"
	"github.com/yhabteab/backbot/git"
)

func main() {
	cfg, err := backport.LoadInputsFromEnv()
	if err != nil {
		githubactions.Fatalf("Failed to load inputs from environment: %v", err)
	}
	githubactions.AddMask(cfg.GitHubToken) // Mask the GitHub token in logs

	ghCtx, err := githubactions.Context()
	if err != nil {
		githubactions.Infof("Failed to retrieve GitHub context: %v", err)
	}
	if ghCtx.EventName != "pull_request" && ghCtx.EventName != "pull_request_target" {
		githubactions.Fatalf("backbot only supports 'pull_request' and 'pull_request_target' events, got: %s", ghCtx.EventName)
	}

	if err := git.Configure(ghCtx, cfg.Committer, cfg.Email); err != nil {
		githubactions.Fatalf("Failed to configure git: %v", err)
	}
	backport.Run(context.Background(), cfg, ghCtx)
}
