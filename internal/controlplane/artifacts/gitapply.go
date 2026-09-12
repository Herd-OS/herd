package artifacts

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
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

// PreparedApply is a local, retryable patch preparation whose GitHub-visible
// side effect has not happened yet. Callers that maintain durable mutation
// state must complete all pre-call work with Prepare, record call_started, then
// invoke Push exactly at that mutation boundary.
type PreparedApply struct {
	CommitSHA string
	push      func() error
	cleanup   func()
}

func NewPreparedApply(commitSHA string, push func() error, cleanup func()) PreparedApply {
	return PreparedApply{CommitSHA: commitSHA, push: push, cleanup: cleanup}
}

func (p PreparedApply) Push() (ApplyResult, error) {
	if p.push == nil {
		return ApplyResult{}, fmt.Errorf("prepared patch push is not configured")
	}
	if err := p.push(); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{CommitSHA: p.CommitSHA}, nil
}

func (p PreparedApply) Cleanup() {
	if p.cleanup != nil {
		p.cleanup()
	}
}

type PreCallError struct {
	Err error
}

func (e PreCallError) Error() string {
	return e.Err.Error()
}

func (e PreCallError) Unwrap() error {
	return e.Err
}

func preCallError(err error) error {
	if err == nil {
		return nil
	}
	return PreCallError{Err: err}
}

func Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	prepared, err := Prepare(ctx, req)
	if err != nil {
		return ApplyResult{}, err
	}
	defer prepared.Cleanup()
	return prepared.Push()
}

func Prepare(ctx context.Context, req ApplyRequest) (PreparedApply, error) {
	if err := validateApplyRequest(req); err != nil {
		return PreparedApply{}, preCallError(err)
	}
	root := req.TempDir
	removeRoot := false
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "herd-artifact-apply-*")
		if err != nil {
			return PreparedApply{}, preCallError(err)
		}
		removeRoot = true
	} else if err := os.MkdirAll(root, 0755); err != nil {
		return PreparedApply{}, preCallError(err)
	}
	cleanup := func() {
		if removeRoot {
			_ = os.RemoveAll(root)
		}
	}
	success := false
	defer func() {
		if !success {
			cleanup()
		}
	}()

	cloneURL := req.CloneURL
	var gitConfig []string
	var gitEnv []string
	var tokenValue string
	if req.TokenSource != nil {
		if err := validateTrustedGitHubCloneURL(req.CloneURL, req.Repository); err != nil {
			return PreparedApply{}, preCallError(err)
		}
		token, err := req.TokenSource.InstallationToken(ctx, req.InstallationID)
		if err != nil {
			return PreparedApply{}, preCallError(fmt.Errorf("get installation token: %w", err))
		}
		if strings.TrimSpace(token.Token) == "" {
			return PreparedApply{}, preCallError(fmt.Errorf("empty installation token"))
		}
		tokenValue = token.Token
		authEnv, authCleanup, err := gitAuthEnv(root, req.CloneURL, token.Token)
		if err != nil {
			return PreparedApply{}, preCallError(err)
		}
		previousCleanup := cleanup
		cleanup = func() {
			authCleanup()
			previousCleanup()
		}
		gitEnv = authEnv
	}

	repoDir := filepath.Join(root, "repo")
	if err := herdgit.CloneWithConfigAndEnv(cloneURL, repoDir, gitConfig, gitEnv); err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	g := herdgit.NewWithConfigAndEnv(repoDir, gitConfig, gitEnv)
	if err := g.Fetch("origin"); err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	current, err := g.RemoteBranchSHA("origin", req.TargetBranch)
	if err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	if current != req.ExpectedHeadSHA {
		return PreparedApply{}, preCallError(fmt.Errorf("target branch advanced: expected %s, got %s", req.ExpectedHeadSHA, current))
	}
	if err := g.CheckoutDetached(req.BaseSHA); err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	if req.Artifact.Metadata.BaseSHA != req.BaseSHA {
		return PreparedApply{}, preCallError(fmt.Errorf("stale patch base SHA: expected %s, got %s", req.BaseSHA, req.Artifact.Metadata.BaseSHA))
	}
	patchFile := filepath.Join(root, "artifact.patch")
	if err := os.WriteFile(patchFile, req.Artifact.Data, 0600); err != nil {
		return PreparedApply{}, preCallError(err)
	}
	if err := g.ApplyBinaryPatch(patchFile); err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	if err := g.ConfigureIdentity(req.Identity.Name, req.Identity.Email); err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	dirty, err := g.IsDirty()
	if err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	if !dirty {
		return PreparedApply{}, preCallError(fmt.Errorf("patch artifact produced no changes"))
	}
	if err := g.Commit(commitMessage(req)); err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	commitSHA, err := g.HeadSHA()
	if err != nil {
		return PreparedApply{}, preCallError(redactToken(err, tokenValue))
	}
	success = true
	return PreparedApply{
		CommitSHA: commitSHA,
		cleanup:   cleanup,
		push: func() error {
			return redactToken(g.PushHEAD("origin", req.TargetBranch, req.ExpectedHeadSHA), tokenValue)
		},
	}, nil
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
	if req.BaseSHA != req.ExpectedHeadSHA {
		return fmt.Errorf("patch base SHA must match expected head SHA")
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

func validateTrustedGitHubCloneURL(cloneURL, repository string) error {
	parsed, err := url.Parse(strings.TrimSpace(cloneURL))
	if err != nil {
		return fmt.Errorf("clone URL must be a trusted GitHub HTTPS URL for %s: %w", repository, err)
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("clone URL must be a trusted GitHub HTTPS URL for %s", repository)
	}
	owner, name, ok := strings.Cut(strings.Trim(strings.TrimSpace(repository), "/"), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("repository must be owner/name")
	}
	path := strings.Trim(strings.TrimSpace(parsed.EscapedPath()), "/")
	path, err = url.PathUnescape(path)
	if err != nil {
		return fmt.Errorf("decode clone URL path: %w", err)
	}
	want := owner + "/" + strings.TrimSuffix(name, ".git")
	got := strings.TrimSuffix(path, ".git")
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("clone URL path %q does not match repository %q", got, want)
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
	tokenFile := filepath.Join(root, "git-token")
	if err := os.WriteFile(tokenFile, []byte(token), 0600); err != nil {
		return nil, func() {}, fmt.Errorf("write git token file: %w", err)
	}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			_ = os.Remove(tokenFile)
		}
	}()
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"*Username*) printf '%s\\n' 'x-access-token' ;;\n" +
		"*Password*) cat \"$HERD_GIT_ASKPASS_TOKEN_FILE\" ;;\n" +
		"*) printf '\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(askpass, []byte(script), 0700); err != nil {
		return nil, func() {}, fmt.Errorf("write git askpass helper: %w", err)
	}
	cleanupOnError = false
	cleanup := func() {
		_ = os.Remove(askpass)
		_ = os.Remove(tokenFile)
	}
	return []string{
		"GIT_ASKPASS=" + askpass,
		"HERD_GIT_ASKPASS_TOKEN_FILE=" + tokenFile,
		"GIT_TERMINAL_PROMPT=0",
	}, cleanup, nil
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
