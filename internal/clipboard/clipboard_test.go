package clipboard

import (
	"runtime"
	"testing"
)

func TestPlatformCmd_Darwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	bin, _ := platformCmd()
	if bin != "pbcopy" {
		t.Errorf("darwin should use pbcopy, got %q", bin)
	}
}

func TestCopy_RoundTripDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only")
	}
	// We can't easily verify the clipboard read without polluting the user's
	// clipboard during test runs. Just confirm Copy doesn't return an error
	// on a platform where pbcopy is available.
	if err := Copy("gdui-clipboard-test"); err != nil {
		t.Errorf("Copy returned error: %v", err)
	}
}
