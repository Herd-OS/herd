package codex

import (
	"embed"
	"fmt"
	"os"
)

// schemaFS holds the embedded JSON Schema documents used to constrain Codex's
// structured output for Plan and Review (via --output-schema).
//
// TODO(verify): the JSON Schema dialect that --output-schema accepts (draft-07
// vs 2020-12) is not confirmed in the Codex source. These schemas are
// implemented as JSON Schema 2020-12 strict mode (additionalProperties:false at
// every object level, all properties required) per the OpenAI Responses API
// convention. Iterate if Codex rejects them at runtime.
//
//go:embed schemas/plan.json
//go:embed schemas/review.json
var schemaFS embed.FS

// writeSchemaFile materializes an embedded schema (by base name, e.g.
// "plan.json") to a temp file and returns its path. The caller is responsible
// for removing the file (use defer os.Remove).
func writeSchemaFile(name string) (string, error) {
	data, err := schemaFS.ReadFile("schemas/" + name)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "codex-schema-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("writing schema temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("closing schema temp file: %w", err)
	}
	return f.Name(), nil
}
