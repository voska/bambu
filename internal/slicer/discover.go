// Package slicer drives the Bambu Studio CLI headlessly with flattened system presets.
package slicer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/voska/bambu/internal/errfmt"
)

// Studio is a discovered Bambu Studio installation.
type Studio struct {
	Binary    string `json:"binary"`
	Resources string `json:"resources"`
	Version   string `json:"version,omitempty"`
}

// Candidates lists the binary locations tried, in order (after BAMBU_STUDIO_PATH / config).
func Candidates() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/BambuStudio.app/Contents/MacOS/BambuStudio",
			filepath.Join(home, "Applications/BambuStudio.app/Contents/MacOS/BambuStudio"),
		}
	case "windows":
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return []string{filepath.Join(pf, "Bambu Studio", "bambu-studio.exe")}
	default:
		return []string{
			"/usr/bin/bambu-studio",
			"/usr/local/bin/bambu-studio",
			"/opt/bambu-studio/bin/bambu-studio",
			"/var/lib/flatpak/app/com.bambulab.BambuStudio/current/active/files/bin/bambu-studio",
			filepath.Join(home, ".local/share/flatpak/app/com.bambulab.BambuStudio/current/active/files/bin/bambu-studio"),
			filepath.Join(home, "squashfs-root/AppRun"),
			filepath.Join(home, "squashfs-root/bin/bambu-studio"),
		}
	}
}

// Discover finds Bambu Studio. Precedence: BAMBU_STUDIO_PATH > configured path > well-known locations.
// resources may be given explicitly; otherwise it is found next to the binary.
func Discover(path, resources string) (*Studio, error) {
	tried := []string{}
	var bins []string
	if p := os.Getenv("BAMBU_STUDIO_PATH"); p != "" {
		bins = append(bins, p)
	} else if path != "" {
		bins = append(bins, path)
	} else {
		bins = Candidates()
	}
	for _, b := range bins {
		tried = append(tried, b)
		st, err := os.Stat(b) //nolint:gosec // candidate paths come from env/config/well-known locations by design
		if err != nil || st.IsDir() {
			continue
		}
		res := resources
		if res == "" {
			res = findResources(b)
		}
		if res == "" {
			return nil, errfmt.New(errfmt.ExitConfig, "found Bambu Studio at %s but not its resources (profiles/BBL.json)", b).
				WithHint("set [slicer] resources in the config; for an AppImage run it with --appimage-extract first")
		}
		return &Studio{Binary: b, Resources: res}, nil
	}
	return nil, errfmt.New(errfmt.ExitConfig, "Bambu Studio not found").
		WithHint("install it (https://bambulab.com/en/download/studio) or set BAMBU_STUDIO_PATH; tried: %v", tried)
}

func findResources(bin string) string {
	real, err := filepath.EvalSymlinks(bin)
	if err == nil {
		bin = real
	}
	d := filepath.Dir(bin)
	for _, c := range []string{"../Resources", "../resources", "../share/BambuStudio", "resources", "../../resources"} {
		p := filepath.Clean(filepath.Join(d, c))
		if _, err := os.Stat(filepath.Join(p, "profiles", "BBL.json")); err == nil {
			return p
		}
	}
	return ""
}

var versionRe = regexp.MustCompile(`BambuStudio-([0-9.]+)`)

// DetectVersion runs `--help` and parses "BambuStudio-02.08.02.61".
func (s *Studio) DetectVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// `--help` writes result.json into the working directory, so run it in a throwaway dir.
	dir, err := os.MkdirTemp("", "bambu-help-")
	if err != nil {
		return ""
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cmd := exec.CommandContext(ctx, s.Binary, "--help") //nolint:gosec // discovered slicer binary
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	if m := versionRe.FindSubmatch(out); m != nil {
		s.Version = string(m[1])
	}
	return s.Version
}
