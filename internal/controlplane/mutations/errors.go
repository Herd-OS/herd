package mutations

import "fmt"

// PreCallError identifies failures that happen before a GitHub-visible
// mutation request is issued, such as App token minting or client setup.
type PreCallError struct {
	Op  string
	Err error
}

func (e PreCallError) Error() string {
	if e.Op == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e PreCallError) Unwrap() error {
	return e.Err
}
