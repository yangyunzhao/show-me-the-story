package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed static
var staticFiles embed.FS

func startWebServer(cfg *Config, cfgPath string, state *Progress, progressPath string, logger *LogBroadcaster, port string) {
	h := NewHandlers(cfg, cfgPath, state, progressPath, logger)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/config", h.GetConfig)
	mux.HandleFunc("PUT /api/config", h.PutConfig)
	mux.HandleFunc("GET /api/progress", h.GetProgress)
	mux.HandleFunc("DELETE /api/progress", h.DeleteProgress)
	mux.HandleFunc("GET /api/status", h.GetStatus)

	mux.HandleFunc("POST /api/outline/generate", h.PostOutlineGenerate)
	mux.HandleFunc("POST /api/outline/confirm", h.PostOutlineConfirm)
	mux.HandleFunc("POST /api/outline/revise", h.PostOutlineRevise)

	mux.HandleFunc("POST /api/chapter/generate", h.PostChapterGenerate)
	mux.HandleFunc("POST /api/chapter/confirm", h.PostChapterConfirm)
	mux.HandleFunc("POST /api/chapter/revise", h.PostChapterRevise)
	mux.HandleFunc("DELETE /api/chapter", h.DeleteChapter)
	mux.HandleFunc("DELETE /api/outline", h.DeleteOutline)

	mux.HandleFunc("GET /api/foreshadows", h.GetForeshadows)
	mux.HandleFunc("POST /api/foreshadows/suggest", h.PostForeshadowsSuggest)
	mux.HandleFunc("POST /api/foreshadows/confirm", h.PostForeshadowsConfirm)
	mux.HandleFunc("POST /api/foreshadows", h.PostForeshadow)
	mux.HandleFunc("PUT /api/foreshadows/{id}", h.PutForeshadow)
	mux.HandleFunc("DELETE /api/foreshadows/{id}", h.DeleteForeshadow)

	mux.HandleFunc("GET /api/events", h.SSEHandler)

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("嵌入静态文件失败: %v", err)
	}

	fileServer := http.FileServer(http.FS(staticFS))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			data, err := staticFiles.ReadFile("static/index.html")
			if err != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			w.Write(data)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	handler := corsMiddleware(loggingMiddleware(mux))

	srv := &http.Server{
		Addr:         port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	fmt.Printf(" [系统] AI 小说生成器 Web UI 启动中...\n")
	fmt.Printf(" [系统] 访问地址: http://localhost%s\n", port)
	fmt.Printf(" [系统] 当前阶段: %s\n", state.Phase)
	if state.Title != "" {
		fmt.Printf(" [系统] 小说标题: 《%s》\n", state.Title)
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, " [错误] 服务器启动失败: %v\n", err)
		os.Exit(1)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" {
			log.Printf(" %s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}
