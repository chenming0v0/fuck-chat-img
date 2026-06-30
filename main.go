package main

import (
	"context"
	"errors"
	"log"
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

	// 优雅关闭: 捕获 SIGINT/SIGTERM, 给正在进行的流式 SSE 请求留足时间收尾,
	// 避免被 SIGKILL 直接切断导致上游写入未完成 / 客户端收到截断响应.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		IdleTimeout:  60 * time.Second,
		WriteTimeout: 3600 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("[fuck-chat-img] 服务已启动，监听 %s", cfg.ListenAddr)
		log.Printf("[fuck-chat-img] 代理端点: /v1/responses /v1/chat/completions")
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		// Server 自行退出(罕见). http.ErrServerClosed 是 Shutdown 引起的正常退出, 其余视为致命.
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[fci] 启动失败: %v", err)
		}
		return
	case <-ctx.Done():
		log.Printf("[fci] 收到退出信号, 正在优雅关闭...")
		// stop() 释放信号订阅; 此后再收到 SIGINT/SIGTERM 将采用默认行为(强制退出).
		stop()
	}

	// 给流式请求最多 30s 收尾; 超时后强制断开剩余连接.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[fci] 优雅关闭超时或出错: %v", err)
	}

	if sqlDB, err := model.DB.DB(); err != nil {
		log.Printf("[fci] 获取底层数据库连接失败: %v", err)
	} else {
		if err := sqlDB.Close(); err != nil {
			log.Printf("[fci] 关闭数据库连接失败: %v", err)
		}
	}
	log.Printf("[fci] 已退出")
}
