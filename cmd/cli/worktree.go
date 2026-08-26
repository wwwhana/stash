package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/models"
	"github.com/urfave/cli/v3"
)

type gitWorktree struct {
	Path     string
	HeadSHA  string
	Branch   string
	Detached bool
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func parseGitWorktreeList(output string) ([]gitWorktree, error) {
	var result []gitWorktree
	var current *gitWorktree
	flush := func() {
		if current != nil && current.Path != "" && current.HeadSHA != "" {
			result = append(result, *current)
		}
		current = nil
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			current = &gitWorktree{Path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
		case current != nil && strings.HasPrefix(line, "HEAD "):
			current.HeadSHA = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case current != nil && strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "branch ")), "refs/heads/")
		case current != nil && line == "detached":
			current.Detached = true
		}
	}
	flush()
	if len(result) == 0 {
		return nil, fmt.Errorf("git worktree list returned no worktrees")
	}
	return result, nil
}

func worktreeRepository(ctx context.Context, path string) (string, error) {
	commonDir, err := runGit(ctx, path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	commonDirValue := strings.TrimSpace(string(commonDir))
	if !filepath.IsAbs(commonDirValue) {
		commonDirValue = filepath.Join(path, commonDirValue)
	}
	return filepath.Clean(commonDirValue), nil
}

func worktreeStatus(ctx context.Context, path string) (string, error) {
	output, err := runGit(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(output)) == "" {
		return "clean", nil
	}
	return "dirty", nil
}

func worktreeSyncCmd(ctx context.Context, cmd *cli.Command) error {
	repoPath, err := filepath.Abs(cmd.String("repo"))
	if err != nil {
		return fmt.Errorf("resolve repository path: %w", err)
	}
	if info, err := os.Stat(repoPath); err != nil || !info.IsDir() {
		if err != nil {
			return fmt.Errorf("repository path: %w", err)
		}
		return fmt.Errorf("repository path is not a directory: %s", repoPath)
	}

	output, err := runGit(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	worktrees, err := parseGitWorktreeList(string(output))
	if err != nil {
		return err
	}

	bc := getBootstrap(cmd)
	_, namespaceID, err := exactNamespaceID(ctx, bc, cmd.String("namespace"))
	if err != nil {
		return err
	}
	agentID := strings.TrimSpace(cmd.String("agent-id"))
	if agentID == "" {
		agentID = strings.TrimSpace(os.Getenv("STASH_AGENT_ID"))
	}

	registered := make([]models.Worktree, 0, len(worktrees))
	for _, item := range worktrees {
		path, err := filepath.Abs(item.Path)
		if err != nil {
			return fmt.Errorf("resolve worktree path %q: %w", item.Path, err)
		}
		status := "missing"
		repository, repoErr := worktreeRepository(ctx, path)
		if _, statErr := os.Stat(path); statErr == nil {
			if repoErr != nil {
				return fmt.Errorf("resolve repository for %s: %w", path, repoErr)
			}
			status, err = worktreeStatus(ctx, path)
			if err != nil {
				return fmt.Errorf("read status for %s: %w", path, err)
			}
		} else {
			// Git can retain a prunable worktree entry after its directory is
			// removed. Keep it visible so the board can show stale workspaces.
			repository, err = worktreeRepository(ctx, repoPath)
			if err != nil {
				return fmt.Errorf("resolve repository for missing worktree %s: %w", path, err)
			}
		}
		metadata, err := json.Marshal(map[string]any{
			"repo_path": repoPath,
			"detached":  item.Detached,
			"source":    "git worktree list",
		})
		if err != nil {
			return fmt.Errorf("marshal worktree metadata: %w", err)
		}
		worktree, err := bc.Brain.RegisterWorktree(ctx, namespaceID, repository, path, item.Branch, item.HeadSHA, status, agentID, metadata)
		if err != nil {
			return err
		}
		keyHash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s:%s:%s", worktree.ID, item.HeadSHA, item.Branch, status, agentID)))
		eventKey := "sync_" + hex.EncodeToString(keyHash[:16])
		eventPayload, err := json.Marshal(map[string]any{
			"repository":    repository,
			"worktree_path": path,
			"branch":        item.Branch,
			"head_sha":      item.HeadSHA,
			"status":        status,
			"agent_id":      agentID,
		})
		if err != nil {
			return fmt.Errorf("marshal worktree event: %w", err)
		}
		worktreeID := worktree.ID
		if _, err := bc.Brain.RecordWorkEvent(ctx, namespaceID, &worktreeID, nil, "worktree.synced", eventKey, eventPayload, nil); err != nil {
			return err
		}
		registered = append(registered, *worktree)
	}
	return printJSON(registered)
}

func worktreeListCmd(ctx context.Context, cmd *cli.Command) error {
	namespaces := cmd.StringSlice("namespaces")
	if len(namespaces) == 0 {
		namespaces = []string{"/"}
	}
	page := brain.Pagination{Limit: cmd.Int("limit"), Offset: cmd.Int("offset")}
	bc := getBootstrap(cmd)
	worktrees, err := bc.Brain.ListWorktrees(ctx, namespaces, page)
	if err != nil {
		return err
	}
	return printJSON(worktrees)
}
