package backport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	v75github "github.com/google/go-github/v75/github"
	"github.com/sethvargo/go-githubactions"
	"github.com/yhabteab/backbot/github"
)

// backPorter handles the backporting of pull requests to specified branches.
type backPorter struct {
	github *github.Client // GitHub client for API interactions

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
	prNumber, err := b.github.GetPrNumber()
	if err != nil {
		return err
	}

	sourcePr, err := b.github.GetPR(ctx, prNumber)
	if err != nil {
		return err
	}

	if !sourcePr.GetMerged() {
		githubactions.Warningf("Pull request #%d is not merged, skipping backport.", prNumber)
		// See https://docs.github.com/en/actions/using-workflows/events-that-trigger-workflows#pull_request_target.
		return b.github.CreateComment(ctx, prNumber, "⚠️ For security reasons, backbot backports merged pull requests only. Aborting.")
	}

	targetRefs, err := b.getTargetRefs(ctx, sourcePr)
	if err != nil {
		return err
	}
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
	var repositoryCommits []*v75github.RepositoryCommit
	switch mk {
	case github.Squash:
		githubactions.Infof("Pull request was merged with a squash commit, cherry-picking the squash commit %s", sourcePr.GetMergeCommitSHA())
		mergeCommit, err := b.github.GetCommit(ctx, sourcePr.GetMergeCommitSHA())
		if err != nil {
			return err
		}
		repositoryCommits = []*v75github.RepositoryCommit{mergeCommit}
	case github.MergeCommit:
		githubactions.Infof("Pull request was merged with a merge commit, cherry-picking all commits from %d excluding the merge commit", prNumber)
		commits, err := b.github.GetCommits(ctx, sourcePr)
		if err != nil {
			return err
		}
		repositoryCommits = commits
	case github.Rebase:
		githubactions.Infof("Pull request was merged with rebase, finding commits to cherry-pick")
		ranges, err := b.github.FindCommitRange(ctx, sourcePr)
		if err != nil {
			return err
		}
		repositoryCommits = ranges
	default:
		githubactions.Fatalf("Could not determine merge strategy '%s' for pull request #%d, skipping backport.", mk, prNumber)
	}

	// Reuse the underlying array of repositoryCommits to build the list of commits to cherry-pick,
	// as we may skip some commits based on configuration (e.g. merge commits). This helps reduce
	// memory usage when dealing with large PRs, as we don't allocate a new backing array.
	commitsToCherryPick := repositoryCommits[:0]

	// Now, alter the commit committer info to the configured committer. The author info is preserved.
	// Also, check for merge commits in the commit list to cherry-pick and handle them according to configuration.
	for _, commit := range repositoryCommits {
		if github.IsMergeCommit(commit.Commit) {
			if b.config.MergeCommitHandling == "skip" {
				githubactions.Infof("Skipping merge commit %s as per configuration", commit.GetSHA())
				continue
			} else if b.config.MergeCommitHandling == "fail" {
				githubactions.Warningf("Found merge commit %s in pull request #%d, failing backport as per configuration", commit.GetSHA(), prNumber)
				return b.github.CreateComment(ctx, prNumber, fmt.Sprintf(
					"⚠️ Found merge commit %s in pull request #%d, failing backport as per configuration.",
					commit.GetSHA(), prNumber,
				))
			}
		}
		commit.Commit.Committer.Name = v75github.Ptr(b.config.Committer)
		commit.Commit.Committer.Email = v75github.Ptr(b.config.Email)
		commitsToCherryPick = append(commitsToCherryPick, commit)
	}
	repositoryCommits = nil // Nil out the original slice to free memory (if any)

	if len(commitsToCherryPick) == 0 {
		githubactions.Infof("No commits to cherry-pick after applying configuration, exiting.")
		return b.github.CreateComment(ctx, prNumber, "⚠️ No commits to cherry-pick after applying configuration, skipping backport.")
	}

	labelsToAdd, err := b.getLabelsToAdd(sourcePr)
	if err != nil {
		return err
	}

	var prList string
	var refList []string

	for _, targetRef := range targetRefs {
		githubactions.Infof("Backporting to branch %s", targetRef.GetRef())
		backportBranch := makeBackportBranchName(prNumber, targetRef.GetRef())

		// Create the backport branch pointing to the latest commit SHA of the target branch.
		ref, err := b.github.CreateRef(ctx, backportBranch, targetRef.GetObject().GetSHA())
		if err != nil {
			githubactions.Errorf("Failed to create backport branch %s: %v", backportBranch, err)
			continue
		}

		newPr := b.cherryPickCommits(ctx, commitsToCherryPick, sourcePr, targetRef, ref, labelsToAdd...)
		if newPr != nil && !newPr.GetDraft() {
			refList = append(refList, "`"+trimRefPrefix(ref.GetRef())+"`")
			prList += fmt.Sprintf("- #%d\n", newPr.GetNumber())
		}
	}

	// Finally, comment on the source PR with the results of the backport operation.
	successBody := fmt.Sprintf(
		"✅ Successfully created backport PR(s) to the following branch(es): %s\n---\n%s",
		strings.Join(refList, ", "),
		prList,
	)
	if err := b.github.CreateComment(ctx, prNumber, successBody); err != nil {
		githubactions.Errorf("Failed to create comment on PR #%d: %v", prNumber, err)
		return err
	}
	return nil
}

