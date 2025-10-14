package backport

import (
	"context"
	"fmt"
	"slices"
	"strings"

	v75github "github.com/google/go-github/v75/github"
	"github.com/sethvargo/go-githubactions"
	"github.com/yhabteab/backbot/git"
	"github.com/yhabteab/backbot/github"
)

// backPorter handles the backporting of pull requests to specified branches.
type backPorter struct {
	github *github.Client // GitHub client for API interactions

	git *git.Git // Git client for git cli operations

	config *Input // Configuration inputs for the backporting process
}

// Run is the entry point for the backporting process.
//
// It initializes the backPorter with the provided configuration and GitHub context,
// and invokes the Run method to perform the backporting. If any error occurs during
// the process, it logs a fatal error and exits with a non-zero status code.
func Run(ctx context.Context, cfg *Input, ghCtx *githubactions.GitHubContext) {
	b := &backPorter{
		github: github.NewClient(ghCtx, cfg.GitHubToken),
		git:    git.NewGit(ghCtx),
		config: cfg,
	}
	if err := b.Run(ctx); err != nil {
		githubactions.Fatalf("Backport failed: %v", err)
	}
}

// Run performs the backporting of the pull request to the target branches.
//
// This method orchestrates the entire backporting process, including determining target branches,
// cherry-picking commits, handling conflicts, creating backport branches and pull requests, and
// commenting on the original pull request with the results.
func (b *backPorter) Run(ctx context.Context) error {
	srcPrNumber, err := b.github.GetPrNumber()
	if err != nil {
		return err
	}

	sourcePr, err := b.github.GetPR(ctx, srcPrNumber)
	if err != nil {
		return err
	}

	if !sourcePr.GetMerged() {
		githubactions.Warningf("Pull request #%d is not merged, skipping backport.", srcPrNumber)
		// See https://docs.github.com/en/actions/using-workflows/events-that-trigger-workflows#pull_request_target.
		return b.github.CreateComment(ctx, srcPrNumber, "⚠️ For security reasons, backbot backports merged pull requests only. Aborting.")
	}

	targetRefs := b.getTargetRefs(ctx, sourcePr)
	if len(targetRefs) == 0 {
		githubactions.Infof("No target branches found for backporting. Exiting.")
		return nil
	}

	mk, err := b.github.MergeKind(ctx, sourcePr)
	if err != nil {
		return err
	}

	// Depending on the merge strategy used, the merge commit from the PR[^1] represents 3 different things:
	// 1. Merge commit strategy: the merge commit is a real merge commit with 2 parents, and the commits
	//    in the PR are the commits to cherry-pick (excluding the merge commit).
	// 2. Squash and merge strategy: the merge commit is a single commit that squashes all commits in the PR,
	//    and is the only commit to cherry-pick (the commits in the PR are not relevant).
	// 3. Rebase and merge strategy: the merge commit is a single commit that represents the last commit in the PR,
	//    but with a different SHA [^2]. The commits in the PR have different SHAs than those in the target branch,
	//    so we need to find the new SHAs of the commits to cherry-pick by looking backward starting from the merge
	//    commit plus the number of commits in the PR.
	//
	// [^1]: https://docs.github.com/en/rest/pulls/pulls?apiVersion=2022-11-28#get-a-pull-request
	// [^2]: https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/about-merge-methods-on-github#rebasing-and-merging-your-commits
	var commitSHAs []string // The SHAs of the commits to cherry-pick
	switch mk {
	case github.Squash:
		githubactions.Infof("Pull request was merged with a squash commit, cherry-picking the squash commit %s", sourcePr.GetMergeCommitSHA())
		commitSHAs = []string{sourcePr.GetMergeCommitSHA()}
	case github.MergeCommit:
		githubactions.Infof("Pull request was merged with a merge commit, cherry-picking all commits from #%d excluding the merge commit", srcPrNumber)
		commits, err := b.github.GetCommits(ctx, sourcePr)
		if err != nil {
			return err
		}
		for _, commit := range commits {
			commitSHAs = append(commitSHAs, commit.GetSHA())
		}
	case github.Rebase:
		githubactions.Infof("Pull request was merged with rebase, finding commits to cherry-pick")
		ranges, err := b.git.FindCommitRange(ctx, fmt.Sprintf("%s~%d..%s", sourcePr.GetMergeCommitSHA(), sourcePr.GetCommits(), sourcePr.GetMergeCommitSHA()))
		if err != nil {
			return err
		}
		commitSHAs = ranges
	default:
		githubactions.Fatalf("Could not determine merge strategy '%s' for pull request #%d, skipping backport.", mk, srcPrNumber)
	}

	mergeCommitSHAs, err := b.git.FindCommitRange(ctx, "--merges", fmt.Sprintf("%s^..%s", commitSHAs[0], commitSHAs[len(commitSHAs)-1]))
	if err != nil {
		return err
	}

	if len(mergeCommitSHAs) != 0 {
		if b.config.MergeCommitHandling == MergeCommitHandlingAbort {
			githubactions.Warningf(
				"Found merge commit(s) %v in pull request #%d, aborting backport as per configuration",
				mergeCommitSHAs, srcPrNumber,
			)
			return b.github.CreateComment(ctx, srcPrNumber, fmt.Sprintf(
				"⚠️ Found merge commit(s) %v in pull request #%d, backport aborted as per configuration.",
				mergeCommitSHAs, srcPrNumber,
			))
		}
		githubactions.Infof("Skipping merge commit %v as per configuration", mergeCommitSHAs)
		// Remove merge commits from the list of commits to cherry-pick.
		commitSHAs = slices.DeleteFunc(commitSHAs, func(s string) bool { return slices.Contains(mergeCommitSHAs, s) })
	}

	if len(commitSHAs) == 0 {
		githubactions.Infof("No commits to cherry-pick after applying configuration, exiting.")
		return b.github.CreateComment(ctx, srcPrNumber, "⚠️ No commits to cherry-pick after applying configuration, skipping backport.")
	}

	labelsToAdd, err := b.getLabelsToAdd(sourcePr)
	if err != nil {
		return err
	}

	var prList string
	var refList []string
	for _, targetRef := range targetRefs {
		backportRef := makeBackportBranchName(srcPrNumber, targetRef)
		githubactions.Infof("Creating backport branch %s for target branch %s", backportRef, targetRef)

		// Checkout the new backport branch locally starting from the target branch.
		if err := b.git.Checkout(ctx, backportRef, fmt.Sprintf("origin/%s", targetRef)); err != nil {
			githubactions.Errorf("Failed to checkout backport branch %s: %v", backportRef, err)
			continue
		}

		if newPr := b.cherryPick(ctx, sourcePr, targetRef, backportRef, commitSHAs); newPr != nil {
			if _, err := b.github.LabelPR(ctx, sourcePr, labelsToAdd...); err != nil {
				githubactions.Errorf("Failed to add labels to backport PR for branch %s: %v", backportRef, err)
			}

			// Dispatch a repository event to allow further automation on the new backport PR.
			if err := b.github.DisPatchPrCreatedEvent(ctx, newPr); err != nil {
				githubactions.Errorf("Failed to dispatch '%s' repository event for backport PR #%d: %v", github.RepoistoryDispatchEvent, newPr.GetNumber(), err)
			}

			refList = append(refList, fmt.Sprintf("`%s`", targetRef))
			prList += fmt.Sprintf("- #%d\n", newPr.GetNumber())
		}
	}

	if len(refList) == 0 {
		githubactions.Infof("No backport PRs were created successfully, exiting.")
		return b.github.CreateComment(ctx, srcPrNumber, "⚠️ No backport PRs were created successfully. See the GitHub Actions logs for details.")
	}

	// Finally, comment on the source PR with the results of the backport operation.
	successBody := fmt.Sprintf(
		"Successfully created backport PR(s) onto the following branch(es): %s\n\n---\n%s",
		strings.Join(refList, ", "), prList,
	)
	return b.github.CreateComment(ctx, srcPrNumber, successBody)
}

