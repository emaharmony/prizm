// Command openshorts-prizm installs and serves the OpenShorts MCP plugin.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/emaharmony/prizm/plugins/openshorts/internal/creatorvod"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: openshorts-prizm import --config prizm.yaml | serve --state-dir path")
	}
	switch args[0] {
	case "import":
		flags := flag.NewFlagSet("import", flag.ContinueOnError)
		config := flags.String("config", "", "existing Prizm configuration")
		stateDir := flags.String("state-dir", "", "plugin state directory (defaults next to configuration)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *config == "" {
			return errors.New("import requires --config pointing to an existing Prizm configuration")
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		configPath, err := filepath.Abs(*config)
		if err != nil {
			return err
		}
		configPath, err = filepath.EvalSymlinks(configPath)
		if err != nil {
			return err
		}
		if *stateDir == "" {
			*stateDir = filepath.Join(filepath.Dir(configPath), ".prizm", "plugins", serverName)
		}
		dir, err := filepath.Abs(*stateDir)
		if err != nil {
			return err
		}
		changed, err := importPlugin(configPath, executable, dir)
		if err != nil {
			return err
		}
		if changed {
			_, err = fmt.Fprintln(output, "OpenShorts plugin imported. Prizm loads its tools at startup.")
		} else {
			_, err = fmt.Fprintln(output, "OpenShorts plugin already imported.")
		}
		return err
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		stateDir := flags.String("state-dir", "", "isolated plugin state directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || !filepath.IsAbs(*stateDir) {
			return errors.New("serve requires an absolute --state-dir")
		}
		demo, err := creatorvod.OpenDemo(*stateDir)
		if err != nil {
			return err
		}
		defer demo.Close()
		return serveMCP(ctx, demo, input, output)
	default:
		return fmt.Errorf("unknown OpenShorts plugin command %q", args[0])
	}
}
