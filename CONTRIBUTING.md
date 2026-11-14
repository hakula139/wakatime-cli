# Contributing

To contribute to this project please carefully read this document.

## Setup

`wakatime-cli` is written in [Go](https://golang.org/).

Prerequisites:

- We use `make` to build and run tests
- We use the [Go](https://golang.org/doc/install) version listed in [go.mod](go.mod)

After cloning, install dependencies with `make install` then build with `make`.

Tip: run `ln -sf $(pwd)/build/* ~/.wakatime/` to have the plugins use your local build.

## Branches

`develop` is the maintained default branch. Create a focused task branch and submit a pull request to `develop`.

## Testing and Linting

Run `make test-all` before creating any pull requests, or your PR won’t pass the automated checks.

Build the binary with a version before running integration tests: `VERSION=v0.0.1-test make build-<os>-<architecture>`.

## Pull Requests

Follow the shared [Git workflow](https://github.com/hakula139/plugins/blob/main/plugins/hakula-workflow/skills/git-workflow/SKILL.md) for commits and pull requests. Repository settings and squash merge messages follow the [devkit GitHub conventions](https://github.com/hakula139/devkit/blob/v3.2.1/docs/github.md).

## Troubleshooting

- Race detection is enabled by default on `Makefile` and it may fail on macOS Monterey (OSX 12.0). You might temporarily set the environment variable `MallocNanoZone=0`. More details in this [thread](https://github.com/golang/go/issues/49138).
