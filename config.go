package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	APIKey            string          `json:"api_key"`
	BaseURL           string          `json:"base_url"`
	Model             string          `json:"model"`
	HTTPTimeoutSeconds int           `json:"http_timeout_seconds"`
	Story             StoryConfig     `json:"story"`
	Prompts           PromptsConfig   `json:"prompts"`
}

type StoryConfig struct {
	Type                  string `json:"type"`
	Title                 string `json:"title"`
	ChapterCount          int    `json:"chapter_count"`
	TargetWordsPerChapter int    `json:"target_words_per_chapter"`
	WritingStyle          string `json:"writing_style"`
	CharacterSetting      string `json:"character_setting"`
	WorldSetting          string `json:"world_setting"`
	CoreRequirements      string `json:"core_requirements"`
}

type PromptsConfig struct {
	OutlineGeneration  string `json:"outline_generation"`
	ChapterWriting     string `json:"chapter_writing"`
	ChapterSummary     string `json:"chapter_summary"`
	FactCheck          string `json:"fact_check"`
	OutlineRevision    string `json:"outline_revision"`
	ForeshadowPlanning string `json:"foreshadow_planning"`
	ForeshadowUpdate   string `json:"foreshadow_update"`
}

func DefaultConfig() *Config {
	cfg := &Config{
		HTTPTimeoutSeconds: 300,
		Story: StoryConfig{
			ChapterCount:          30,
			TargetWordsPerChapter: 2500,
		},
	}
	cfg.Prompts.applyDefaults()
	return cfg
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if saveErr := saveConfig(path, cfg); saveErr != nil {
				return nil, fmt.Errorf("创建默认配置文件失败: %w", saveErr)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	if cfg.Story.ChapterCount <= 0 {
		cfg.Story.ChapterCount = 30
	}
	if cfg.Story.TargetWordsPerChapter <= 0 {
		cfg.Story.TargetWordsPerChapter = 2500
	}
	if cfg.HTTPTimeoutSeconds <= 0 {
		cfg.HTTPTimeoutSeconds = 300
	}

	cfg.Prompts.applyDefaults()

	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func (p *PromptsConfig) applyDefaults() {
	if p.OutlineGeneration == "" {
		p.OutlineGeneration = DefaultPrompts.OutlineGeneration
	}
	if p.ChapterWriting == "" {
		p.ChapterWriting = DefaultPrompts.ChapterWriting
	}
	if p.ChapterSummary == "" {
		p.ChapterSummary = DefaultPrompts.ChapterSummary
	}
	if p.FactCheck == "" {
		p.FactCheck = DefaultPrompts.FactCheck
	}
	if p.OutlineRevision == "" {
		p.OutlineRevision = DefaultPrompts.OutlineRevision
	}
	if p.ForeshadowPlanning == "" {
		p.ForeshadowPlanning = DefaultPrompts.ForeshadowPlanning
	}
	if p.ForeshadowUpdate == "" {
		p.ForeshadowUpdate = DefaultPrompts.ForeshadowUpdate
	}
}
