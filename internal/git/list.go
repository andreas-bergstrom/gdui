package git

import (
	"os/exec"
)

// ListAll returns every tracked + untracked-non-ignored path in the repo,
// repo-relative. Used by the "show all files" view mode.
func ListAll(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "-c", "core.quotepath=false",
		"ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, err
	}
	parts := splitNUL(out)
	// Deduplicate (a path may appear twice if both cached and changed-on-disk).
	seen := make(map[string]struct{}, len(parts))
	out2 := parts[:0]
	for _, p := range parts {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out2 = append(out2, p)
	}
	return out2, nil
}
