package core

import "net/http"

func WithExitRecovery(handler http.Handler) Option {
	return func(server *Server) { server.exitRecovery = handler }
}
