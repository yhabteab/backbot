package github

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/google/go-github/v75/github"
	"github.com/sethvargo/go-githubactions"
)

// ErrConflict is returned when a merge conflict occurs while backporting a commit to a target branch.
var ErrConflict = fmt.Errorf("merge conflict")

// GetRef retrieves the specified git reference (branch) from the repository.
//
// It returns the reference object or an error if the operation fails.
func (c *Client) GetRef(ctx context.Context, head string) (*github.Reference, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Retrieving branch %s from %s/%s", head, owner, repo)

	ref, resp, err := c.git.GetRef(ctx, owner, repo, "heads/"+head)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return ref, nil
}

// CreateRef is a wrapper around the GitHub API to create a new git reference (branch).
//
// It takes the context, branch name, and the SHA of the commit to point the new branch to.
// It returns the created reference or an error if the operation fails.
func (c *Client) CreateRef(ctx context.Context, head, sha string) (*github.Reference, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Checking out branch %s from commit %s in %s/%s", head, sha, owner, repo)
	ref, resp, err := c.git.CreateRef(ctx, owner, repo, github.CreateRef{
		Ref: "refs/heads/" + head,
		SHA: sha,
	})
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return ref, nil
}

// DeleteRef deletes the specified git reference (branch) from the repository.
//
// It returns an error if the operation fails.
func (c *Client) DeleteRef(ctx context.Context, head string) error {
	owner, repo := c.Repo()
	githubactions.Infof("Deleting branch %s from %s/%s", head, owner, repo)

	resp, err := c.git.DeleteRef(ctx, owner, repo, head)
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return nil
}

// FindCommitRange retrieves the list of commits in the specified pull request.
//
// The function assumes that the pull request was merged using the "Rebase and merge" strategy.
// It will fetch the commits in the range from the merge commit SHA minus the number of commits
// to the merge commit SHA.
//
// It returns a slice of [github.RepositoryCommit] objects or an error if the operation fails.
func (c *Client) FindCommitRange(ctx context.Context, pr *github.PullRequest) ([]*github.RepositoryCommit, error) {
	owner, repo := c.Repo()
	mergeSHA := pr.GetMergeCommitSHA()
	githubactions.Infof("Finding commits in range %s~%d..%s in %s/%s", mergeSHA, pr.GetCommits(), mergeSHA, owner, repo)

	opts := &github.CommitsListOptions{
		SHA:         mergeSHA,
		ListOptions: github.ListOptions{PerPage: pr.GetCommits()},
	}
	commits, resp, err := c.client.Repositories.ListCommits(ctx, owner, repo, opts)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return commits, nil
}

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

// CreateCommit creates a new commit in the repository.
//
// It takes the context and a [github.Commit] object representing the commit to be created.
// If the commit cannot be created due to a conflict, it returns [ErrConflict] error, otherwise
// it returns the created commit or an error if the operation fails for other reasons.
func (c *Client) CreateCommit(ctx context.Context, commit *github.Commit) (*github.Commit, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Creating commit in %s/%s with message: %s", owner, repo, commit.GetMessage())

	createdCommit, resp, err := c.git.CreateCommit(ctx, owner, repo, *commit, nil)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusCreated {
		if resp.StatusCode == http.StatusConflict {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return createdCommit, nil
}

// CompareCommits compares two commits by their SHAs.
//
// It returns a [github.CommitsComparison] object containing the comparison details
// or an error if the operation fails.
func (c *Client) CompareCommits(ctx context.Context, base, head string) (*github.CommitsComparison, error) {
	owner, repo := c.Repo()
	githubactions.Infof("Comparing commits %s...%s in %s/%s", base, head, owner, repo)

	comp, resp, err := c.client.Repositories.CompareCommits(ctx, owner, repo, base, head, nil)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	return comp, nil
}
