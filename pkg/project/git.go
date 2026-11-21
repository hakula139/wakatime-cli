package project

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wakatime/wakatime-cli/pkg/file"
	"github.com/wakatime/wakatime-cli/pkg/log"
	"github.com/wakatime/wakatime-cli/pkg/regex"
)

// Git contains git data.
type Git struct {
	// Filepath contains the entity path.
	Filepath string
	// ProjectFromGitRemote when enabled uses the git remote as the project name instead of local git folder.
	ProjectFromGitRemote bool
	// SubmoduleDisabledPatterns will be matched against the submodule path and if matching, will skip it.
	SubmoduleDisabledPatterns []regex.Regex
	// SubmoduleProjectMapPatterns will be matched against the submodule path and if matching, will use the project map.
	SubmoduleProjectMapPatterns []MapPattern
}

// Detect gets information about the git project for a given file.
// It tries to return a project and branch name.
func (g Git) Detect(ctx context.Context) (Result, bool, error) {
	logger := log.Extract(ctx)
	fp := g.Filepath

	// Find for submodule takes priority if enabled
	gitdirSubmodule, ok, err := findSubmodule(ctx, fp, g.SubmoduleDisabledPatterns)
	if err != nil {
		return Result{}, false, fmt.Errorf("failed to find submodule: %s", err)
	}

	if ok {
		project := projectOrRemote(ctx, filepath.Base(gitdirSubmodule), g.ProjectFromGitRemote, gitdirSubmodule)

		// If submodule has a project map, then use it.
		if result, ok := matchPattern(ctx, gitdirSubmodule, g.SubmoduleProjectMapPatterns); ok {
			project = result
		}

		branch, err := findGitBranch(ctx, filepath.Join(gitdirSubmodule, "HEAD"))
		if err != nil {
			logger.Errorf(
				"error finding branch from %q: %s",
				filepath.Join(filepath.Dir(gitdirSubmodule), "HEAD"),
				err,
			)
		}

		return Result{
			Project: project,
			Branch:  branch,
			Folder:  filepath.Dir(gitdirSubmodule),
		}, true, nil
	}

	// Find for .git file or directory
	dotGit, found := FindFileOrDirectory(ctx, fp, ".git")
	if !found {
		return Result{}, false, nil
	}

	// Find for gitdir path
	gitdir, err := findGitdir(ctx, dotGit)
	if err != nil {
		return Result{}, false, fmt.Errorf("error finding gitdir: %s", err)
	}

	// Commonly .git folder is present when it's a worktree but there's an exception where
	// worktree is present but .git folder is not present. In that case, we need to find
	// for worktree folder.
	// Find for commondir file
	commondir, ok, err := findCommondir(ctx, gitdir)
	if err != nil {
		return Result{}, false, fmt.Errorf("error finding commondir: %s", err)
	}

	if ok {
		dir := commondir
		projectName := strings.TrimSuffix(filepath.Base(commondir), ".git")

		if filepath.Base(commondir) == ".git" {
			dir = filepath.Dir(commondir)
			projectName = filepath.Base(dir)
		}

		project := projectOrRemote(ctx, projectName, g.ProjectFromGitRemote, commondir)

		branch, err := findGitBranch(ctx, filepath.Join(gitdir, "HEAD"))
		if err != nil {
			logger.Errorf(
				"error finding branch from %q: %s",
				filepath.Join(filepath.Dir(dotGit), "HEAD"),
				err,
			)
		}

		return Result{
			Project: project,
			Branch:  branch,
			Folder:  dir,
		}, true, nil
	}

	// Otherwise it's only a plain .git file and not a submodule
	if gitdir != "" && !strings.Contains(gitdir, "modules") {
		project := projectOrRemote(ctx, filepath.Base(filepath.Join(dotGit, "..")), g.ProjectFromGitRemote, gitdir)

		branch, err := findGitBranch(ctx, filepath.Join(gitdir, "HEAD"))
		if err != nil {
			logger.Errorf(
				"error finding branch from %q: %s",
				filepath.Join(filepath.Dir(gitdir), "HEAD"),
				err,
			)
		}

		return Result{
			Project: project,
			Branch:  branch,
			Folder:  filepath.Join(gitdir, ".."),
		}, true, nil
	}

	// Find for .git/config file
	gitConfigFile, found := FindFileOrDirectory(ctx, fp, filepath.Join(".git", "config"))

	if found {
		gitDir := filepath.Dir(gitConfigFile)
		projectDir := filepath.Join(gitDir, "..")

		branch, err := findGitBranch(ctx, filepath.Join(gitDir, "HEAD"))
		if err != nil {
			logger.Errorf(
				"error finding branch from %q: %s",
				filepath.Join(gitDir, "HEAD"),
				err,
			)
		}

		project := projectOrRemote(ctx, filepath.Base(projectDir), g.ProjectFromGitRemote, gitDir)

		return Result{
			Project: project,
			Branch:  branch,
			Folder:  projectDir,
		}, true, nil
	}

	return Result{}, false, nil
}

