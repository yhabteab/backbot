package github

import (
	"io"

	"github.com/google/go-github/v75/github"
)

// IsMergeCommit checks if the given [github.Commit] is a merge commit.
//
// A merge commit is defined as a commit that has more than one parent.
// The function returns true if the commit is a merge commit, false otherwise.
func IsMergeCommit(commit *github.Commit) bool {
	return len(commit.Parents) > 1
}

// compareCommitFile compares two [github.CommitFile] objects for equality.
//
// It returns true if all relevant fields are equal, false otherwise.
func compareCommitFile(a, b *github.CommitFile) bool {
	if a.GetFilename() != b.GetFilename() {
		return false
	}
	if a.GetAdditions() != b.GetAdditions() {
		return false
	}
	if a.GetDeletions() != b.GetDeletions() {
		return false
	}
	if a.GetChanges() != b.GetChanges() {
		return false
	}
	if a.GetPatch() != b.GetPatch() {
		return false
	}
	return true
}

// closeResponseBody closes the response body and discards any remaining data.
//
// It is used to ensure that the response body is properly closed after use.
func closeResponseBody(resp *github.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body) // Discard any remaining data
	_ = resp.Body.Close()
}
