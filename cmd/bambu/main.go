// Command bambu slices, checks, sends and monitors prints on Bambu Lab printers over LAN.
package main

import (
	"os"

	"github.com/voska/bambu/internal/cmd"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	os.Exit(cmd.Execute(os.Args[1:], cmd.BuildInfo{Version: version, Commit: commit, Date: date}, nil))
}
