package main

import (
	"net/http"
)

// AuthMiddleware checks for headers (logging only for now)
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			logger.Warn("Authorization header is missing")
		}
		next.ServeHTTP(w, r)
	})
}
