package main

import (
	"flag"
	"log"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/api"
)

func main() {
	addr := flag.String("addr", ":8080", "监听地址")
	flag.Parse()

	server := api.NewServer()
	if err := server.ListenAndServe(*addr); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
