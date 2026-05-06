package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/display"
)

// errCheckDrift signals --check found drift. cmd/herd/main.go propagates a
// non-nil RunE error to a non-zero exit code.
var errCheckDrift = errors.New("herd init --check: drift detected")

// DriftedFile is a single file that would be modified by `herd init` if re-run today.
type DriftedFile struct {
	Path   string // relative to repo root
	Reason string // "missing", "content differs", or "would be created" for Dockerfile.herd_runner / .herdos.yml
}

// CheckHerdFilesUpToDate returns the list of herd-managed files in dir that drift
// from what the currently-installed herd binary would render. Returns nil, nil if
// everything matches. Returns a non-nil error only on unrecoverable conditions
// (e.g. cannot read embedded assets); a missing .herdos.yml is treated as drift,
// not an error.
func CheckHerdFilesUpToDate(dir string) ([]DriftedFile, error) {
	_, _, _, drifted, err := computeManagedDrift(dir)
	return drifted, err
}

// computeManagedDrift loads config, renders all herd-managed files, and computes
// the drift list in one pass. Returned to callers that need both the rendered
// file set and the drift result without re-doing the work.
func computeManagedDrift(dir string) (*config.Config, bool, []managedFile, []DriftedFile, error) {
	owner, repo, err := detectOwnerRepo(dir)
	if err != nil {
		return nil, false, nil, nil, fmt.Errorf("detecting repository: %w", err)
	}

	cfg, cfgMissing, err := loadConfigForCheck(dir, owner, repo)
	if err != nil {
		return nil, false, nil, nil, err
	}

	var drifted []DriftedFile
	if cfgMissing {
		drifted = append(drifted, DriftedFile{Path: config.ConfigFile, Reason: "would be created"})
	}

	files, err := renderManagedFiles(dir, owner, repo, cfg)
	if err != nil {
		return nil, false, nil, nil, fmt.Errorf("rendering managed files: %w", err)
	}

	for _, mf := range files {
		existing, readErr := os.ReadFile(filepath.Join(dir, mf.Path))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				drifted = append(drifted, DriftedFile{Path: mf.Path, Reason: "missing"})
				continue
			}
			return nil, false, nil, nil, fmt.Errorf("reading %s: %w", mf.Path, readErr)
		}
		if !bytes.Equal(existing, mf.Content) {
			drifted = append(drifted, DriftedFile{Path: mf.Path, Reason: "content differs"})
		}
	}

	herdRunnerPath := filepath.Join(dir, "Dockerfile.herd_runner")
	if _, err := os.Stat(herdRunnerPath); os.IsNotExist(err) {
		drifted = append(drifted, DriftedFile{Path: "Dockerfile.herd_runner", Reason: "would be created"})
	}

	return cfg, cfgMissing, files, drifted, nil
}

// loadConfigForCheck loads .herdos.yml, treating its absence as drift rather than
// an error. When missing, returns config.Default() with owner/repo populated so
// downstream rendering still produces a sensible result. A parse error is fatal.
func loadConfigForCheck(dir, owner, repo string) (cfg *config.Config, missing bool, err error) {
	configPath := filepath.Join(dir, config.ConfigFile)
	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
		c := config.Default()
		c.Platform.Owner = owner
		c.Platform.Repo = repo
		return c, true, nil
	}
	loaded, loadErr := config.Load(dir)
	if loadErr != nil {
		return nil, false, fmt.Errorf("loading config: %w", loadErr)
	}
	return loaded, false, nil
}

// runInitCheck performs all rendering work runInit does, writes nothing, and prints
// a per-file diff summary. Returns errCheckDrift if any file would change. Skips:
// label creation, git commit/push/PR, workflow API installation.
func runInitCheck() error {
	if latest, ok := checkLatestVersion(context.Background()); ok {
		fmt.Println(display.Warning(fmt.Sprintf(
			"A newer herd version is available: %s (you are running %s). See https://github.com/Herd-OS/herd/releases/latest",
			latest, version,
		)))
	}

	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	if err := checkPrerequisites(dir); err != nil {
		return err
	}

	_, cfgMissing, files, drifted, err := computeManagedDrift(dir)
	if err != nil {
		return err
	}
	driftedSet := make(map[string]string, len(drifted))
	for _, d := range drifted {
		driftedSet[d.Path] = d.Reason
	}

	var countDrifted, countUnchanged int

	if cfgMissing {
		countDrifted++
		fmt.Println(display.Error(config.ConfigFile + " (would change)"))
	} else {
		countUnchanged++
		fmt.Println(display.Success(config.ConfigFile))
	}

	for _, mf := range files {
		if _, drift := driftedSet[mf.Path]; drift {
			countDrifted++
			fmt.Println(display.Error(mf.Path + " (would change)"))
			existing, readErr := os.ReadFile(filepath.Join(dir, mf.Path))
			if readErr == nil {
				preview := firstDiffLines(existing, mf.Content, 5)
				if preview != "" {
					for _, line := range strings.Split(strings.TrimRight(preview, "\n"), "\n") {
						fmt.Println("    " + line)
					}
				}
			}
		} else {
			countUnchanged++
			fmt.Println(display.Success(mf.Path))
		}
	}

	if _, drift := driftedSet["Dockerfile.herd_runner"]; drift {
		countDrifted++
		fmt.Println(display.Error("Dockerfile.herd_runner (would change)"))
	} else {
		countUnchanged++
		fmt.Println(display.Success("Dockerfile.herd_runner (user-owned, content not checked)"))
	}

	fmt.Printf("%d files would be modified, %d unchanged\n", countDrifted, countUnchanged)

	if countDrifted > 0 {
		return errCheckDrift
	}
	return nil
}

// firstDiffLines returns up to max lines of a unified-diff-style preview comparing
// old to new. Matching lines are prefixed with " ", removed lines with "-", and
// added lines with "+". The walk is line-by-line on the corresponding index — it
// is intentionally naive (no LCS), enough to surface the first divergence.
func firstDiffLines(old, new []byte, max int) string {
	if max <= 0 {
		return ""
	}
	oldLines := strings.Split(string(old), "\n")
	newLines := strings.Split(string(new), "\n")

	n := len(oldLines)
	if len(newLines) > n {
		n = len(newLines)
	}

	var out []string
	for i := 0; i < n && len(out) < max; i++ {
		oldExists := i < len(oldLines)
		newExists := i < len(newLines)

		if oldExists && newExists && oldLines[i] == newLines[i] {
			out = append(out, " "+oldLines[i])
			continue
		}
		if oldExists {
			out = append(out, "-"+oldLines[i])
			if len(out) >= max {
				break
			}
		}
		if newExists {
			out = append(out, "+"+newLines[i])
		}
	}
	return strings.Join(out, "\n")
}
