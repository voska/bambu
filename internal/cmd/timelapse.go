package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/ftps"
	"github.com/voska/bambu/internal/output"
)

const timelapseDir = "/timelapse/"

// TimelapseCmd groups read-only timelapse commands.
type TimelapseCmd struct {
	List TimelapseListCmd `cmd:"" help:"List timelapse videos, newest first. Exit 3 if none."`
	Pull TimelapsePullCmd `cmd:"" help:"Download one timelapse by name or --latest; never overwrites or deletes from the printer."`
}

// TimelapseListCmd lists the printer's timelapses.
type TimelapseListCmd struct{}

type timelapseFile struct {
	Name  string    `json:"name"`
	Bytes int64     `json:"bytes"`
	MTime time.Time `json:"mtime"`
}

func openTimelapse(g *Globals) (*ftps.Client, string, error) {
	p, err := g.Target()
	if err != nil {
		return nil, "", err
	}
	code, _, err := g.Code(p)
	if err != nil {
		return nil, "", err
	}
	f, err := ftps.Dial(g.Ctx, p.Host, g.FTPSPort, "bblp", code)
	return f, p.Name, err
}

func validTimelapseName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mp4" || ext == ".avi"
}

func timelapseMetadata(f *ftps.Client, name string) (timelapseFile, error) {
	file := timelapseFile{Name: name}
	var err error
	file.Bytes, err = f.Size(timelapseDir + name)
	if err != nil {
		return file, errfmt.Wrap(errfmt.ExitRetryable, err, "read timelapse size for %q", name)
	}
	if file.Bytes < 0 {
		return file, errfmt.New(errfmt.ExitRetryable, "invalid timelapse size for %q", name)
	}
	file.MTime, err = f.ModTime(timelapseDir + name)
	if err != nil {
		return file, errfmt.Wrap(errfmt.ExitRetryable, err, "read timelapse modification time for %q", name)
	}
	return file, nil
}

func listTimelapses(g *Globals, f *ftps.Client, wanted string) ([]timelapseFile, error) {
	names, err := f.List(g.Ctx, timelapseDir)
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitRetryable, err, "list printer timelapses").
			WithHint("check the SD card is available and has a /timelapse/ directory")
	}
	files := []timelapseFile{}
	for _, name := range names {
		if strings.HasPrefix(name, timelapseDir) {
			name = strings.TrimPrefix(name, timelapseDir)
		} else {
			name = strings.TrimPrefix(name, "timelapse/")
		}
		if !validTimelapseName(name) || (wanted != "" && wanted != name) {
			continue
		}
		file, err := timelapseMetadata(f, name)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].MTime.Equal(files[j].MTime) {
			return files[i].Name > files[j].Name
		}
		return files[i].MTime.After(files[j].MTime)
	})
	return files, nil
}

// Run executes the command.
func (c *TimelapseListCmd) Run(g *Globals) error {
	f, printerName, err := openTimelapse(g)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	files, err := listTimelapses(g, f, "")
	if err != nil {
		return err
	}
	rows := [][]string{{"name", "bytes", "mtime"}}
	names := make([]string, 0, len(files))
	for _, file := range files {
		rows = append(rows, []string{file.Name, strconv.FormatInt(file.Bytes, 10), file.MTime.Format(time.RFC3339Nano)})
		names = append(names, file.Name)
	}
	if err := g.Out.Print(output.View{
		Data: map[string]any{"printer": printerName, "timelapses": files},
		Human: func(h *output.Human) {
			if len(files) == 0 {
				h.Line("No timelapse videos on %s. Record the next print with: bambu print send ... --timelapse", printerName)
				return
			}
			h.Table(rows)
		},
		Plain: rows,
		Quiet: strings.Join(names, "\n"),
	}); err != nil {
		return err
	}
	if len(files) == 0 {
		return errfmt.Exit(errfmt.ExitEmpty)
	}
	return nil
}

// TimelapsePullCmd downloads one video without changing the printer.
type TimelapsePullCmd struct {
	Name   string `arg:"" optional:"" help:"Video basename from timelapse list (.mp4 or .avi); omit with --latest."`
	Latest bool   `help:"Pull the newest nonempty video (by mtime, then name)."`
	Output string `short:"o" help:"Output file, or an existing directory (default ./<name>). Never overwrites." type:"path"`
}