// cherryPickCommits cherry-picks the specified commits onto the backport branch.
//
// It handles conflicts according to the configuration and returns an error if the operation fails.
func (b *backPorter) cherryPickCommits(
	ctx context.Context,
	commits []*v75github.RepositoryCommit,
	sourcePr *v75github.PullRequest,
	target,
	backport *v75github.Reference,
	labelsToAdd ...string,
) *v75github.PullRequest {
	// addParent adds a parent commit to the given commit, handling merge commits appropriately.
	addParent := func(commit *v75github.Commit, parentSHA string) {
		if github.IsMergeCommit(commit) {
			// For merge commits, we need to preserve all parents.
			commit.Parents = append(commit.Parents, &v75github.Commit{SHA: v75github.Ptr(parentSHA)})
		} else {
			// For non-merge commits, they should have exactly one parent.
			commit.Parents = []*v75github.Commit{{SHA: v75github.Ptr(parentSHA)}}
		}
	}

	backportBranch := trimRefPrefix(backport.GetRef())
	// Ensure the backport branch is deleted if we exit early due to an error.
	deleteRef := func() {
		if err := b.github.DeleteRef(ctx, backport.GetRef()); err != nil {
			githubactions.Errorf("Failed to delete backport branch %s: %v", backportBranch, err)
		}
	}

	// Now go through the commits and attempt to cherry-pick them one by one via the GitHub API.
	var previousCommit *v75github.Commit
	for i, commit := range commits {
		if previousCommit == nil {
			// First commit to cherry-pick onto the backport branch must have the
			// backport branch's latest commit as its parent.
			addParent(commit.Commit, backport.GetObject().GetSHA())
		} else {
			// Subsequent commits to cherry-pick must have the previous commit as their parent.
			addParent(commit.Commit, previousCommit.GetSHA())
		}
		newCommit, err := b.github.CreateCommit(ctx, commit.Commit)
		if err != nil {
			if errors.Is(err, github.ErrConflict) {
				githubactions.Warningf(
					"Conflict occurred while cherry-picking commit %s to branch %s",
					commit.GetSHA(), trimRefPrefix(target.GetRef()),
				)

				srcPrNum := int64(sourcePr.GetNumber())
				if b.config.ConflictHandling == "draft" {
					if i == 0 {
						// If the first commit to cherry-pick causes a conflict, we need to create an empty commit
						// on the backport branch to serve as the base for the draft PR.
						if emptyCommit, err := b.github.CreateCommit(ctx, createEmptyCommit(backport)); err != nil {
							githubactions.Errorf("Failed to create empty commit on backport branch %s: %v", backportBranch, err)
							deleteRef()
							return nil
						} else {
							newCommit = emptyCommit
						}
					} else {
						newCommit = previousCommit // Use the last successfully cherry-picked commit as the base
					}
					if backport, err = b.github.UpdateRef(ctx, backport.GetRef(), newCommit.GetSHA(), false); err != nil {
						githubactions.Errorf("Failed to update backport branch %s: %v", backportBranch, err)
						deleteRef()
						return nil
					}
					newPr, err := b.github.CreatePR(ctx, b.makeNewPullRequest(sourcePr, target, backport, true))
					if err != nil {
						githubactions.Errorf("Failed to create draft PR for backport branch %s: %v", backportBranch, err)
						deleteRef()
						return nil
					}
					if _, err := b.github.LabelPR(ctx, sourcePr, labelsToAdd...); err != nil {
						githubactions.Errorf("Failed to add labels to backport PR for branch %s: %v", backportBranch, err)
					}
					msg := fmt.Sprintf(
						"⚠️ Backporting commit %s to branch `%s` causes a conflict. Created draft PR for manual resolution.\n\n",
						commit.GetSHA(), trimRefPrefix(target.GetRef()),
					)
					msg += fmt.Sprintf("### Manual Backport Steps\n```bash\n%s\n```\n", b.listManualSteps(commits[i:], backport, i == 0))
					if err := b.github.CreateComment(ctx, srcPrNum, msg); err != nil {
						githubactions.Errorf("Failed to create comment on PR #%d: %v", srcPrNum, err)
					}
					if err := b.github.CreateComment(ctx, int64(newPr.GetNumber()), msg); err != nil {
						githubactions.Errorf("Failed to create comment on draft PR #%d: %v", newPr.GetNumber(), err)
					}
					return newPr
				}
				msg := fmt.Sprintf(
					"⚠️ Conflict occurred while backporting commit %s to branch %s. Aborting backport as per configuration.",
					commit.GetSHA(), trimRefPrefix(target.GetRef()),
				)
				if err := b.github.CreateComment(ctx, srcPrNum, msg); err != nil {
					githubactions.Errorf("Failed to create comment on PR #%d: %v", srcPrNum, err)
				}
			} else {
				githubactions.Errorf("Failed to create commit for cherry-pick of %s: %v", commit.GetSHA(), err)
			}
			deleteRef() // Delete the backport branch as the backport failed.
			return nil
		}
		previousCommit = newCommit
		githubactions.Infof(
			"Successfully cherry-picked commit %s to branch %s as new commit %s",
			commit.GetSHA(), trimRefPrefix(target.GetRef()), newCommit.GetSHA(),
		)
	}

	// Update the backport branch to point to the latest cherry-picked commit.
	if ref, err := b.github.UpdateRef(ctx, backportBranch, previousCommit.GetSHA(), false); err != nil {
		githubactions.Errorf("Failed to update backport branch %s: %v", backportBranch, err)
		deleteRef()
		return nil
	} else {
		backport = ref
	}

	// We've finished processing all commits for this target branch, so create the PR.
	newPr, err := b.github.CreatePR(ctx, b.makeNewPullRequest(sourcePr, target, backport, false))
	if err != nil {
		githubactions.Errorf("Failed to create PR for backport branch %s: %v", backportBranch, err)
		deleteRef()
		return nil
	}
	githubactions.Infof("Created backport PR #%d for branch %s", newPr.GetNumber(), trimRefPrefix(target.GetRef()))

	// Add labels to the backport PR if any.
	if _, err := b.github.LabelPR(ctx, newPr, labelsToAdd...); err != nil {
		githubactions.Errorf("Failed to add labels to backport PR #%d: %v", newPr.GetNumber(), err)
	}
	return newPr
}

