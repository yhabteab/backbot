package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sethvargo/go-githubactions"
)

// Git holds configuration for git operations.
type Git struct {
	githubCtx *githubactions.GitHubContext
}

// NewGit creates a new Git instance with the provided committer name and email.
func NewGit(ghCtx *githubactions.GitHubContext) *Git { return &Git{githubCtx: ghCtx} }

// Configure sets up git with the specified committer name and email, and marks the workspace as a safe directory.
func Configure(ghCtx *githubactions.GitHubContext, committer, email string) error {
	g := NewGit(ghCtx)
	githubactions.Infof("Marking GitHub workspace '%s' as a safe directory", ghCtx.Workspace)
	// Mark the current workspace as a safe directory to avoid some weird git errors [^1].
	// [^1]: https://github.com/actions/runner-images/issues/6775
	if err := g.runCmd(context.Background(), "config", "--global", "--add", "safe.directory", ghCtx.Workspace); err != nil {
		return fmt.Errorf("failed to mark workspace '%s' as safe directory: %w", ghCtx.Workspace, err)
	}

	githubactions.Infof("Disabling git merge conflict advice")
	if err := g.runCmd(context.Background(), "config", "--global", "advice.mergeConflict", "false"); err != nil {
		return fmt.Errorf("failed to disable git merge conflict advice: %w", err)
	}

	githubactions.Infof("Configuring git committer name and email")
	if err := g.runCmd(context.Background(), "config", "--global", "user.name", committer); err != nil {
		return fmt.Errorf("failed to set git committer name: %w", err)
	}
	if err := g.runCmd(context.Background(), "config", "--global", "user.email", email); err != nil {
		return fmt.Errorf("failed to set git committer email: %w", err)
	}
	githubactions.Infof("Configured git with committer '%s' and email '%s'", committer, email)
	return nil
}

// Fetch fetches the specified ref from the given remote with the provided depth.
func (g *Git) Fetch(ctx context.Context, ref string, depth int) error {
	githubactions.Infof("Fetching from remote origin, ref %s, depth %d", ref, depth)
	return g.runCmd(ctx, "fetch", "--depth", fmt.Sprintf("%d", depth), "origin", ref)
}

// Push pushes the specified branch to the given remote.
func (g *Git) Push(ctx context.Context, ref string) error {
	githubactions.Infof("Pushing branch %s to remote origin", ref)
	return g.runCmd(ctx, "push", "--set-upstream", "origin", ref)
}

// Checkout checks out the specified branch.
func (g *Git) Checkout(ctx context.Context, ref, startPoint string) error {
	githubactions.Infof("Checking out branch %s from %s", ref, startPoint)
	return g.runCmd(ctx, "switch", "--create", ref, startPoint)
}

// BranchExists checks if the specified branch exists in the given remote.
func (g *Git) BranchExists(ctx context.Context, ref string) bool {
	githubactions.Infof("Checking if branch %s exists", ref)
	if err := g.runCmd(ctx, "show-ref", "--verify", "--quiet", fmt.Sprintf("refs/remotes/%s/%s", "origin", ref)); err != nil {
		githubactions.Warningf("Failed to check if branch %s exists: %v", ref, err)
		return false
	}
	githubactions.Infof("Branch %s exists", ref)
	return true
}

// CherryPick applies the commit with the given hash to the current branch.
//
// This method cherry-picks each commit in the provided order, even if some of them are empty.
// However, it will drop commits that result in no changes (empty commits) after applying them,
// i.e., if the changes introduced by the commit are already present in the target branch.
//
// If a conflict occurs during cherry-picking, it will attempt to abort the operation before returning an error.
func (g *Git) CherryPick(ctx context.Context, commitOnConflict bool, commits ...string) error {
	cmd := append([]string{"cherry-pick", "--empty=drop", "--allow-empty", "-x"}, commits...)
	githubactions.Infof("Cherry-picking commits using git %v", cmd)

	if err := g.runCmd(ctx, append(cmd, commits...)...); err != nil {
		if commitOnConflict {
			if IsConflictErr(err) {
				githubactions.Warningf("Conflict occurred while cherry-picking commits %v, creating draft commit: %v", commits, err)
				if err := g.runCmd(ctx, "commit", "--all", "--message", "Backport commit with conflicts, needs manual resolution"); err != nil {
					return fmt.Errorf("failed to create draft commit after conflict: %w", err)
				}
			}
		}
		// Attempt to abort the cherry-pick operation if it failed
		if abortErr := g.runCmd(ctx, "cherry-pick", "--abort"); abortErr != nil {
			githubactions.Warningf("Failed to abort cherry-pick after error: %v", abortErr)
		}
		return fmt.Errorf("failed to cherry-pick commits %v: %w", commits, err)
	}
	return nil
}

// FindCommitRange finds the range of commits between the given base and head commit hashes.
//
// It returns a slice of commit hashes in chronological order (from oldest to newest).
func (g *Git) FindCommitRange(ctx context.Context, args ...string) ([]string, error) {
	githubactions.Infof("Finding commit range for %s using git rev-list", args)

	// Set a timeout to avoid hanging indefinitely
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := g.prepareCMD(ctx, append([]string{"rev-list", "--reverse"}, args...)...)
	cmd.Stderr = nil // We want to handle exit errors down below
	cmd.Stdout = nil // We want to capture the output
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, NewErrGitOp(strings.Join(cmd.Args, " "), err, exitErr.ExitCode())
		}
		return nil, fmt.Errorf("failed to run git rev-list: %w", err)
	}
	commits := strings.Fields(string(output))
	githubactions.Infof("Found commits in range %v: %v", args, commits)
	return commits, nil
}

// runCmd runs a git command with the specified arguments.
//
// This will set the committer name and email in the environment for the command execution, even though
// not all git commands use these variables. The command's output (both stdout and stderr) will be
// redirected to the standard output where GitHub Actions can capture it.
func (g *Git) runCmd(ctx context.Context, args ...string) error {
	githubactions.Infof("Running git command: git %v", args)

	// Set a timeout to avoid hanging indefinitely
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := g.prepareCMD(ctx, args...)
	if err := cmd.Run(); err != nil {
		return NewErrGitOp(strings.Join(cmd.Args, " "), err, cmd.ProcessState.ExitCode())
	}

	state := cmd.ProcessState
	if !state.Success() {
		return NewErrGitOp(
			strings.Join(cmd.Args, " "),
			fmt.Errorf("running git command failed with exit code %d", state.ExitCode()),
			state.ExitCode(),
		)
	}
	return nil
}

// prepareCMD prepares an exec.Cmd for the given git command arguments.
//
// This method sets up the command with the appropriate environment variables, working directory,
// and output redirection. It does not run the command; it only prepares it for execution.
func (g *Git) prepareCMD(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = os.Environ()
	// Redirect everything to the standard output where GitHub Actions can capture it
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stdout
	// Set the working directory to the GitHub workspace
	cmd.Dir = g.githubCtx.Workspace
	return cmd
}
