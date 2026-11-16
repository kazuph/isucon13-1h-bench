package main

import (
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/hash", func(w http.ResponseWriter, r *http.Request) {
		var q struct{ Password string }
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(q.Password), bcrypt.MinCost)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Write(hash)
	})
	http.HandleFunc("/compare", func(w http.ResponseWriter, r *http.Request) {
		var q struct{ Password, Hash string }
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		err := bcrypt.CompareHashAndPassword([]byte(q.Hash), []byte(q.Password))
		if err == bcrypt.ErrMismatchedHashAndPassword {
			w.WriteHeader(401)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	log.Fatal(http.ListenAndServe("10.99.0.13:9090", nil))
}
