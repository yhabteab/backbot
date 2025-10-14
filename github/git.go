package github

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/google/go-github/v75/github"
	"github.com/sethvargo/go-githubactions"
)

// GetCommit fetches a single commit by its SHA.
//
// Returns the commit object or an error if the operation fails.
func (c *Client) GetCommit(ctx context.Context, sha string) (*github.RepositoryCommit, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Retrieving commit %s from %s/%s", sha, owner, repo)

	commit, resp, err := c.client.Repositories.GetCommit(ctx, owner, repo, sha, nil)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return commit, nil
}

// GetCommits fetches all commits associated with a pull request.
//
// It will iteratively fetch commits in pages until all commits are retrieved.
//
// Returns a slice of commits or an error if the operation fails.
func (c *Client) GetCommits(ctx context.Context, pr *github.PullRequest) ([]*github.RepositoryCommit, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Retrieving commits for PR #%d from %s/%s", pr.GetNumber(), owner, repo)
	if pr.GetCommits() == 0 {
		return nil, nil
	}

	// Reuse cached commits if available to avoid redundant API calls
	if commits, ok := c.commitsPRCache[int64(pr.GetNumber())]; ok {
		return commits, nil
	}

	var allCommits []*github.RepositoryCommit
	opts := &github.ListOptions{PerPage: 100}
	for {
		commits, resp, err := c.client.PullRequests.ListCommits(ctx, owner, repo, pr.GetNumber(), opts)
		if err != nil {
			return nil, err
		}
		allCommits = append(allCommits, commits...)
		if resp.NextPage == 0 || float64(resp.NextPage) > math.Ceil(float64(pr.GetCommits())/100) {
			closeResponseBody(resp)
			break
		}
		opts.Page = resp.NextPage
		closeResponseBody(resp)
	}
	c.commitsPRCache[int64(pr.GetNumber())] = allCommits // Cache the commits for future use
	return allCommits, nil
}
