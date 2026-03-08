package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v68/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabelServiceCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/labels", func(w http.ResponseWriter, r *http.Request) {
		var label gh.Label
		require.NoError(t, json.NewDecoder(r.Body).Decode(&label))
		assert.Equal(t, "herd/status:ready", *label.Name)
		assert.Equal(t, "0e8a16", *label.Color) // # should be stripped

		resp := gh.Label{Name: gh.Ptr("herd/status:ready"), Color: gh.Ptr("0e8a16")}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	err := client.Labels().Create(context.Background(), "herd/status:ready", "#0e8a16", "Ready for dispatch")

	require.NoError(t, err)
}

func TestLabelServiceCreateNoHash(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/labels", func(w http.ResponseWriter, r *http.Request) {
		var label gh.Label
		require.NoError(t, json.NewDecoder(r.Body).Decode(&label))
		assert.Equal(t, "0e8a16", *label.Color) // no # to strip

		resp := gh.Label{Name: gh.Ptr("test"), Color: gh.Ptr("0e8a16")}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	err := client.Labels().Create(context.Background(), "test", "0e8a16", "desc")

	require.NoError(t, err)
}

func TestLabelServiceList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/labels", func(w http.ResponseWriter, r *http.Request) {
		resp := []*gh.Label{
			{Name: gh.Ptr("herd/status:ready"), Color: gh.Ptr("0e8a16"), Description: gh.Ptr("Ready")},
			{Name: gh.Ptr("herd/type:feature"), Color: gh.Ptr("1d76db"), Description: gh.Ptr("Feature")},
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	labels, err := client.Labels().List(context.Background())

	require.NoError(t, err)
	assert.Len(t, labels, 2)
	assert.Equal(t, "herd/status:ready", labels[0].Name)
	assert.Equal(t, "herd/type:feature", labels[1].Name)
}

func TestLabelServiceDelete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /repos/test-org/test-repo/labels/herd/status:ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	client, _ := newTestClient(t, mux)
	err := client.Labels().Delete(context.Background(), "herd/status:ready")

	require.NoError(t, err)
}
