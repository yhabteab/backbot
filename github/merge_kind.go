//go:generate go tool stringer -linecomment -type=MergeKind -output=merge_kind_string.go

package github

// MergeKind represents the strategy to use when merging a pull request.
type MergeKind uint8

const (
	MergeInvalid MergeKind = iota // unknown

	Squash      // Squash and Merge
	MergeCommit // Merge Commit
	Rebase      // Rebase and Merge
)
