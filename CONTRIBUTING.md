# Contributing

To contribute to this project please carefully read this document.

## Setup

`wakatime-cli` is written in [Go](https://golang.org/).

Prerequisites:

- We use `make` to build and run tests
- We use [bats](https://bats-core.readthedocs.io/en/latest/installation.html) to test shell scripts. (`brew install bats-core`)
- We use the [Go](https://golang.org/doc/install) version listed in [go.mod](go.mod)

After cloning, install dependencies with `make install` then build with `make`.

Tip: run `ln -sf $(pwd)/build/* ~/.wakatime/` to have the plugins use your local build.

## Branches

`develop` is the maintained default branch. Create a focused task branch and submit a pull request to `develop`.

The inherited release jobs belong to the upstream release process. Automatic tagging, signing, and releases run only in `wakatime/wakatime-cli`.

## Testing and Linting

Run `make test-all` before creating any pull requests, or your PR won’t pass the automated checks.

> make sure you build binary by setting its version otherwise integration tests will fail. `VERSION=v0.0.1-test make build-<os>-<architecture>`. For testing shell scripts you might initialize submodules by running `git submodule update --init --recursive`.

## Pull Requests

- Search existing pull requests and issues before starting a change.
- Code formatting should be consistent with the style used in the existing code.
- Don't leave commented out code. A record of this code is already preserved in the commit history.
- All commits must be atomic. This means that the commit completely accomplishes a single task. Each commit should result in fully functional code. Multiple tasks should not be combined in a single commit, but a single task should not be split over multiple commits (e.g. one commit per file modified is not a good practice). For more information see <http://www.freshconsulting.com/atomic-commits>.
- Each pull request should address a single bug fix or feature. This may consist of multiple commits. If you have multiple, unrelated fixes or enhancements to contribute, submit them as separate pull requests.
- Use `type(scope): description` for commit messages and pull request titles. The scope is optional. Allowed types are `feat`, `fix`, `refactor`, `docs`, `test`, `ci`, `chore`, `style`, and `perf`.
- Use the imperative mood in the title and omit a final period.
- Describe the problem, resulting behavior, rationale, and relevant validation in the pull request body. Link any issue the change resolves.
- Pull requests are squash-merged using the pull request title and body as the commit message.

## Troubleshooting

- Race detection is enabled by default on `Makefile` and it may fail on macOS Monterey (OSX 12.0). You might temporarily set the environment variable `MallocNanoZone=0`. More details in this [thread](https://github.com/golang/go/issues/49138).

Any question join us on [Slack](https://wakaslack.herokuapp.com/).
