// Command local is the hermetic example server for examples/local (Q35): it
// backs the collection's login → me chain, and the /graphql endpoint, with no
// dependency on any third-party service.
package main

import (
	"encoding/json"
	"log"
	"net/http"
)

// loginToken is the bearer credential /login hands out and /me demands. The
// example's capture chain exists to move exactly this value between requests.
const loginToken = "tok-ada-42"

func main() {
	http.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": loginToken})
	})

	http.HandleFunc("/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+loginToken {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "Ada", "login": "ada"})
	})

	http.HandleFunc("/graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"hero":{"name":"R2-D2"}}}`))
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
}