func findSubmodule(ctx context.Context, fp string, patterns []regex.Regex) (string, bool, error) {
	if !shouldTakeSubmodule(ctx, fp, patterns) {
		return "", false, nil
	}

	gitConfigFile, found := FindFileOrDirectory(ctx, fp, ".git")
	if !found {
		return "", false, nil
	}

	gitdir, err := findGitdir(ctx, gitConfigFile)
	if err != nil {
		return "", false,
			fmt.Errorf("error finding gitdir for submodule: %s", err)
	}

	if !strings.Contains(gitdir, "modules") {
		return "", false, nil
	}

	_, isWorktree, err := findCommondir(ctx, gitdir)
	if err != nil {
		return "", false, err
	}

	if isWorktree {
		return "", false, nil
	}

	return gitdir, true, nil
}

// shouldTakeSubmodule checks a filepath against the passed in regex patterns to determine,
// if submodule filepath should be taken.
func shouldTakeSubmodule(ctx context.Context, fp string, patterns []regex.Regex) bool {
	for _, p := range patterns {
		if p.MatchString(ctx, fp) {
			return false
		}
	}

	return true
}

func findGitdir(ctx context.Context, fp string) (string, error) {
	lines, err := file.ReadLines(ctx, fp, 1)
	if err != nil {
		return "", fmt.Errorf("failed while opening file %q: %s", fp, err)
	}

	if len(lines) > 0 && strings.HasPrefix(lines[0], "gitdir: ") {
		if arr := strings.Split(lines[0], "gitdir: "); len(arr) > 1 {
			return resolveGitdir(filepath.Join(fp, ".."), arr[1])
		}
	}

	return "", nil
}

func resolveGitdir(fp, gitdir string) (string, error) {
	subPath := strings.TrimSpace(gitdir)
	if !filepath.IsAbs(subPath) {
		subPath = filepath.Join(fp, subPath)
	}

	if fileOrDirExists(filepath.Join(subPath, "HEAD")) {
		return subPath, nil
	}

	return "", nil
}

func findCommondir(ctx context.Context, fp string) (string, bool, error) {
	if fp == "" {
		return "", false, nil
	}

	if filepath.Base(filepath.Dir(fp)) != "worktrees" {
		return "", false, nil
	}

	if fileOrDirExists(filepath.Join(fp, "commondir")) {
		return resolveCommondir(ctx, fp)
	}

	return "", false, nil
}

func resolveCommondir(ctx context.Context, fp string) (string, bool, error) {
	lines, err := file.ReadLines(ctx, filepath.Join(fp, "commondir"), 1)
	if err != nil {
		return "", false,
			fmt.Errorf("failed while opening file %q: %s", fp, err)
	}

	if len(lines) == 0 {
		return "", false, nil
	}

	gitdir, err := filepath.Abs(filepath.Join(fp, lines[0]))
	if err != nil {
		return "", false,
			fmt.Errorf("failed to get absolute path: %s", err)
	}

	return gitdir, true, nil
}

