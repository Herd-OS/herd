package issues

import "fmt"

// validTransitions defines the allowed state transitions.
var validTransitions = map[string][]string{
	StatusBlocked:    {StatusReady, StatusFailed, StatusCancelled},
	StatusReady:      {StatusInProgress, StatusFailed, StatusCancelled},
	StatusInProgress: {StatusDone, StatusFailed, StatusCancelled},
	StatusFailed:     {StatusReady, StatusCancelled},
	StatusDone:       {StatusFailed},
	StatusCancelled:  {},
}

// ValidateTransition checks whether transitioning from one status to another is allowed.
func ValidateTransition(from, to string) error {
	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("unknown status: %s", from)
	}
	for _, a := range allowed {
		if a == to {
			return nil
		}
	}
	return fmt.Errorf("invalid transition: %s → %s", from, to)
}
