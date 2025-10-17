# Backbot

Backbot is a fast and efficient GitHub action designed to automate backporting pull requests to other branches.
It helps you backport changes made in a pull request to multiple target branches based on labels applied to the
original pull request. It's written in pure Go and runs in a minimal Docker container, making it lightweight and easy
to use. It uses a tiny pre-built Container image based on Alpine pulled from
[GitHub Container Registry](https://github.com/yhabteab/backbot/pkgs/container/backbot), which is only a few megabytes
in size, so it's pulled and started very quickly.

## Features

- Automatically backports merged pull requests to specified branches based on labels.
- Customizable pull request titles/descriptions for backports.
- Option to copy labels from the original pull request to the backport pull request.
- Handles merge commits in the original pull request with configurable strategies.
- Easy to set up and use in any GitHub repository.
- Lightweight and efficient, written in pure **Go** 🩵 and runs in a minimal Docker container.

It is designed to be flexible and configurable, allowing you to tailor its behavior to fit your workflow. Simply add
one of the provided workflow files to your repository, and Backbot will take care of the rest.

## Getting Started

To use Backbot in your GitHub repository, you need to set up a GitHub Actions workflow. Below is an example workflow
file that demonstrates how to configure and run Backbot. Typically, you would want that the PRs created by Backbot can
trigger workflows as well in order to run tests and checks on the backported changes. However, for a good reason, GitHub
does not allow workflows to be triggered for PRs created by GitHub Actions using the default 
[`GITHUB_TOKEN`](https://docs.github.com/en/actions/concepts/security/github_token#when-github_token-triggers-workflow-runs).
Moreover, Backbot won't be able to backport PRs that modify workflow files in the `.github/workflows` directory.
Therefore, we recommend using a custom GitHub App to generate an installation access token with the necessary
permissions instead of using the default `GITHUB_TOKEN`. You can create a GitHub App by following the instructions
[here](https://docs.github.com/en/developers/apps/building-github-apps/creating-a-github-app). Make sure to grant the
GitHub App the following permissions:

- `contents`: Read & Write
- `pull-request`: Read & Write
- `workflows`: Read & Write (needed to backport PRs that modify workflow files)
- `issues`: Read & Write (needed to add comments to the PRs created by Backbot and the original PR)

And install the GitHub App on the repository where you want to use Backbot. After creating the GitHub App, you need to
add the following secrets to the repository:

- `BACKBOT_APP_ID`: The App ID of the GitHub App you created above (you can find it on the GitHub App settings page).
- `BACKBOT_APP_PRIVATE_KEY`: The private key of the GitHub App you created above (you can generate and download it from
  the GitHub App settings page).

After setting up the GitHub App and adding the secrets, you can create a workflow file in your repository (e.g.,
`.github/workflows/backbot.yml`) with the following content and customize it as needed:

```yaml
name: Backbot
on:
  pull_request:
    types: [closed]

jobs:
  backbot:
    runs-on: ubuntu-latest

    # Disable all permissions for the GITHUB_TOKEN, as we are using a GitHub App token instead.
    permissions: {}

    # Never run this job for unmerged pull requests.
    if: ${{ github.event.pull_request.merged == true }}
    steps:
      - name: Generate GitHub Installation Access Token
        # Use GitHub App to generate an installation access token to allow PRs created by Backbot to trigger workflows.
        # This is necessary because PRs created using the default `GITHUB_TOKEN` do not trigger workflows plus
        # GitHub doesn't allow to alter any file within the .github/workflows directory using the default GITHUB_TOKEN.
        # This action will create a token with the permissions listed below and is valid only for 1 hour, but if the
        # job completes before that 1 hour limit, the token will automatically be revoked in the post-job cleanup step.
        uses: actions/create-github-app-token@67018539274d69449ef7c02e8e71183d1719ab42 # v2.1.4
        id: backbot-token
        with:
          app-id: ${{ secrets.BACKBOT_APP_ID }}
          private-key: ${{ secrets.BACKBOT_APP_PRIVATE_KEY }}
          skip-token-revoke: false # Revoke the token after the job is done (is the default behavior).
          # GitHub recommends to explicitly list the permissions the token should have instead of inheriting all the
          # permissions from the GitHub App itself. See https://github.com/actions/create-github-app-token
          permission-contents: write # Allow to create, delete and update branches.
          permission-pull-requests: write # Allow to create and update PRs.
          permission-workflows: write # Allow to backport PRs that modify workflow files.
          permission-issues: write # Needed to add comments to the PRs created by Backbot and the original PR.

      - name: Checkout
        uses: actions/checkout@08c6903cd8c0fde910a37f88322edcfb5dd907a8 # v5.0.0
        with:
          token: ${{ steps.backbot-token.outputs.token }} # To make authenticated git operations.

      - name: Run Backbot
        uses: yhabteab/backbot@main
        with:
          github_token: ${{ steps.backbot-token.outputs.token }}
          conflict_handling: 'draft' # create a draft pull request if there are conflicts
```

If you are not interested in having the PRs created by Backbot to trigger workflows, and you don't need to backport PRs
that modify workflow files, you can simplify the workflow by using the default `GITHUB_TOKEN`. In that case, you would
not need to create a GitHub App or add any secrets to your repository. Here is an example workflow file using the default
`GITHUB_TOKEN`:

```yaml
name: Backbot
on:
  pull_request:
    types: [closed]

jobs:
  backbot:
    runs-on: ubuntu-latest

    permissions:
      contents: write # Allow to create, delete and update branches.
      pull-requests: write # Allow to create and update PRs.
      issues: write # Needed to add comments to the PRs created by Backbot and the original PR.

    # Never run this job for unmerged pull requests.
    if: ${{ github.event.pull_request.merged == true }}
    steps:
      - name: Checkout
        uses: actions/checkout@08c6903cd8c0fde910a37f88322edcfb5dd907a8 # v5.0.0

      - name: Run Backbot
        uses: yhabteab/backbot@main
        with:
          github_token: ${{ secrets.GITHUB_TOKEN }}
          conflict_handling: 'draft' # create a draft pull request if there are conflicts
```

## Configuration

Backbot provides several configuration options that can be set via the workflow file:

| Option                  | Description                                          | Default Value                                      |
|-------------------------|------------------------------------------------------|----------------------------------------------------|
| `github_token`          | **Required**. GitHub token for authentication        | None                                               |
| `committer`             | **Required**. Name of the committer                  | `github-actions[bot]`                              |
| `committer_email`       | **Required**. Email of the committer                 | `github-actions[bot]@users.noreply.github.com`     |
| `pr_title`              | **Required**. Title format for backport PRs          | `[Backport ${target_branch}] ${original_pr_title}` |
| `pr_description`        | **Required**. Description format for backport PRs    | See the `action.yml`                               |
| `label_pattern`         | **Required**. Regex pattern to match backport labels | `^backport-to-(support\/\d+\.\d+)$`                |
| `copy_labels_pattern`   | **Optional**. Regex pattern to match labels to copy  | None                                               |
| `conflict_handling`     | **Required**. Strategy for handling conflicts        | `abort`                                            |
| `merge_commit_handling` | **Required**. Strategy for handling merge commits    | `skip`                                             |

Most of these options are required but have also sensible default values. So, you can omit them if the default values
fit your needs. Required options without default values, such as `github_token`, must always be provided. Here is a
brief description of each option with a bit more detail:

- `github_token`: A GitHub token with sufficient permissions to create branches and pull requests in the repository.
- `committer`: The name that will be used as the committer for the backport commits.
- `committer_email`: The email that will be used as the committer email for the backport commits.
- `pr_title`: The title format for the backport pull requests.
- `pr_description`: The description format for the backport pull requests.
- `label_pattern`: A regex pattern to match labels that indicate which branches to backport to. For example, a label
  `backport-to-support/1.2` would match the default pattern and indicate that the pull request should be backported to
  the `support/1.2` branch. The supported regex flavor is defined by the [Go regex package](https://pkg.go.dev/regexp/syntax).
- `copy_labels_pattern`: A regex pattern to match labels that should be copied from the original pull request to the
  backport pull request. If not set, no labels will be copied.
- `conflict_handling`: The strategy to use when a conflict occurs during the backport. Possible values are:
  - `abort`: Abort the backporting process and fail with a non-zero exit code (default).
  - `draft`: Create a pull request with the changes that could be applied, leaving the rest for manual resolution.
- `merge_commit_handling`: The strategy to use when the original pull request contains merge commits as part of its
  history. Possible values are:
  - `skip`: Skip the merge commits and only backport the individual commits (default).
  - `abort`: Abort the backporting process and fail with a non-zero exit code if merge commits are detected.
  - Any other value will be treated as if `include` was specified, meaning that merge commits will be backported like
    any other commit in the pull request.

These options allow you to customize the behavior of Backbot to fit your workflow and requirements. You can additionally
use some placeholders in the `pr_title` and `pr_description` options:

| Placeholder                  | Description                                   |
|------------------------------|-----------------------------------------------|
| `${target_branch}`           | The target branch for the backport.           |
| `${original_pr_title}`       | The title of the original pull request.       |
| `${original_pr_number}`      | The number of the original pull request.      |
| `${original_pr_description}` | The description of the original pull request. |

These placeholders will be replaced with the appropriate values when creating the backport pull request.

## Contributing

Contributions are welcome! If you find a bug or have a feature request, please open an issue or submit a pull request.

## License

Backbot is licensed under the MIT License. See the [LICENSE](LICENSE) file for more information.
