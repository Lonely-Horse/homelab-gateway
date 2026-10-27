package main

import (
	"flag"
	"homelab-gateway/internal/config"
	"homelab-gateway/internal/server"
	"log"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "cfgPath", "/etc/gateway/gateway.yaml", "配置文件路径（yaml）")
	flag.Parse()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	server.Run(cfg)
}
