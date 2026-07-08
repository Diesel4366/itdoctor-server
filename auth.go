package main

import (
	"net/http"
)

// pulseAuthMiddleware проверяет X-Pulse-Key заголовок
func pulseAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Pulse-Key")
		if key != PulseKey {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
