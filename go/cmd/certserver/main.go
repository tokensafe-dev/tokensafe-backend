// Command certserver is a live net/http application used only by
// go/certify-transports.sh: it mounts the stdhttp adapter (HTTP + SSE + WS) and a
// database/sql sandbox, so oumv-certify can produce real transport certificates.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/adapters/stdhttp"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/db"
	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/oumv"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	workspace := flag.String("workspace", "", "workspace directory the fixtures edit")
	// §12: the harness starts the certserver with a token so the auth refusal
	// cases are certified against the real binding.
	token := flag.String("token", "", "session token the adapter must require")
	flag.Parse()

	if *workspace == "" {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatalf("workspace: %v", err)
		}
		*workspace = wd
	}

	cfg := oumv.DefaultDevelopmentRuntimeConfig()
	if *token != "" {
		cfg.Security.AuthToken = *token
		cfg.Security.AuthenticationRequired = true
	}
	// §14.2 workspace policy: the policy certificate runs the same server with the
	// query permission switched off — oumv/query stays registered and answers -32007.
	if os.Getenv("TOKENSAFE_CERT_QUERY_DISABLED") == "1" {
		cfg.Permissions = &oumv.PermissionsConfig{QueryDisabled: true}
	}
	rt := oumv.NewRuntime(oumv.Config{
		Name:         "tokensafe-stdhttp-cert",
		Version:      "cert",
		WorkspaceDir: *workspace,
		Runtime:      cfg,
	})
	rt.SetDBSandbox(db.NewMemorySandbox())

	mux := stdhttp.NewServeMux(rt)
	mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"orders":[]}`))
	})
	mux.HandleFunc("POST /__oumv_sandbox_probe", func(w http.ResponseWriter, req *http.Request) {
		tx := db.GetTxFromContext(req.Context())
		if tx == nil || req.Header.Get("X-TokenSafe-Dry-Run") != "true" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec("UPDATE cert_probe SET v = 1"); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Mount("/__oumv/rpc")

	log.Printf("certserver listening on %s (workspace %s)", *addr, *workspace)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
