package config

import "fmt"

// BuildSecretID normalizes a configured secret/env name to a BuildKit secret ID.
func BuildSecretID(name string) string {
	id := make([]byte, 0, len(name))
	needSeparator := false

	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b >= 'A' && b <= 'Z':
			if needSeparator && len(id) > 0 {
				id = append(id, '_')
			}
			id = append(id, b+'a'-'A')
			needSeparator = false
		case (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9'):
			if needSeparator && len(id) > 0 {
				id = append(id, '_')
			}
			id = append(id, b)
			needSeparator = false
		default:
			if len(id) > 0 {
				needSeparator = true
			}
		}
	}

	return string(id)
}

// BuildSecretIDs normalizes configured secret/env names to BuildKit secret IDs.
func BuildSecretIDs(names []string) ([]string, error) {
	ids := make([]string, 0, len(names))
	seen := map[string]int{}
	for i, name := range names {
		id := BuildSecretID(name)
		if id == "" {
			return nil, fmt.Errorf("image_publish.build_secrets[%d] (%q) normalizes to empty BuildKit secret id", i, name)
		}
		if first, ok := seen[id]; ok {
			return nil, fmt.Errorf("image_publish.build_secrets[%d] (%q) normalizes to duplicate BuildKit secret id %q from image_publish.build_secrets[%d] (%q)", i, name, id, first, names[first])
		}
		seen[id] = i
		ids = append(ids, id)
	}
	return ids, nil
}
