package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"strings"
	"syscall"

	"github.com/aristath/songya"
	"github.com/aristath/songya/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type rootsFlag map[string]string

func (r rootsFlag) String() string { return "name=/absolute/source/path" }
func (r rootsFlag) Set(value string) error {
	name, path, ok := strings.Cut(value, "=")
	if !ok || name == "" || path == "" {
		return fmt.Errorf("use name=/source/path")
	}
	r[name] = path
	return nil
}
func main() {
	flags := flag.NewFlagSet("songya-mcp", flag.ExitOnError)
	flags.SetOutput(os.Stderr)
	stores := rootsFlag{}
	flags.Var(stores, "store", "configured name=/store/path; repeat for each accessible store")
	roots := rootsFlag{}
	flags.Var(roots, "import-root", "optional named import root; repeat for more roots")
	_ = flags.Parse(os.Args[1:])
	if len(stores) == 0 {
		flags.Usage()
		os.Exit(2)
	}
	opened := map[string]*songya.Store{}
	for name, directory := range stores {
		store, err := songya.Open(directory)
		if err != nil {
			log.Fatal(err)
		}
		opened[name] = store
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := mcpserver.New(ctx, opened, roots)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
