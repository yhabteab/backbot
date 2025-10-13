package backport

import (
	"fmt"
	"strings"

	"github.com/google/go-github/v75/github"
)

// trimRefPrefix removes the "refs/heads/" prefix from a branch reference if it exists.
//
// It returns the branch name without the prefix.
func trimRefPrefix(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// makeBackportBranchName constructs the name for the backport branch.
//
// The branch name is formatted as "backport-{prNumber}-to-{targetBranch}".
// It returns the constructed branch name.
func makeBackportBranchName(prNumber int64, targetBranch string) string {
	return fmt.Sprintf("backport-%d-to-%s", prNumber, trimRefPrefix(targetBranch))
}

// replacePlaceholders replaces placeholders in the input string with actual values.
//
// Supported placeholders:
// - ${target_branch}: replaced with the target branch name.
// - ${original_pr_number}: replaced with the original pull request number.
// - ${original_pr_title}: replaced with the original pull request title.
// - ${original_pr_description}: replaced with the original pull request description.
//
// It returns the string with placeholders expanded to their corresponding values.
func replacePlaceholders(value string, target *github.Reference, sourcePr *github.PullRequest) string {
	value = strings.ReplaceAll(value, "${target_branch}", trimRefPrefix(target.GetRef()))
	value = strings.ReplaceAll(value, "${original_pr_number}", fmt.Sprintf("%d", sourcePr.GetNumber()))
	value = strings.ReplaceAll(value, "${original_pr_title}", sourcePr.GetTitle())
	value = strings.ReplaceAll(value, "${original_pr_description}", sourcePr.GetBody())
	return value
}

// makeNewPullRequest returns a fully initialized [github.NewPullRequest] object for creating a backport PR.
//
// The title and body are constructed based on the configuration and source PR details.
// It returns the constructed [github.NewPullRequest] object.
func (b *backPorter) makeNewPullRequest(sourcePr *github.PullRequest, target, backport *github.Reference, draft bool) *github.NewPullRequest {
	return &github.NewPullRequest{
		Title:               github.Ptr(replacePlaceholders(b.config.Title, target, sourcePr)),
		Head:                github.Ptr(trimRefPrefix(backport.GetRef())),
		Base:                github.Ptr(trimRefPrefix(target.GetRef())),
		Body:                github.Ptr(fmt.Sprintf("# Description\n\n%s", replacePlaceholders(b.config.Description, target, sourcePr))),
		MaintainerCanModify: github.Ptr(true),
		Draft:               github.Ptr(draft),
	}
}

// createEmptyCommit creates an empty commit on the specified reference.
//
// This is used to create a base commit for a draft PR when the first commit to cherry-pick
// causes a conflict. It returns the dummy commit object with empty changes.
func createEmptyCommit(ref *github.Reference) *github.Commit {
	return &github.Commit{
		Message: github.Ptr("chore: empty commit to serve as base for backport PR"),
		Tree:    &github.Tree{SHA: github.Ptr(ref.GetObject().GetSHA())},
		Parents: []*github.Commit{{SHA: github.Ptr(ref.GetObject().GetSHA())}},
	}
}
