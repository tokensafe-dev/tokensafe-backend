package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
)

// StdioServer runs the OUMV JSON-RPC protocol over standard I/O (stdio).
// This enables direct plug-and-play with AI Coding Agents (Claude Code, Cursor, Antigravity)
// as an MCP server or subprocess worker.
type StdioServer struct {
	dispatcher *Dispatcher
	in         io.Reader
	out        io.Writer
}

// NewStdioServer creates a stdio transport server.
func NewStdioServer(dispatcher *Dispatcher, in io.Reader, out io.Writer) *StdioServer {
	return &StdioServer{
		dispatcher: dispatcher,
		in:         in,
		out:        out,
	}
}

// Serve continuously reads newline-delimited JSON-RPC requests until EOF or context cancellation.
func (s *StdioServer) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	// Buffer up to 10MB messages for large source code exchanges
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	encoder := json.NewEncoder(s.out)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return nil // EOF
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		resp := s.dispatcher.Handle(ctx, line)
		if err := encoder.Encode(resp); err != nil {
			return err
		}
	}
}
