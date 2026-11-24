package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

func main() {
	workspaceDir := flag.String("workspace", ".", "Workspace directory path")
	adapterName := flag.String("name", "tokensafe-backend", "Runtime adapter name")
	flag.Parse()

	rt := oumv.NewRuntime(oumv.Config{
		Name:         *adapterName,
		Version:      "0.2.0",
		WorkspaceDir: *workspaceDir,
	})

	dispatcher := transport.NewDispatcher(rt)
	server := transport.NewStdioServer(dispatcher, os.Stdin, os.Stdout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Serve(ctx); err != nil && err != context.Canceled {
		log.Fatalf("OUMV MCP stdio server terminated with error: %v", err)
	}
}
