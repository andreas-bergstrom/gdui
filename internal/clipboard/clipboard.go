// Package clipboard wraps the platform-native copy command so the UI can
// place a string into the system clipboard. macOS uses pbcopy, Linux probes
// for wl-copy / xclip / xsel, Windows uses clip. The function is best-effort:
// callers should surface errors as a user-visible toast rather than treating
// them as fatal.
package clipboard

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// ErrUnsupported is returned when no platform copy mechanism could be located.
var ErrUnsupported = errors.New("clipboard: no supported copy command found")

// Copy writes s to the system clipboard.
func Copy(s string) error {
	bin, args := platformCmd()
	if bin == "" {
		return ErrUnsupported
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

func platformCmd() (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "pbcopy", nil
	case "windows":
		return "clip", nil
	case "linux":
		// Wayland first (commonly preferred when present), then X11.
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return "wl-copy", nil
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			return "xclip", []string{"-selection", "clipboard"}
		}
		if _, err := exec.LookPath("xsel"); err == nil {
			return "xsel", []string{"--clipboard", "--input"}
		}
		return "", nil
	default:
		return "", nil
	}
}
