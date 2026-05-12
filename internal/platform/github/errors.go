package github

import (
	"errors"

	gh "github.com/google/go-github/v68/github"
)

// IsMilestoneAlreadyExists reports whether err is a GitHub ErrorResponse
// indicating that a milestone with the requested title already exists.
// It inspects the structured Errors[] payload (Code == "already_exists")
// rather than string-matching the message.
func IsMilestoneAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var gerr *gh.ErrorResponse
	if !errors.As(err, &gerr) {
		return false
	}
	for _, e := range gerr.Errors {
		if e.Code == "already_exists" {
			return true
		}
	}
	return false
}
