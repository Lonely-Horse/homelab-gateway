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
		log.Printf("The config address is bad,detail: %s", err)
		return
	}

	for _, rt := range cfg.Routes {
		log.Printf("route=%s host=%s prefix=%s upstream=%s strip=%v", rt.Name, rt.Host, rt.PathPrefix, rt.Upstream, rt.StripPrefix)
	}

	log.Printf("listen=%s dial=%s read_header=%s", cfg.Listen, cfg.Timeouts.Dial, cfg.Server.ReadHeader)

}
