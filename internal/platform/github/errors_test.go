package github

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	gh "github.com/google/go-github/v68/github"
	"github.com/stretchr/testify/assert"
)

func TestIsMilestoneAlreadyExists(t *testing.T) {
	newResp := func() *http.Response {
		return &http.Response{
			StatusCode: 422,
			Request: &http.Request{
				Method: "POST",
				URL:    &url.URL{Path: "/repos/o/r/milestones"},
			},
		}
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "plain string error",
			err:  errors.New("some string"),
			want: false,
		},
		{
			name: "ErrorResponse with already_exists code",
			err: &gh.ErrorResponse{
				Response: newResp(),
				Errors: []gh.Error{
					{Code: "already_exists", Resource: "Milestone", Field: "title"},
				},
			},
			want: true,
		},
		{
			name: "ErrorResponse with different code",
			err: &gh.ErrorResponse{
				Response: newResp(),
				Errors: []gh.Error{
					{Code: "missing_field"},
				},
			},
			want: false,
		},
		{
			name: "wrapped ErrorResponse with already_exists code",
			err: fmt.Errorf("creating milestone: %w", &gh.ErrorResponse{
				Response: newResp(),
				Errors: []gh.Error{
					{Code: "already_exists"},
				},
			}),
			want: true,
		},
		{
			name: "ErrorResponse with empty Errors slice",
			err: &gh.ErrorResponse{
				Response: newResp(),
				Errors:   []gh.Error{},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsMilestoneAlreadyExists(tc.err))
		})
	}
}
