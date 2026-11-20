// ignisforum-plugin performs explicit operator-side package transactions.
// It never talks to the web server and never changes a running Manager.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/buraksocial/IgnisForum/internal/plugin"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("one command is required: install, rollback or remove")
	}
	command := args[0]
	flags := flag.NewFlagSet("ignisforum-plugin", flag.ContinueOnError)
	root := flags.String("root", "", "private configured plugin_dir")
	source := flags.String("source", "", "validated package directory for install")
	id := flags.String("id", "", "plugin ID for rollback or remove")
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("positional arguments are not supported")
	}
	if *root == "" {
		return fmt.Errorf("-root is required")
	}
	var result plugin.PackageResult
	var err error
	switch command {
	case "install":
		if *source == "" || *id != "" {
			return fmt.Errorf("install requires -source and does not accept -id")
		}
		result, err = plugin.InstallPackage(context.Background(), *root, *source)
	case "rollback":
		if *id == "" || *source != "" {
			return fmt.Errorf("rollback requires -id and does not accept -source")
		}
		result, err = plugin.RollbackPackage(*root, *id)
	case "remove":
		if *id == "" || *source != "" {
			return fmt.Errorf("remove requires -id and does not accept -source")
		}
		result, err = plugin.RemovePackage(*root, *id)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(result)
}
