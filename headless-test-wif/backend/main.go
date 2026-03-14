package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/example/headless-backend-wif/middleware"
)

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	mux := http.NewServeMux()

	protected := func(h http.HandlerFunc) http.Handler {
		return withCORS(middleware.ValidateToken(h))
	}

	mux.Handle("/api/users", protected(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"id": "test-user", "status": "created"})
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]interface{}{"users": []interface{}{}})
		default:
			http.NotFound(w, r)
		}
	}))

	port := os.Getenv("BACKEND_PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("backend listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
