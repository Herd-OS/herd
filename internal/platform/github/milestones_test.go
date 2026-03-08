package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMilestoneServiceCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/milestones", func(w http.ResponseWriter, r *http.Request) {
		var m gh.Milestone
		require.NoError(t, json.NewDecoder(r.Body).Decode(&m))
		assert.Equal(t, "M2: Plan & Dispatch", *m.Title)

		resp := gh.Milestone{
			Number: gh.Ptr(2),
			Title:  gh.Ptr("M2: Plan & Dispatch"),
			State:  gh.Ptr("open"),
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	m, err := client.Milestones().Create(context.Background(), "M2: Plan & Dispatch", "", nil)

	require.NoError(t, err)
	assert.Equal(t, 2, m.Number)
	assert.Equal(t, "M2: Plan & Dispatch", m.Title)
	assert.Equal(t, "open", m.State)
}

func TestMilestoneServiceGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/milestones/2", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.Milestone{
			Number:       gh.Ptr(2),
			Title:        gh.Ptr("M2"),
			State:        gh.Ptr("open"),
			OpenIssues:   gh.Ptr(3),
			ClosedIssues: gh.Ptr(2),
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	m, err := client.Milestones().Get(context.Background(), 2)

	require.NoError(t, err)
	assert.Equal(t, 2, m.Number)
	assert.Equal(t, 3, m.OpenIssues)
	assert.Equal(t, 2, m.ClosedIssues)
}

func TestMilestoneServiceList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/milestones", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		resp := []*gh.Milestone{
			{Number: gh.Ptr(1), Title: gh.Ptr("M1"), State: gh.Ptr("open")},
			{Number: gh.Ptr(2), Title: gh.Ptr("M2"), State: gh.Ptr("open")},
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	milestones, err := client.Milestones().List(context.Background())

	require.NoError(t, err)
	assert.Len(t, milestones, 2)
}

func TestMilestoneServiceUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /repos/test-org/test-repo/milestones/2", func(w http.ResponseWriter, r *http.Request) {
		var m gh.Milestone
		require.NoError(t, json.NewDecoder(r.Body).Decode(&m))
		assert.Equal(t, "closed", *m.State)

		resp := gh.Milestone{
			Number: gh.Ptr(2),
			Title:  gh.Ptr("M2"),
			State:  gh.Ptr("closed"),
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	state := "closed"
	m, err := client.Milestones().Update(context.Background(), 2, platform.MilestoneUpdate{State: &state})

	require.NoError(t, err)
	assert.Equal(t, "closed", m.State)
}
