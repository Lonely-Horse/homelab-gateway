package main

import (
	"flag"
	"homelab-gateway/internal/config"
	"log"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "cfgPath", "/etc/gateway/gateway.yaml", "配置文件地址")
	flag.Parse()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("The config address is bad")
		return
	}

	_ = cfg

}