// cherryPick attempts to cherry-pick the specified commits onto the backport branch.
//
// It handles conflicts based on the configuration, either aborting the backport or creating
// a draft PR for manual resolution. If successful, it pushes the backport branch and creates
// a pull request and returns it, otherwise returns nil.
//
// All encountered errors are sent to GitHub Actions logs.
func (b *backPorter) cherryPick(ctx context.Context, srcPr *v75github.PullRequest, targetRef, backportRef string, commitSHAs []string) *v75github.PullRequest {
	srcPrNum := int64(srcPr.GetNumber())

	switch b.config.ConflictHandling {
	case ConflictHandlingAbort:
		if err := b.git.CherryPick(ctx, false, commitSHAs...); err != nil {
			githubactions.Errorf("Failed to cherry pick commits: %v", err)

			msg := fmt.Sprintf(
				"⚠️ Conflict occurred while backporting to branch %s. Aborting backport as per configuration.",
				targetRef,
			)
			if err := b.github.CreateComment(ctx, srcPrNum, msg); err != nil {
				githubactions.Errorf("Failed to create comment on PR #%d: %v", srcPrNum, err)
			}
			return nil
		}
	case ConflictHandlingDraft:
		for i, commitSHA := range commitSHAs {
			if err := b.git.CherryPick(ctx, true, commitSHA); err != nil {
				if git.IsConflictErr(err) {
					githubactions.Warningf(
						"Conflict occurred while cherry-picking commit %s to branch %s, trying to prepare for manual backport.",
						commitSHA, targetRef,
					)

					// Push the backport branch with the draft commit to remote.
					if err := b.git.Push(ctx, backportRef); err != nil {
						githubactions.Errorf("Failed to push backport branch %s: %v", backportRef, err)
						return nil
					}
					newPr, err := b.github.CreatePR(ctx, b.makeNewPullRequest(srcPr, targetRef, backportRef, true))
					if err != nil {
						githubactions.Errorf("Failed to create draft PR for backport branch %s: %v", backportRef, err)
						return nil
					}
					msg := fmt.Sprintf(
						"⚠️ Backporting commit %s to branch `%s` causes a conflict. Created draft PR for manual resolution.\n\n",
						commitSHA, targetRef,
					)
					msg += fmt.Sprintf("### Manual Backport Steps\n```bash\n%s\n```\n", listManualSteps(backportRef, commitSHAs[i:]))
					if err := b.github.CreateComment(ctx, srcPrNum, msg); err != nil {
						githubactions.Errorf("Failed to create comment on PR #%d: %v", srcPrNum, err)
					}
					if err := b.github.CreateComment(ctx, int64(newPr.GetNumber()), msg); err != nil {
						githubactions.Errorf("Failed to create comment on draft PR #%d: %v", newPr.GetNumber(), err)
					}
					return newPr
				}

				githubactions.Errorf("Failed to create commit for cherry-pick of %s: %v", commitSHA, err)
				return nil
			}
			githubactions.Infof("Successfully cherry-picked commit %s to branch %s", commitSHA, targetRef)
		}
	default:
		githubactions.Errorf("Unknown conflict handling strategy: %s", b.config.ConflictHandling)
		return nil
	}

	if err := b.git.Push(ctx, backportRef); err != nil {
		githubactions.Errorf("Failed to push backport branch %s: %v", backportRef, err)
		return nil
	}

	// We've finished processing all commits for this target branch, so create the PR.
	newPr, err := b.github.CreatePR(ctx, b.makeNewPullRequest(srcPr, targetRef, backportRef, false))
	if err != nil {
		githubactions.Errorf("Failed to create PR for backport branch %s: %v", backportRef, err)
		return nil
	}
	githubactions.Infof("Created backport PR #%d for branch %s", newPr.GetNumber(), targetRef)
	return newPr
}

