package commands

// DefaultRegistry returns a registry with all built-in commands registered.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register("fix-ci", handleFixCI)
	r.Register("retry", handleRetry)
	r.Register("review", handleReview)
	r.Register("fix", handleFix)
	r.Register("integrate", handleIntegrate)
	return r
}
