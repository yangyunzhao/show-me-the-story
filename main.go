package main

import (
	"fmt"
	"os"
)

const (
	progressFile = "progress.json"
	configFile   = "config.json"
	defaultPort  = ":8080"
)

func main() {
	cfg, err := LoadConfig(configFile)
	if err != nil {
		fmt.Printf(" [错误] 加载配置失败: %v\n", err)
		os.Exit(1)
	}

	if cfg.BaseURL == "" || cfg.Model == "" {
		fmt.Println(" [系统] 检测到空白配置，已自动生成 config.json")
		fmt.Println(" [系统] 请通过 Web UI 配置 API 地址和模型后再使用")
	}

	state, err := LoadProgress(progressFile)
	if err != nil {
		fmt.Printf(" [错误] 加载进度文件失败: %v\n", err)
		os.Exit(1)
	}

	if state == nil {
		fmt.Println(" [系统] 未检测到历史进度，开始全新的创作流程...")
		state = &Progress{
			Phase: "outline",
		}
	} else {
		fmt.Printf(" [系统] 检测到历史进度，当前阶段: %s\n", state.Phase)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	} else {
		port = ":" + port
	}

	logger := NewLogBroadcaster()
	defer logger.Close()

	startWebServer(cfg, configFile, state, progressFile, logger, port)
}