// listManualSteps lists the manual steps need to manually backport a PR in case of conflicts.
//
// It returns a string containing the manual steps.
func (b *backPorter) listManualSteps(commits []*v75github.RepositoryCommit, ref *v75github.Reference, haveEmptyCommit bool) string {
	var comitSHAs []string
	for _, commit := range commits {
		comitSHAs = append(comitSHAs, commit.GetSHA())
	}

	normalizedRefName := trimRefPrefix(ref.GetRef())
	steps := []string{
		fmt.Sprintf("git fetch origin %s", normalizedRefName),
		fmt.Sprintf("git worktree add --checkout backbot/%[1]s origin/%[1]s", normalizedRefName),
		fmt.Sprintf("cd backbot/%s", normalizedRefName),
	}
	if haveEmptyCommit {
		steps = append(steps, "git reset --hard HEAD^") // Remove the empty commit from the branch history
	}
	steps = append(steps, fmt.Sprintf("git cherry-pick -x %s", strings.Join(comitSHAs, " ")))
	if haveEmptyCommit {
		steps = append(steps, "git push --force-with-lease")
	}
	steps = append(steps, "cd -", fmt.Sprintf("git worktree remove backbot/%s", normalizedRefName))
	return strings.Join(steps, "\n")
}

// getTargetRefs determines the target branches for backporting based on the configuration and source PR.
//
// It returns a slice of target branch names or an error if the operation fails.
func (b *backPorter) getTargetRefs(ctx context.Context, sourcePr *v75github.PullRequest) ([]*v75github.Reference, error) {
	if b.config.labelRegex == nil || len(sourcePr.Labels) == 0 {
		return nil, nil
	}
	githubactions.Infof("Finding target branches matching pattern: %s", b.config.LabelPattern)

	_, repo := b.github.Repo()

	var branches []*v75github.Reference
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
		// Does the branch extracted from the label exist in the repository?
		ref, err := b.github.GetRef(ctx, branch)
		if err != nil || ref == nil {
			githubactions.Warningf(
				"Branch '%s' extracted from label '%s' does not exist in repository %s. %s",
				branch, label.GetName(), repo, err,
			)
			continue
		}
		githubactions.Infof("Label '%s' matches pattern '%s', adding branch '%s'", label.GetName(), b.config.LabelPattern, ref.GetRef())
		branches = append(branches, ref)
	}
	return branches, nil
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
