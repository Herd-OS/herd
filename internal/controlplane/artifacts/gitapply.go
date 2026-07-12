package artifacts

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/appauth"
	herdgit "github.com/herd-os/herd/internal/git"
)

type CommitIdentity struct {
	Name  string
	Email string
}

type HumanAttribution struct {
	Name  string
	Email string
}

type TokenSource interface {
	InstallationToken(ctx context.Context, installationID int64) (appauth.InstallationToken, error)
}

type ApplyRequest struct {
	Repository      string
	CloneURL        string
	InstallationID  int64
	TargetBranch    string
	BaseSHA         string
	ExpectedHeadSHA string
	Artifact        ValidatedArtifact
	Identity        CommitIdentity
	Human           HumanAttribution
	TokenSource     TokenSource
	TempDir         string
	Now             func() time.Time
}

type ApplyResult struct {
	CommitSHA string `json:"commit_sha"`
}

func Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	if err := validateApplyRequest(req); err != nil {
		return ApplyResult{}, err
	}
	root := req.TempDir
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "herd-artifact-apply-*")
		if err != nil {
			return ApplyResult{}, err
		}
		defer func() {
			_ = os.RemoveAll(root)
		}()
	} else if err := os.MkdirAll(root, 0755); err != nil {
		return ApplyResult{}, err
	}

	cloneURL := req.CloneURL
	var gitConfig []string
	var gitEnv []string
	var tokenValue string
	if req.TokenSource != nil {
		token, err := req.TokenSource.InstallationToken(ctx, req.InstallationID)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("get installation token: %w", err)
		}
		tokenValue = token.Token
		authEnv, cleanup, err := gitAuthEnv(root, req.CloneURL, token.Token)
		if err != nil {
			return ApplyResult{}, err
		}
		defer cleanup()
		gitEnv = authEnv
	}

	repoDir := filepath.Join(root, "repo")
	if err := herdgit.CloneWithConfigAndEnv(cloneURL, repoDir, gitConfig, gitEnv); err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	g := herdgit.NewWithConfigAndEnv(repoDir, gitConfig, gitEnv)
	if err := g.Fetch("origin"); err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	current, err := g.RemoteBranchSHA("origin", req.TargetBranch)
	if err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	if current != req.ExpectedHeadSHA {
		return ApplyResult{}, fmt.Errorf("target branch advanced: expected %s, got %s", req.ExpectedHeadSHA, current)
	}
	if err := g.CheckoutDetached(req.BaseSHA); err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	if req.Artifact.Metadata.BaseSHA != req.BaseSHA {
		return ApplyResult{}, fmt.Errorf("stale patch base SHA: expected %s, got %s", req.BaseSHA, req.Artifact.Metadata.BaseSHA)
	}
	patchFile := filepath.Join(root, "artifact.patch")
	if err := os.WriteFile(patchFile, req.Artifact.Data, 0600); err != nil {
		return ApplyResult{}, err
	}
	if err := g.ApplyBinaryPatch(patchFile); err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	if err := g.ConfigureIdentity(req.Identity.Name, req.Identity.Email); err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	dirty, err := g.IsDirty()
	if err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	if !dirty {
		return ApplyResult{}, fmt.Errorf("patch artifact produced no changes")
	}
	if err := g.Commit(commitMessage(req)); err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	commitSHA, err := g.HeadSHA()
	if err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	if err := g.PushHEAD("origin", req.TargetBranch, req.ExpectedHeadSHA); err != nil {
		return ApplyResult{}, redactToken(err, tokenValue)
	}
	return ApplyResult{CommitSHA: commitSHA}, nil
}

func DefaultIdentity(appLogin, email string) CommitIdentity {
	name := strings.TrimSpace(appLogin)
	if name == "" {
		name = "HerdOS"
	}
	if email = strings.TrimSpace(email); email == "" {
		email = "herdos@users.noreply.github.com"
	}
	return CommitIdentity{Name: name, Email: email}
}

func validateApplyRequest(req ApplyRequest) error {
	if strings.TrimSpace(req.Repository) == "" {
		return fmt.Errorf("repository is required")
	}
	if strings.TrimSpace(req.CloneURL) == "" {
		return fmt.Errorf("clone URL is required")
	}
	if strings.TrimSpace(req.TargetBranch) == "" {
		return fmt.Errorf("target branch is required")
	}
	if strings.TrimSpace(req.BaseSHA) == "" {
		return fmt.Errorf("base SHA is required")
	}
	if strings.TrimSpace(req.ExpectedHeadSHA) == "" {
		return fmt.Errorf("expected head SHA is required")
	}
	if req.Artifact.Metadata.Format != FormatGitDiffBinary {
		return fmt.Errorf("unsupported patch artifact format %q", req.Artifact.Metadata.Format)
	}
	if req.Artifact.Metadata.Repository != req.Repository {
		return fmt.Errorf("patch repository does not match apply repository")
	}
	if req.Identity.Name == "" || req.Identity.Email == "" {
		return fmt.Errorf("commit identity is required")
	}
	if req.TokenSource != nil && req.InstallationID == 0 {
		return fmt.Errorf("installation ID is required")
	}
	return nil
}

func commitMessage(req ApplyRequest) string {
	message := fmt.Sprintf("Apply HerdOS worker changes for %s\n\nHerd-Job-ID: %s\nHerd-Base-SHA: %s", req.Repository, req.Artifact.Metadata.JobID, req.BaseSHA)
	if req.Human.Name != "" && req.Human.Email != "" {
		message += fmt.Sprintf("\nCo-authored-by: %s <%s>", req.Human.Name, req.Human.Email)
	}
	return message
}

func gitAuthEnv(root, cloneURL, token string) ([]string, func(), error) {
	if strings.TrimSpace(token) == "" || !strings.HasPrefix(cloneURL, "https://") {
		return nil, func() {}, nil
	}
	if strings.TrimSpace(root) == "" {
		return nil, func() {}, fmt.Errorf("temporary directory is required for git authentication")
	}
	askpass := filepath.Join(root, "git-askpass.sh")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"*Username*) printf '%s\\n' 'x-access-token' ;;\n" +
		"*Password*) printf '%s\\n' '" + shellSingleQuote(token) + "' ;;\n" +
		"*) printf '\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(askpass, []byte(script), 0600); err != nil {
		return nil, func() {}, fmt.Errorf("write git askpass helper: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(askpass)
	}
	return []string{
		"GIT_ASKPASS=" + askpass,
		"GIT_TERMINAL_PROMPT=0",
	}, cleanup, nil
}

func shellSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "'\"'\"'")
}

func redactToken(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	message := strings.ReplaceAll(err.Error(), "AUTHORIZATION: basic "+credential, "AUTHORIZATION: [REDACTED]")
	message = strings.ReplaceAll(message, credential, "[REDACTED]")
	message = strings.ReplaceAll(message, "x-access-token:"+token, "[REDACTED]")
	message = strings.ReplaceAll(message, token, "[REDACTED]")
	return errors.New(message)
}
