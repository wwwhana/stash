package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alash3al/stash/internal/brain"
	"github.com/urfave/cli/v3"
)

type workspaceFacts struct {
	brain.WorkspaceIdentityInput
	ProjectNamespace string `json:"project_namespace,omitempty"`
}

func commandNeedsBootstrap(args []string) bool {
	return len(args) < 3 || args[1] != "workspace" || args[2] != "facts"
}

func optionalGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(output)), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 2 || exitErr.ExitCode() == 128) {
		return "", nil
	}
	return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
}

func newRepositoryInstanceID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate repository instance ID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func absoluteGitPath(ctx context.Context, dir, selector string) (string, error) {
	value, err := optionalGitOutput(ctx, dir, "rev-parse", "--path-format=absolute", selector)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("git rev-parse %s returned no path", selector)
	}
	return filepath.Clean(value), nil
}

func inferRepositoryProvider(remote string) string {
	normalized, err := brain.NormalizeWorkspaceRemoteURL(remote)
	if err != nil {
		return ""
	}
	host := normalized
	if slash := strings.IndexByte(host, '/'); slash >= 0 {
		host = host[:slash]
	}
	switch {
	case host == "github.com":
		return "github"
	case host == "gitlab.com" || strings.Contains(host, "gitlab"):
		return "gitlab"
	case strings.Contains(host, "gitea"):
		return "gitea"
	default:
		return "git"
	}
}

func collectWorkspaceFacts(ctx context.Context, cwd, agentID, projectNamespace string, initialize bool) (*workspaceFacts, error) {
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve current directory: %w", err)
	}
	if canonicalCWD, err := filepath.EvalSymlinks(absCWD); err == nil {
		absCWD = canonicalCWD
	}
	if info, err := os.Stat(absCWD); err != nil || !info.IsDir() {
		if err != nil {
			return nil, fmt.Errorf("current directory: %w", err)
		}
		return nil, fmt.Errorf("current directory is not a directory: %s", absCWD)
	}
	worktreePath, err := optionalGitOutput(ctx, absCWD, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	if worktreePath == "" {
		return nil, fmt.Errorf("current directory is not inside a Git worktree")
	}
	worktreePath, err = filepath.Abs(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree path: %w", err)
	}
	gitCommonDir, err := absoluteGitPath(ctx, absCWD, "--git-common-dir")
	if err != nil {
		return nil, err
	}
	gitDir, err := absoluteGitPath(ctx, absCWD, "--git-dir")
	if err != nil {
		return nil, err
	}
	repositoryInstanceID, err := optionalGitOutput(ctx, absCWD, "config", "--local", "--get", "stash.repositoryInstanceId")
	if err != nil {
		return nil, err
	}
	if repositoryInstanceID == "" {
		if !initialize {
			return nil, fmt.Errorf("stash.repositoryInstanceId is missing; rerun without --no-init")
		}
		repositoryInstanceID, err = newRepositoryInstanceID()
		if err != nil {
			return nil, err
		}
		if _, err := runGit(ctx, absCWD, "config", "--local", "stash.repositoryInstanceId", repositoryInstanceID); err != nil {
			return nil, err
		}
	}
	configuredNamespace, err := optionalGitOutput(ctx, absCWD, "config", "--local", "--get", "stash.projectNamespace")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(projectNamespace) == "" {
		projectNamespace = configuredNamespace
	} else if initialize && projectNamespace != configuredNamespace {
		if _, err := runGit(ctx, absCWD, "config", "--local", "stash.projectNamespace", projectNamespace); err != nil {
			return nil, err
		}
	}
	remoteName, err := optionalGitOutput(ctx, absCWD, "config", "--local", "--get", "stash.remoteName")
	if err != nil {
		return nil, err
	}
	if remoteName == "" {
		remoteName = "origin"
	}
	remoteURL, err := optionalGitOutput(ctx, absCWD, "remote", "get-url", remoteName)
	if err != nil {
		return nil, err
	}
	provider, err := optionalGitOutput(ctx, absCWD, "config", "--local", "--get", "stash.repositoryProvider")
	if err != nil {
		return nil, err
	}
	if provider == "" && remoteURL != "" {
		provider = inferRepositoryProvider(remoteURL)
	}
	providerID, err := optionalGitOutput(ctx, absCWD, "config", "--local", "--get", "stash.repositoryProviderId")
	if err != nil {
		return nil, err
	}
	branch, err := optionalGitOutput(ctx, absCWD, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return nil, err
	}
	headSHA, err := optionalGitOutput(ctx, absCWD, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, err
	}
	status, err := worktreeStatus(ctx, worktreePath)
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(map[string]any{"source": "stash workspace facts", "remote_name": remoteName})
	if err != nil {
		return nil, fmt.Errorf("marshal workspace metadata: %w", err)
	}
	return &workspaceFacts{
		WorkspaceIdentityInput: brain.WorkspaceIdentityInput{
			CWD:                  absCWD,
			RepositoryInstanceID: repositoryInstanceID,
			Provider:             provider,
			ProviderRepositoryID: providerID,
			RemoteURL:            remoteURL,
			GitCommonDir:         gitCommonDir,
			GitDir:               gitDir,
			WorktreePath:         filepath.Clean(worktreePath),
			Branch:               branch,
			HeadSHA:              headSHA,
			Status:               status,
			AgentID:              strings.TrimSpace(agentID),
			Metadata:             metadata,
		},
		ProjectNamespace: strings.TrimSpace(projectNamespace),
	}, nil
}

func workspaceFactsCmd(ctx context.Context, cmd *cli.Command) error {
	agentID := strings.TrimSpace(cmd.String("agent-id"))
	if agentID == "" {
		agentID = strings.TrimSpace(os.Getenv("STASH_AGENT_ID"))
	}
	facts, err := collectWorkspaceFacts(ctx, cmd.String("cwd"), agentID, cmd.String("project-namespace"), !cmd.Bool("no-init"))
	if err != nil {
		return err
	}
	return printJSON(facts)
}
