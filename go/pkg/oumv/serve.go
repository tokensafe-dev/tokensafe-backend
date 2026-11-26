package oumv

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Serve announces the runtime in the §9.2 discovery registry using the listener's
// real bound address, serves handler on ln, and removes the entry when it returns.
//
// Go has no framework-wide listen hook (`gin.Run` creates its own listener), so the
// adapter ships this wrapper instead: replace
//
//	r.Run(":8080")
//
// with
//
//	oumv.ListenAndServe(rt, ":8080", r)
//
// and the descriptor carries the port the kernel actually assigned — including :0.
func Serve(rt *Runtime, ln net.Listener, handler http.Handler) error {
	if rt == nil || ln == nil || handler == nil {
		return errNilServeArgument
	}
	if _, err := rt.WriteDescriptor("http://" + publicAddress(ln.Addr()) + rt.rpcPath()); err != nil {
		return err
	}
	defer func() { _ = rt.DeleteDescriptor() }()
	return (&http.Server{Handler: handler}).Serve(ln)
}

// ListenAndServe binds addr, then behaves like Serve.
func ListenAndServe(rt *Runtime, addr string, handler http.Handler) error {
	if rt == nil || handler == nil {
		return errNilServeArgument
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return Serve(rt, ln, handler)
}

type serveArgumentError struct{}

func (serveArgumentError) Error() string {
	return "oumv: Serve needs a runtime, a listener and a handler"
}

var errNilServeArgument = serveArgumentError{}

// publicAddress reports a connectable host:port. A wildcard bind (":8080" or
// "0.0.0.0:8080") is announced as 127.0.0.1, which is where an agent on the same
// machine can actually reach the runtime.
func publicAddress(addr net.Addr) string {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		host := tcp.IP.String()
		if tcp.IP == nil || tcp.IP.IsUnspecified() {
			host = "127.0.0.1"
		}
		return net.JoinHostPort(host, strconv.Itoa(tcp.Port))
	}
	return addr.String()
}

func (r *Runtime) rpcPath() string {
	if path := strings.TrimSpace(r.config.Transport.Path); path != "" {
		return path
	}
	return "/__oumv/rpc"
}
