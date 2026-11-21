# Contributing

To contribute to this project please carefully read this document.

## Setup

`wakatime-cli` is written in [Go](https://golang.org/).

Prerequisites:

- We use `make` to build and run tests
- We use [bats](https://bats-core.readthedocs.io/en/latest/installation.html) to test shell scripts. (`brew install bats-core`)
- Use the [Go](https://go.dev/doc/install) version listed in [go.mod](go.mod).

After cloning, install dependencies with `make install` then build with `make`.

Tip: run `ln -sf $(pwd)/build/* ~/.wakatime/` to have the plugins use your local build.

## Branches and releases

Branch from `develop` and open pull requests against `develop`. For changes intended for the upstream WakaTime CLI, follow the [upstream contribution guide](https://github.com/wakatime/wakatime-cli/blob/develop/CONTRIBUTING.md).

To release this fork, update `VERSION` to `X.Y.Z-hakula.N` in a pull request. After it merges, wait for the `Tests` push run on that exact `develop` commit to pass. Download its `release-assets` artifact and verify the three portable archives against `SHA256SUMS`. Create an annotated `vX.Y.Z-hakula.N` tag on that commit with the release notes, then publish a GitHub prerelease using the tag notes, archives, and checksum file.

## Testing and Linting

Initialize the shell test dependencies with `git submodule update --init --recursive`. Build the integration-test binary for the host with the Makefile's default `<local-build>` version, then run `make test-all`. For example, on Linux amd64 run `make build-linux-amd64` followed by `make test-all`.

## Pull Requests

- Discuss API changes and backward-compatibility tradeoffs in a fork issue before implementing them. Search the fork's [issues](https://github.com/hakulab/wakatime-cli/issues) and [pull requests](https://github.com/hakulab/wakatime-cli/pulls) for related work.
- Code formatting should be consistent with the style used in the existing code.
- Don't leave commented out code. A record of this code is already preserved in the commit history.
- All commits must be atomic. This means that the commit completely accomplishes a single task. Each commit should result in fully functional code. Multiple tasks should not be combined in a single commit, but a single task should not be split over multiple commits (e.g. one commit per file modified is not a good practice). For more information see <http://www.freshconsulting.com/atomic-commits>.
- Each pull request should address a single bug fix or feature. This may consist of multiple commits. If you have multiple, unrelated fixes or enhancements to contribute, submit them as separate pull requests.
- Commit messages:
  - Use a type, an optional scope, and an imperative summary, for example `fix(project): resolve Git project names`.
  - Do not end the title with a period.
  - Separate title from the body with a blank line. If you're committing via GitHub or GitHub Desktop this will be done automatically.
  - Wrap body at 72 characters.
  - Completely explain the purpose of the commit. Include a rationale for the change, any caveats, side-effects, etc.
  - If your pull request fixes an issue in the issue tracker, use the [closes/fixes/resolves syntax](https://help.github.com/articles/closing-issues-via-commit-messages) in the body to indicate this.
  - See <http://chris.beams.io/posts/git-commit> for more tips on writing good commit messages.
- Use the same title format for pull requests. Explain the user-visible change and how you verified it in the description.
- Rebase on `develop` when needed. Maintainers squash-merge reviewed pull requests after CI passes.

## Troubleshooting

- Race detection is enabled by default on `Makefile` and it may fail on macOS Monterey (OSX 12.0). You might temporarily set the environment variable `MallocNanoZone=0`. More details in this [thread](https://github.com/golang/go/issues/49138).
