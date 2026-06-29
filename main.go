package main

import (
	"log"

	"github.com/fuck-chat-img/fci/internal/api"
	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
)

func main() {
	config.Load()

	if err := model.Init(); err != nil {
		log.Fatalf("[fci] 初始化数据库失败: %v", err)
	}
	cache.Init()

	r := api.SetupRouter()
	cfg := config.Get()
	log.Printf("[fuck-chat-img] 监听 %s", cfg.ListenAddr)
	log.Printf("[fuck-chat-img] Web UI: http://localhost%s  代理端点: /v1/responses /v1/chat/completions", cfg.ListenAddr)
	if err := r.Run(cfg.ListenAddr); err != nil {
		log.Fatalf("[fci] 启动失败: %v", err)
	}
}
