package backport

import (
	"fmt"
	"strings"

	"github.com/google/go-github/v75/github"
)

// makeBackportBranchName constructs the name for the backport branch.
//
// The branch name is formatted as "backport-{prNumber}-to-{targetBranch}".
// It returns the constructed branch name.
func makeBackportBranchName(prNumber int64, targetBranch string) string {
	return fmt.Sprintf("backport-%d-to-%s", prNumber, targetBranch)
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
func replacePlaceholders(value string, target string, sourcePr *github.PullRequest) string {
	value = strings.ReplaceAll(value, "${target_branch}", target)
	value = strings.ReplaceAll(value, "${original_pr_number}", fmt.Sprintf("%d", sourcePr.GetNumber()))
	value = strings.ReplaceAll(value, "${original_pr_title}", sourcePr.GetTitle())
	value = strings.ReplaceAll(value, "${original_pr_description}", sourcePr.GetBody())
	return value
}

// makeNewPullRequest returns a fully initialized [github.NewPullRequest] object for creating a backport PR.
//
// The title and body are constructed based on the configuration and source PR details.
// It returns the constructed [github.NewPullRequest] object.
func (b *backPorter) makeNewPullRequest(sourcePr *github.PullRequest, target, backport string, draft bool) *github.NewPullRequest {
	return &github.NewPullRequest{
		Title:               github.Ptr(replacePlaceholders(b.config.Title, target, sourcePr)),
		Head:                github.Ptr(backport),
		Base:                github.Ptr(target),
		Body:                github.Ptr(replacePlaceholders(b.config.Description, target, sourcePr)),
		MaintainerCanModify: github.Ptr(true),
		Draft:               github.Ptr(draft),
	}
}

// listManualSteps generates a list of manual steps to resolve conflicts during backporting.
//
// It returns a formatted string containing the git commands to manually backport the specified commits
// to the target branch. The steps include fetching the target branch, creating a worktree, cherry-picking
// the commits, pushing the resolved branch, and cleaning up.
func listManualSteps(refName string, commitSHAs []string) string {
	worktree := strings.ReplaceAll(refName, "/", "-")
	steps := []string{
		fmt.Sprintf("git fetch origin %s", refName),
		fmt.Sprintf("git worktree add --checkout %s origin/%[1]s", worktree, refName),
		fmt.Sprintf("cd %s", worktree),
		"git reset --hard HEAD^", // Reset the draft commit created during conflict.
		fmt.Sprintf("git cherry-pick -x %s", strings.Join(commitSHAs, " ")),
		"git push --force",                              // Force push the resolved backport branch.
		"cd -",                                          // Go back to the main working directory.
		fmt.Sprintf("git worktree remove %s", worktree), // Clean up the worktree.
	}
	return strings.Join(steps, "\n")
}
