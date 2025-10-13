package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/go-github/v75/github"
	"github.com/sethvargo/go-githubactions"
)

// Client wraps the GitHub client to provide methods for interacting with GitHub API.
type Client struct {
	client    *github.Client               // GitHub API client
	githubCtx *githubactions.GitHubContext // GitHub Actions context

	git *github.GitService // GitHub Git service

	commitsPRCache map[int64][]*github.RepositoryCommit // Cache for commits in PRs to avoid redundant API calls.
}

// NewClient creates a new GitHub client with the provided authentication token.
func NewClient(ghCtx *githubactions.GitHubContext, githubToken string) *Client {
	c := &Client{
		client:         github.NewClient(nil).WithAuthToken(githubToken),
		githubCtx:      ghCtx,
		commitsPRCache: make(map[int64][]*github.RepositoryCommit),
	}
	c.git = c.client.Git
	return c
}

// Repo fetches the owner and repository name from the GitHub context.
func (c *Client) Repo() (string, string) { return c.githubCtx.Repo() }

// GetPrNumber fetches the pull request number from the event context.
//
// Returns the pull request number or an error if the operation fails.
func (c *Client) GetPrNumber() (int64, error) {
	if c.githubCtx.EventName != "pull_request" {
		return 0, fmt.Errorf("event is not a pull request")
	}
	if c.githubCtx.Event == nil {
		return 0, fmt.Errorf("event payload is nil")
	}
	pr, ok := c.githubCtx.Event["pull_request"]
	if !ok {
		return 0, fmt.Errorf("pull_request field not found in event payload")
	}
	prMap, ok := pr.(map[string]any)
	if !ok {
		return 0, fmt.Errorf("pull_request field is not a map")
	}
	number, ok := prMap["number"]
	if !ok {
		return 0, fmt.Errorf("pull_request.number field not found in event payload")
	}
	return strconv.ParseInt(fmt.Sprint(number), 10, 64)
}

// GetPR fetches a pull request by its number.
//
// Returns the pull request object or an error if the operation fails.
func (c *Client) GetPR(ctx context.Context, prNumber int64) (*github.PullRequest, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Retrieving PR #%d from %s/%s", prNumber, owner, repo)

	pr, resp, err := c.client.PullRequests.Get(ctx, owner, repo, int(prNumber))
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return pr, nil
}

// CreatePR creates a new pull request in the repository.
//
// Returns the created pull request object or an error if the operation fails.
func (c *Client) CreatePR(ctx context.Context, pr *github.NewPullRequest) (*github.PullRequest, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Creating PR in %s/%s", owner, repo)

	createdPr, resp, err := c.client.PullRequests.Create(ctx, owner, repo, pr)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return createdPr, nil
}

// IsCommitInPR checks if a commit with the given SHA is part of the specified pull request.
//
// Returns true if the commit is found in the pull request, false otherwise.
// Returns an error if the operation fails.
func (c *Client) IsCommitInPR(ctx context.Context, pr *github.PullRequest, sha string) (bool, error) {
	commits, err := c.GetCommits(ctx, pr)
	if err != nil {
		return false, err
	}
	for _, commit := range commits {
		if commit.GetSHA() == sha {
			return true, nil
		}
	}
	return false, nil
}

// LabelPR adds the specified labels to a pull request.
//
// Returns the added labels or an error if the operation fails.
func (c *Client) LabelPR(ctx context.Context, pr *github.PullRequest, labels ...string) ([]*github.Label, error) {
	if len(labels) == 0 {
		return nil, nil
	}

	repo, owner := c.Repo()
	githubactions.Infof("Adding labels '%+v' to PR #%d in %s/%s", labels, pr.GetNumber(), owner, repo)

	ghLabels, resp, err := c.client.Issues.AddLabelsToIssue(ctx, owner, repo, pr.GetNumber(), labels)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return ghLabels, nil
}

// ListFiles lists the files changed in the specified pull request.
//
// It will iteratively fetch files in pages until all files are retrieved.
//
// Returns a slice of pull request files or an error if the operation fails.
func (c *Client) ListFiles(ctx context.Context, pr *github.PullRequest) ([]*github.CommitFile, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Retrieving files for PR #%d from %s/%s", pr.GetNumber(), owner, repo)
	if pr.GetChangedFiles() == 0 {
		return nil, nil
	}

	var allFiles []*github.CommitFile
	opts := &github.ListOptions{PerPage: 100}
	for {
		files, resp, err := c.client.PullRequests.ListFiles(ctx, owner, repo, pr.GetNumber(), opts)
		if err != nil {
			return nil, err
		}
		allFiles = append(allFiles, files...)
		if resp.NextPage == 0 {
			closeResponseBody(resp)
			break
		}
		opts.Page = resp.NextPage
		closeResponseBody(resp)
	}
	return allFiles, nil
}

// MergeKind returns the merge strategy used to merge the given pull request.
//
// It can be [MergeCommit], [Squash], [Rebase] or [MergeInvalid] if the PR is not merged or
// the merge strategy cannot be determined.
func (c *Client) MergeKind(ctx context.Context, pr *github.PullRequest) (MergeKind, error) {
	if !pr.GetMerged() {
		return MergeInvalid, nil
	}
	if mergeCommitSHA := pr.GetMergeCommitSHA(); mergeCommitSHA != "" {
		mergeCommit, err := c.GetCommit(ctx, mergeCommitSHA) // This also fetches the changed files in the commit
		if err != nil {
			return MergeInvalid, fmt.Errorf("failed to get parents of merge commit %s: %w", mergeCommitSHA, err)
		}
		if IsMergeCommit(mergeCommit) {
			return MergeCommit, nil
		}
		if pr.GetCommits() == 1 { // Single commit PRs are treated as squash merges
			return Squash, nil
		}

		files, err := c.ListFiles(ctx, pr)
		if err != nil {
			return MergeInvalid, fmt.Errorf("failed to list changed files in PR #%d: %w", pr.GetNumber(), err)
		}

		if len(files) == len(mergeCommit.Files) {
			for i, file := range files {
				if !compareCommitFile(file, mergeCommit.Files[i]) {
					return Rebase, nil // Files differ, must be a rebase
				}
			}
			return Squash, nil // Files are identical, must be a squash
		}
		return Rebase, nil
	}
	return MergeInvalid, nil
}

// CreateComment adds a comment to the specified issue or pull request.
//
// Returns an error if the operation fails.
func (c *Client) CreateComment(ctx context.Context, issueNumber int64, body string) error {
	owner, repo := c.Repo()
	githubactions.Infof("Creating comment on issue/PR #%d in %s/%s", issueNumber, owner, repo)

	comment := &github.IssueComment{Body: github.Ptr(body)}
	_, resp, err := c.client.Issues.CreateComment(ctx, owner, repo, int(issueNumber), comment)
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return nil
}
