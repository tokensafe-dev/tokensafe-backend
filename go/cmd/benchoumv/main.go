// Command benchoumv exposes the AUB sandbox through OUMV over stdin/stdout so a
// benchmark driver can call observe/getSource/verify the way an agent would.
//
// One JSON-RPC request per line in, one JSON-RPC response per line out. It is a
// benchmark harness, not a server: the sandbox app and the runtime live in the
// same process, so `oumv/verify` dispatches in-memory.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	stdruntime "runtime"

	"github.com/tokensafe-dev/tokensafe-backend/go/benchmark/sandboxes/vanilla_gin"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/transport"
)

func main() {
	app := vanilla_gin.NewVanillaApp(nil)

	cfg := oumv.DefaultDevelopmentRuntimeConfig()
	rt := oumv.NewRuntime(oumv.Config{Name: "aub-sandbox", Version: "bench", Runtime: cfg})
	for _, route := range app.Engine.Routes() {
		pc := reflect.ValueOf(route.HandlerFunc).Pointer()
		if fn := stdruntime.FuncForPC(pc); fn != nil {
			file, line := fn.FileLine(pc)
			rt.RegisterRouteSource(route.Method, route.Path, route.Handler, file, line)
			continue
		}
		rt.RegisterRoute(route.Method, route.Path, route.Handler)
	}
	rt.SetHandler(http.HandlerFunc(app.ServeHTTP))

	dispatcher := transport.NewDispatcher(rt)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 1<<20)
	out := json.NewEncoder(os.Stdout)

	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req protocol.OumvRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = out.Encode(protocol.NewErrorResponse(nil, &protocol.OumvError{
				Code: protocol.ErrParseError, Message: err.Error(),
			}))
			continue
		}
		resp := dispatcher.Dispatch(context.Background(), &req)
		_ = out.Encode(resp)
	}
	if err := in.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "benchoumv:", err)
		os.Exit(1)
	}
}