func projectOrRemote(ctx context.Context, projectName string, projectFromGitRemote bool, dotGitFolder string) string {
	if !projectFromGitRemote {
		return projectName
	}

	logger := log.Extract(ctx)
	configFile := filepath.Join(dotGitFolder, "config")

	remote, err := findGitRemote(ctx, configFile)
	if err != nil {
		logger.Errorf("error finding git remote from %q: %s", configFile, err)

		return projectName
	}

	if remote != "" {
		return remote
	}

	return projectName
}

func findGitBranch(ctx context.Context, fp string) (string, error) {
	if !fileOrDirExists(fp) {
		return "master", nil
	}

	lines, err := file.ReadLines(ctx, fp, 1)
	if err != nil {
		return "", fmt.Errorf("failed while opening file %q: %s", fp, err)
	}

	logger := log.Extract(ctx)

	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "ref: ") {
		parts := strings.SplitN(lines[0], "/", 3)
		if len(parts) < 3 {
			logger.Warnf("invalid branch from %q: %s", fp, lines[0])

			return "", nil
		}

		return strings.TrimSpace(strings.SplitN(lines[0], "/", 3)[2]), nil
	}

	return "", nil
}

func findGitRemote(ctx context.Context, fp string) (string, error) {
	if !fileOrDirExists(fp) {
		return "", nil
	}

	lines, err := file.ReadLines(ctx, fp, 1000)
	if err != nil {
		return "", fmt.Errorf("failed while opening file %q: %s", fp, err)
	}

	inOrigin := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if header, ok := strings.CutPrefix(line, "["); ok {
			section, _, closed := strings.Cut(header, "]")
			parts := strings.Fields(section)
			inOrigin = closed && len(parts) == 2 && strings.EqualFold(parts[0], "remote") && parts[1] == `"origin"`

			continue
		}

		if !inOrigin {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "url") {
			remote, err := gitConfigRemoteValue(strings.TrimSpace(value))
			if err != nil {
				return "", err
			}

			return gitRemoteProject(remote)
		}
	}

	return "", nil
}

func gitConfigRemoteValue(value string) (string, error) {
	if !strings.HasPrefix(value, `"`) {
		if end := strings.IndexAny(value, "#;"); end >= 0 {
			value = value[:end]
		}

		if strings.ContainsRune(value, '"') {
			return "", nil
		}

		return strings.TrimSpace(value), nil
	}

	quoted, err := strconv.QuotedPrefix(value)
	if err != nil {
		return "", errors.New("invalid quoted Git remote URL")
	}

	tail := strings.TrimSpace(value[len(quoted):])
	if tail != "" && strings.IndexAny(tail, "#;") != 0 {
		return "", nil
	}

	return strconv.Unquote(quoted)
}

func gitRemoteProject(remote string) (string, error) {
	host, path, ok := strings.Cut(remote, ":")
	if !ok || host == "" || strings.ContainsAny(host, `/\`) || filepath.VolumeName(remote) != "" {
		return "", nil
	}

	if strings.HasPrefix(path, "//") {
		switch strings.ToLower(host) {
		case "ftp", "ftps", "git", "http", "https", "ssh":
			parsed, err := url.Parse(remote)
			if err != nil || parsed.Hostname() == "" {
				return "", errors.New("invalid Git remote URL")
			}

			path = strings.TrimPrefix(parsed.Path, "/")
		default:
			return "", nil
		}
	} else {
		if strings.Contains(host, "[") {
			_, path, ok = strings.Cut(remote, "]:")
		}

		if !ok || strings.HasPrefix(path, ":") || strings.HasPrefix(path, `\`) {
			return "", nil
		}
	}

	return strings.TrimSuffix(path, ".git"), nil
}

// ID returns its id.
func (Git) ID() DetectorID {
	return GitDetector
}
