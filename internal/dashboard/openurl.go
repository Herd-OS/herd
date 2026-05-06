package dashboard

import (
	"os/exec"
	"runtime"
)

// OpenURL launches the user's default browser. Returns nil if the spawn
// succeeds; the dashboard treats failures as non-fatal.
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
