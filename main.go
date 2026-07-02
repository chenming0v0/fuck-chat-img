package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	defer func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			sqlDB.Close()
		}
	}()

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       400 * time.Second,
		IdleTimeout:       60 * time.Second,
		WriteTimeout:      3600 * time.Second,
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Fatalf("[fci] 监听 %s 失败: %v", cfg.ListenAddr, err)
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("[fci] 服务已启动，监听 %s", cfg.ListenAddr)
		log.Printf("[fci] 代理端点: /v1/responses /v1/chat/completions")
		serverErr <- srv.Serve(ln)
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[fci] 服务错误: %v", err)
			log.Fatalf("[fci] 服务异常退出")
		}
		return
	case <-ctx.Done():
		log.Printf("[fci] 收到退出信号, 正在优雅关闭...")
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[fci] 优雅关闭超时或出错: %v", err)
	}
	log.Printf("[fci] 已退出")
}
