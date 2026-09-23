// Package config loads and saves bambu's TOML config and selects the target printer.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/voska/bambu/internal/errfmt"
)

// Plates are the installed-plate values accepted in config (the bed_type strings Bambu uses in 3MF plate JSON).
var Plates = []string{"textured_plate", "cool_plate", "eng_plate", "hot_plate", "supertack_plate"}

// Printer is one configured printer.
type Printer struct {
	Name   string  `toml:"-" json:"name"`
	Host   string  `toml:"host" json:"host"`
	Serial string  `toml:"serial" json:"serial"`
	Model  string  `toml:"model" json:"model"`
	Nozzle float64 `toml:"nozzle" json:"nozzle"`
	Plate  string  `toml:"plate" json:"plate"`
}

// Slicer overrides slicer discovery.
type Slicer struct {
	Path      string `toml:"path,omitempty"`
	Resources string `toml:"resources,omitempty"`
}

// Config is the whole config file.
type Config struct {
	DefaultPrinter string              `toml:"default_printer,omitempty"`
	OutputDir      string              `toml:"output_dir,omitempty"`
	RecipesDir     string              `toml:"recipes_dir,omitempty"`
	Slicer         Slicer              `toml:"slicer,omitempty"`
	Printers       map[string]*Printer `toml:"printers,omitempty"`

	path string
}

// DefaultPath returns the config path: $BAMBU_CONFIG, $XDG_CONFIG_HOME/bambu/config.toml,
// %AppData%\bambu\config.toml on Windows, else ~/.config/bambu/config.toml.
func DefaultPath() string {
	if p := os.Getenv("BAMBU_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Dir(), "config.toml")
}

// Dir returns the config directory (used for user recipes).
func Dir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "bambu")
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "bambu")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "bambu")
}

// Load reads the config at path. A missing file yields an empty config.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	c := &Config{Printers: map[string]*Printer{}, path: path}
	b, err := os.ReadFile(path) //nolint:gosec // user-chosen config path
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitConfig, err, "read config %s", path)
	}
	if _, err := toml.Decode(string(b), c); err != nil {
		return nil, errfmt.Wrap(errfmt.ExitConfig, err, "parse config %s", path).WithHint("fix the TOML syntax")
	}
	if c.Printers == nil {
		c.Printers = map[string]*Printer{}
	}
	for name, p := range c.Printers {
		p.Name = name
		if p.Nozzle == 0 {
			p.Nozzle = 0.4
		}
		if p.Plate == "" {
			p.Plate = "textured_plate"
		}
	}
	c.path = path
	return c, nil
}

// Path returns where the config is (or would be) stored.
func (c *Config) Path() string { return c.path }

// Save writes the config atomically with 0600 permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return errfmt.Wrap(errfmt.ExitConfig, err, "create config dir")
	}
	var buf bytes.Buffer
	buf.WriteString("# bambu config — https://github.com/voska/bambu\n# Access codes are NOT stored here (OS keychain: `bambu auth set <printer>`).\n\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return errfmt.Wrap(errfmt.ExitConfig, err, "encode config")
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return errfmt.Wrap(errfmt.ExitConfig, err, "write config")
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return errfmt.Wrap(errfmt.ExitConfig, err, "write config")
	}
	return nil
}

// Names returns the configured printer names, sorted.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Printers))
	for n := range c.Printers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Select picks a printer: explicit name (flag or BAMBU_PRINTER) > default_printer > the only printer.
func (c *Config) Select(name string) (*Printer, error) {
	if name == "" {
		name = c.DefaultPrinter
	}
	if name == "" {
		switch len(c.Printers) {
		case 0:
			return nil, errfmt.New(errfmt.ExitConfig, "no printers configured").
				WithHint("add one: bambu printer add <name> --host <ip> --serial <serial> --model X1C (or: bambu printer discover)")
		case 1:
			for _, p := range c.Printers {
				return p, nil
			}
		default:
			return nil, errfmt.New(errfmt.ExitUsage, "several printers configured and none selected").
				WithHint("pass --printer <name>, set BAMBU_PRINTER, or run: bambu printer default <name> (have: %s)", strings.Join(c.Names(), ", "))
		}
	}
	p, ok := c.Printers[name]
	if !ok {
		return nil, errfmt.New(errfmt.ExitNotFound, "printer %q not configured", name).
			WithHint("configured: %s", strings.Join(c.Names(), ", "))
	}
	return p, nil
}

var (
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	serialRe = regexp.MustCompile(`^[A-Za-z0-9]{8,32}$`)
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9.:\-\[\]]+$`)
)

// Validate checks a printer entry.
func (p *Printer) Validate() error {
	switch {
	case !nameRe.MatchString(p.Name):
		return errfmt.New(errfmt.ExitUsage, "invalid printer name %q", p.Name).WithHint("use letters, digits, - or _ (max 32)")
	case !hostRe.MatchString(p.Host):
		return errfmt.New(errfmt.ExitUsage, "invalid host %q", p.Host).WithHint("use an IP address or hostname, no scheme or port")
	case !serialRe.MatchString(p.Serial):
		return errfmt.New(errfmt.ExitUsage, "invalid serial %q", p.Serial).WithHint("the serial is on the printer (Settings > Device) or from `bambu printer discover`")
	case p.Nozzle <= 0 || p.Nozzle > 2:
		return errfmt.New(errfmt.ExitUsage, "invalid nozzle %v", p.Nozzle).WithHint("e.g. 0.4")
	}
	for _, pl := range Plates {
		if p.Plate == pl {
			return nil
		}
	}
	return errfmt.New(errfmt.ExitUsage, "invalid plate %q", p.Plate).WithHint("one of: %s", strings.Join(Plates, ", "))
}

// NozzleString formats the nozzle like Bambu preset names ("0.4").
func (p *Printer) NozzleString() string { return fmt.Sprintf("%g", p.Nozzle) }