// Run executes the command.
func (c *TimelapsePullCmd) Run(g *Globals) error {
	if c.Latest == (c.Name != "") || (c.Name != "" && !validTimelapseName(c.Name)) || c.Output == "-" {
		return errfmt.New(errfmt.ExitUsage, "use one timelapse basename or --latest, and a local output path").
			WithHint("bambu timelapse list; bambu timelapse pull --latest -o ./video.mp4")
	}
	f, printerName, err := openTimelapse(g)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	files, err := listTimelapses(g, f, c.Name)
	if err != nil {
		return err
	}
	if c.Latest {
		for _, file := range files {
			if file.Bytes > 0 {
				c.Name = file.Name
				break
			}
		}
		if c.Name == "" {
			return errfmt.New(errfmt.ExitEmpty, "no nonempty timelapse videos on %s", printerName)
		}
	}
	for _, file := range files {
		if file.Name != c.Name {
			continue
		}
		if file.Bytes == 0 {
			return errfmt.New(errfmt.ExitError, "timelapse %q is empty", file.Name).
				WithHint("choose another video, or wait for recording to finish")
		}
		out := c.Output
		if out == "" {
			out = file.Name
		} else if info, err := os.Stat(out); err == nil && info.IsDir() {
			out = filepath.Join(out, file.Name)
		} else if strings.HasSuffix(out, "/") || strings.HasSuffix(out, string(os.PathSeparator)) {
			return errfmt.New(errfmt.ExitUsage, "output directory %q must already exist", out)
		}
		out, err = filepath.Abs(out)
		if err != nil {
			return errfmt.Wrap(errfmt.ExitError, err, "resolve output path")
		}
		if err := pullTimelapse(g, f, file, out); err != nil {
			return err
		}
		return g.Out.Print(output.View{
			Data:  map[string]any{"printer": printerName, "name": file.Name, "path": out, "bytes": file.Bytes, "mtime": file.MTime},
			Human: func(h *output.Human) { h.Line("%s (%d bytes)", out, file.Bytes) },
			Plain: [][]string{{"printer", "name", "path", "bytes", "mtime"}, {printerName, file.Name, out, strconv.FormatInt(file.Bytes, 10), file.MTime.Format(time.RFC3339Nano)}},
			Quiet: out,
		})
	}
	return errfmt.New(errfmt.ExitNotFound, "timelapse %q not found", c.Name).WithHint("run: bambu timelapse list")
}

func pullTimelapse(g *Globals, f *ftps.Client, file timelapseFile, out string) error {
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return errfmt.New(errfmt.ExitError, "cannot create %s: destination exists or is inaccessible", out).
			WithHint("choose an unused output filename with -o")
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".*.part")
	if err != nil {
		return errfmt.Wrap(errfmt.ExitError, err, "create download file")
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()
	g.Out.Hint("Downloading %s (%d bytes)", file.Name, file.Bytes)
	n, err := f.Retrieve(g.Ctx, timelapseDir+file.Name, tmp)
	if err != nil {
		code := errfmt.ExitRetryable
		var typed *errfmt.Error
		if errors.As(err, &typed) {
			code = typed.Code
		}
		return errfmt.Wrap(code, err, "download timelapse %q", file.Name)
	}
	after, err := timelapseMetadata(f, file.Name)
	if err != nil {
		return err
	}
	if n == 0 || n != file.Bytes || after.Bytes != file.Bytes || !after.MTime.Equal(file.MTime) {
		return errfmt.New(errfmt.ExitRetryable, "timelapse %q changed or download was incomplete (%d of %d bytes)", file.Name, n, file.Bytes).
			WithHint("the video may still be recording; retry after the print finishes")
	}
	if err := tmp.Sync(); err != nil {
		return errfmt.Wrap(errfmt.ExitError, err, "flush timelapse download")
	}
	if err := tmp.Close(); err != nil {
		return errfmt.Wrap(errfmt.ExitError, err, "close timelapse download")
	}
	if err := g.Ctx.Err(); err != nil {
		return errfmt.Wrap(errfmt.ExitRetryable, err, "timelapse download interrupted")
	}
	if err := os.Link(tmp.Name(), out); err != nil {
		return errfmt.Wrap(errfmt.ExitError, err, "publish timelapse download").
			WithHint("choose an unused filename on a filesystem that supports hard links")
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() != n {
		return errfmt.New(errfmt.ExitError, "download at %s could not be verified", out).WithData("path", out)
	}
	return nil
}
