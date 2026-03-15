package main

import (
	"flag"
	"log"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
)

func main() {
	addr := flag.String("addr", ":8080", "")
	flag.Parse()

	server := api.NewServer()
	if err := server.ListenAndServe(*addr); err != nil {
		log.Fatalf(": %v", err)
	}
}
