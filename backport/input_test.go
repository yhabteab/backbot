package backport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInput(t *testing.T) {
	env := map[string]string{
		"INPUT_GITHUB_TOKEN":       "token",
		"INPUT_COMMITTER":          "committer",
		"INPUT_EMAIL":              "email",
		"INPUT_PR_TITLE":           "title",
		"INPUT_PR_DESCRIPTION":     "description",
		"INPUT_COPY_LABEL_PATTERN": "copy-label-pattern",
		"INPUT_LABEL_PATTERN":      "label-pattern",
		"INPUT_CONFLICT_HANDLING":  "abort",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	input, err := LoadInputsFromEnv()
	require.NoError(t, err)
	require.Equal(t, "token", input.GitHubToken)
	require.Equal(t, "committer", input.Committer)
	require.Equal(t, "email", input.Email)
	require.Equal(t, "title", input.Title)
	require.Equal(t, "description", input.Description)
	require.Equal(t, "copy-label-pattern", input.CopyLabelPattern)
	require.Equal(t, "label-pattern", input.LabelPattern)
	require.Equal(t, "abort", input.ConflictHandling)
	require.Equal(t, "skip", input.MergeCommitHandling)
}