// getTargetRefs determines the target branches for backporting based on the configuration and source PR.
//
// This will only return slice of target branch references that match the LabelPattern in the configuration
// or an empty slice if no matching branches are found.
func (b *backPorter) getTargetRefs(ctx context.Context, sourcePr *v75github.PullRequest) []string {
	if b.config.labelRegex == nil || len(sourcePr.Labels) == 0 {
		return nil
	}
	githubactions.Infof("Finding target branches matching pattern: %s", b.config.LabelPattern)

	owner, repo := b.github.Repo()

	var branches []string
	for _, label := range sourcePr.Labels {
		matches := b.config.labelRegex.FindStringSubmatch(label.GetName())
		if len(matches) == 0 {
			githubactions.Infof("Label '%s' does not match pattern '%s'", label.GetName(), b.config.LabelPattern)
			continue
		}
		if len(matches) < 2 {
			githubactions.Warningf("Label '%s' matches pattern '%s' but has no capturing group", label.GetName(), b.config.LabelPattern)
			continue
		}
		branch := matches[1]
		if err := b.git.Fetch(ctx, branch, 2); err != nil {
			githubactions.Warningf(
				"Branch '%s' extracted from label '%s' does not exist in repository %s/%s. %v",
				branch, label.GetName(), owner, repo, err,
			)
			continue
		}
		branches = append(branches, branch)
		githubactions.Infof("Label '%s' matches pattern '%s', adding branch '%s'", label.GetName(), b.config.LabelPattern, branch)
	}
	return branches
}

// getLabelsToAdd determines the labels to add to backport PRs based on the configuration and source PR.
//
// This will only return labels that match the CopyLabelsPattern excluding any labels that were used to
// determine target branches. It returns a slice of labels to add or an error if the operation fails.
func (b *backPorter) getLabelsToAdd(sourcePr *v75github.PullRequest) ([]string, error) {
	if b.config.copyLabelRegex == nil || len(sourcePr.Labels) == 0 {
		return nil, nil
	}
	githubactions.Infof("Finding labels to copy matching pattern: %s", b.config.CopyLabelsPattern)

	var labels []string
	for _, label := range sourcePr.Labels {
		if b.config.labelRegex != nil && b.config.labelRegex.MatchString(label.GetName()) {
			githubactions.Infof("Skipping label '%s' as it was used to determine target branches", label.GetName())
			continue
		}
		if !b.config.copyLabelRegex.MatchString(label.GetName()) {
			githubactions.Infof("Label '%s' does not match pattern '%s'", label.GetName(), b.config.CopyLabelsPattern)
			continue
		}
		githubactions.Infof("Label '%s' matches pattern '%s', adding to backport PR labels", label.GetName(), b.config.CopyLabelsPattern)
		labels = append(labels, label.GetName())
	}
	return labels, nil
}
