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
		// Avoid `cmd /c start` because cmd.exe re-interprets characters like
		// `&`, `^`, and `%` in the URL argument. rundll32 hands the URL
		// directly to the protocol handler without a shell pass.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
