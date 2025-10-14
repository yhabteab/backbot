package backport

import (
	"fmt"
	"regexp"

	"github.com/icinga/icinga-go-library/config"
)

const (
	MergeCommitHandlingSkip  = "skip"  // Skip merge commits when cherry-picking.
	MergeCommitHandlingAbort = "abort" // Abort the backport if any merge commits are found.

	ConflictHandlingAbort = "abort" // Abort the backport if there are conflicts.
	ConflictHandlingDraft = "draft" // Create a draft PR if there are conflicts.
)

// Input represents the inputs to the GitHub Action.
type Input struct {
	// GitHubToken is the GitHub token to use for authentication.
	//
	// The environment variable will be unset after loading the inputs to prevent accidental exposure.
	GitHubToken string `env:"GITHUB_TOKEN,unset"`

	// Committer is the name of the committer to use for git commits.
	Committer string `env:"COMMITTER"`

	// Email is the email of the committer to use for git commits.
	Email string `env:"COMMITTER_EMAIL"`

	// Title is the title of the backport pull request.
	Title string `env:"PR_TITLE"`

	// Description is the description of the backport pull request.
	Description string `env:"PR_DESCRIPTION"`

	// CopyLabelsPattern is a regex pattern to match labels that should be copied from the original pull request
	// to the backport pull request. If not set, none are copied.
	CopyLabelsPattern string `env:"COPY_LABELS_PATTERN"`

	// copyLabelRegex is the compiled regex from CopyLabelsPattern. This is not set from environment variables.
	copyLabelRegex *regexp.Regexp `env:"-"`

	// LabelPattern is a regex pattern to match labels that should be used to determine target branches for backporting.
	//
	// The part of the label that matches the first capturing group will be used as the target branch name.
	// For example, if you set this to `backport-to-(support\/\d+\.\d+)`, and the original pull request has
	// a label `backport-to-support/2.15`, a backport will be created to the `support/2.15` branch.
	//
	// By default, this is set to `backport-to-(support\/\d+\.\d+)`.
	LabelPattern string `env:"LABEL_PATTERN" default:"backport-to-(support\\/\\d+\\.\\d+)"`

	// labelRegex is the compiled regex from LabelPattern. This is not set from environment variables.
	labelRegex *regexp.Regexp `env:"-"`

	// ConflictHandling determines how to handle conflicts during cherry-picking.
	//
	// You can set this to "abort" to abort the backport if there are conflicts, or "draft" to create
	// a draft pull request that needs to be resolved manually. Defaults to "abort".
	ConflictHandling string `env:"CONFLICT_HANDLING"`

	// MergeCommitHandling determines whether to skip merge commits when cherry-picking from the source pull request.
	//
	// This is used control the behaviour for when the source pull request has any merge commits in its history
	// due to merges from some other pull requests, and you want to either include, skip them or fail the backport
	// if any are found. Can be "skip", or "abort" and any other value is treated as "include". Defaults to "skip".
	MergeCommitHandling string `env:"MERGE_COMMIT_HANDLING" default:"skip"`
}

// Validate checks that the required fields are set and valid.
func (in *Input) Validate() error {
	if in.GitHubToken == "" {
		return fmt.Errorf("github_token is required")
	}
	if in.Committer == "" {
		return fmt.Errorf("committer is required")
	}
	if in.Email == "" {
		return fmt.Errorf("committer email is required")
	}
	if in.Title == "" {
		return fmt.Errorf("pr_title is required")
	}
	if in.Description == "" {
		return fmt.Errorf("pr_description is required")
	}
	if in.CopyLabelsPattern != "" {
		re, err := regexp.Compile(in.CopyLabelsPattern)
		if err != nil {
			return fmt.Errorf("failed to compile copy_labels_pattern regex: %w", err)
		}
		in.copyLabelRegex = re
	}
	if in.LabelPattern == "" {
		return fmt.Errorf("label_pattern is required")
	}
	re, err := regexp.Compile(in.LabelPattern)
	if err != nil {
		return fmt.Errorf("failed to compile label_pattern regex: %w", err)
	}
	in.labelRegex = re

	if in.ConflictHandling != "abort" && in.ConflictHandling != "draft" {
		return fmt.Errorf("expected input 'conflict_handling' to be either 'abort' or 'draft', got: '%s'", in.ConflictHandling)
	}
	if in.MergeCommitHandling == "" {
		return fmt.Errorf("merge_commit_handling is required")
	}
	return nil
}

// LoadInputsFromEnv loads and validates the action inputs from environment variables.
func LoadInputsFromEnv() (*Input, error) {
	var input Input
	if err := config.FromEnv(&input, config.EnvOptions{Prefix: "INPUT_"}); err != nil {
		return nil, fmt.Errorf("failed to load action inputs from environment: %w", err)
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	return &input, nil
}
