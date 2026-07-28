- Every function must have unit tests covering edge cases.
- Use table-driven tests where applicable.
- Use testify/assert and testify/require, not raw if/t.Fatal.
- Before pushing, you MUST run ALL of the following and fix any failures:
  1. go build ./...
  2. go test ./... -count=1 -race
  3. go vet ./...
  4. golangci-lint run
- Do NOT substitute weaker validation commands. Plain `go test ./...` is not sufficient for this repository because CI runs the race detector.
- Do NOT push code that fails any of these checks. Fix the issues first.
