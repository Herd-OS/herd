package dashboard

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/planner"
	"github.com/herd-os/herd/internal/platform"
)

// Fetch builds a fresh State by calling platform APIs in parallel. Individual
// failures are captured as a non-fatal error string (returned as the second
// value); the State is still returned with whatever data was retrieved.
func Fetch(ctx context.Context, p platform.Platform, owner, repo string) (State, string) {
	s := State{Owner: owner, Repo: repo, LastRefresh: time.Now()}
	var errs []string
	var mu sync.Mutex
	addErr := func(e error, what string) {
		if e == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, fmt.Sprintf("%s: %v", what, e))
	}

	var wg sync.WaitGroup
	var runs []*platform.Run
	var milestones []*platform.Milestone

	wg.Add(2)
	go func() {
		defer wg.Done()
		r, err := p.Workflows().ListRuns(ctx, platform.RunFilters{
			WorkflowFileName: "herd-worker.yml", Status: "in_progress",
		})
		if err != nil {
			addErr(err, "list worker runs")
			return
		}
		runs = r
	}()
	go func() {
		defer wg.Done()
		m, err := p.Milestones().List(ctx)
		if err != nil {
			addErr(err, "list milestones")
			return
		}
		milestones = m
	}()
	wg.Wait()

	// Workers (sequentially enrich titles — usually small N).
	for _, r := range runs {
		we := WorkerEntry{RunID: r.ID, URL: r.URL, StartedAt: r.CreatedAt}
		if v, ok := r.Inputs["issue_number"]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				we.IssueNumber = n
			}
		}
		if we.IssueNumber > 0 {
			if iss, err := p.Issues().Get(ctx, we.IssueNumber); err == nil {
				we.IssueTitle = iss.Title
			}
		}
		s.Workers = append(s.Workers, we)
	}

	// Batches: open milestones in parallel.
	type result struct {
		entry BatchEntry
		ok    bool
	}
	results := make(chan result, len(milestones))
	var bwg sync.WaitGroup
	for _, ms := range milestones {
		if ms.State != "open" {
			continue
		}
		bwg.Add(1)
		go func(ms *platform.Milestone) {
			defer bwg.Done()
			be, ok := buildBatchEntry(ctx, p, ms, owner, repo, addErr)
			results <- result{be, ok}
		}(ms)
	}
	go func() { bwg.Wait(); close(results) }()
	for r := range results {
		if !r.ok {
			continue
		}
		s.Batches = append(s.Batches, r.entry)
	}
	SortBatches(s.Batches)

	// Recent failures: closed-or-open issues with herd/status:failed updated in last 24h.
	failed, err := p.Issues().List(ctx, platform.IssueFilters{
		State: "all", Labels: []string{issues.StatusFailed},
	})
	if err != nil {
		addErr(err, "list failures")
	} else {
		cutoff := time.Now().Add(-24 * time.Hour)
		for _, iss := range failed {
			if iss.UpdatedAt.Before(cutoff) {
				continue
			}
			s.Failures = append(s.Failures, FailureEntry{
				Number: iss.Number, Title: iss.Title,
				Label: primaryTypeLabel(iss.Labels), UpdatedAt: iss.UpdatedAt,
			})
		}
	}

	if len(errs) == 0 {
		return s, ""
	}
	return s, strings.Join(errs, "; ")
}

func buildBatchEntry(ctx context.Context, p platform.Platform, ms *platform.Milestone, owner, repo string, addErr func(error, string)) (BatchEntry, bool) {
	issueList, err := p.Issues().List(ctx, platform.IssueFilters{State: "all", Milestone: &ms.Number})
	if err != nil {
		addErr(err, fmt.Sprintf("list issues for #%d", ms.Number))
		return BatchEntry{}, false
	}
	// Skip milestones that contain no herd/* issue.
	hasHerd := false
	for _, iss := range issueList {
		for _, l := range iss.Labels {
			if strings.HasPrefix(l, "herd/") {
				hasHerd = true
				break
			}
		}
		if hasHerd {
			break
		}
	}
	if !hasHerd {
		return BatchEntry{}, false
	}

	be := BatchEntry{MilestoneNumber: ms.Number, MilestoneTitle: ms.Title}
	var latest time.Time
	for _, iss := range issueList {
		if iss.UpdatedAt.After(latest) {
			latest = iss.UpdatedAt
		}
		switch issues.StatusLabel(iss.Labels) {
		case issues.StatusDone:
			be.Done++
		case issues.StatusInProgress:
			be.InProgress++
		case issues.StatusReady:
			be.Ready++
		case issues.StatusFailed:
			be.Failed++
		case issues.StatusBlocked:
			be.Blocked++
		}
	}
	be.LatestActivity = latest
	be.HasAttention = be.Failed > 0

	// Tier and total tiers — placeholder for v1; richer DAG-based tier
	// extraction is out of scope for this task.
	be.TotalTiers = 1
	be.Tier = 1

	// Look up batch PR by branch name.
	branch := fmt.Sprintf("herd/batch/%d-%s", ms.Number, planner.Slugify(ms.Title))
	prs, err := p.PullRequests().List(ctx, platform.PRFilters{State: "open", Head: branch})
	if err != nil {
		addErr(err, fmt.Sprintf("list PR for #%d", ms.Number))
	}
	be.MilestoneURL = fmt.Sprintf("https://github.com/%s/%s/milestone/%d", owner, repo, ms.Number)
	if len(prs) > 0 {
		pr := prs[0]
		be.PRNumber = pr.Number
		be.PRURL = pr.URL
		if issues.HasLabel(pr.Labels, issues.CascadeFailed) {
			be.CascadeFailed = true
			be.HasAttention = true
		}
		if issues.HasLabel(pr.Labels, issues.StableDisagreement) {
			be.StableDisagreement = true
			be.HasAttention = true
		}
		if status, err := p.Checks().GetCombinedStatus(ctx, pr.Head); err == nil {
			be.CIStatus = status
			if status == "failure" {
				be.HasAttention = true
			}
		}
	}
	return be, true
}

func primaryTypeLabel(labels []string) string {
	for _, l := range labels {
		if strings.HasPrefix(l, "herd/type:") {
			return l
		}
	}
	return ""
}
