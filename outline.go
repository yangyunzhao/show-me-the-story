package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type OutlineResponse struct {
	Title            string           `json:"title"`
	CorePrompt       string           `json:"core_prompt"`
	CoreRequirements string           `json:"core_requirements"`
	Chapters         []OutlineChapter `json:"chapters"`
}

type OutlineChapter struct {
	Num     int    `json:"num"`
	Title   string `json:"title"`
	Outline string `json:"outline"`
}

func generateOutline(cfg *Config) (*OutlineResponse, error) {
	chapterCountStr := fmt.Sprintf("%d", cfg.Story.ChapterCount)
	targetWordsStr := fmt.Sprintf("%d", cfg.Story.TargetWordsPerChapter)

	userPrompt := RenderPrompt(cfg.Prompts.OutlineGeneration, map[string]string{
		"StoryType":         cfg.Story.Type,
		"ChapterCount":      chapterCountStr,
		"TargetWords":       targetWordsStr,
		"WritingStyle":      cfg.Story.WritingStyle,
		"CharacterSetting":  cfg.Story.CharacterSetting,
		"WorldSetting":      cfg.Story.WorldSetting,
		"CoreRequirements":  cfg.Story.CoreRequirements,
	})

	systemPrompt := "你是一位专业的小说策划编辑。请严格按照要求的JSON格式输出，不要添加任何额外文字或markdown代码块标记。"

	rawResp := CallAPIWithRetry(cfg, systemPrompt, userPrompt)

	rawResp = cleanJSONResponse(rawResp)

	var resp OutlineResponse
	if err := json.Unmarshal([]byte(rawResp), &resp); err != nil {
		return nil, fmt.Errorf("解析大纲JSON失败: %w\n原始响应: %s", err, rawResp)
	}

	return &resp, nil
}

func reviseOutline(cfg *Config, state *Progress, userFeedback string) error {
	lockedChapters := ""
	for _, ch := range state.Chapters {
		if ch.Status == StatusAccepted {
			lockedChapters += fmt.Sprintf("第%d章《%s》: %s\n", ch.Num, ch.Title, ch.Outline)
		}
	}
	if lockedChapters == "" {
		lockedChapters = "无已锁定章节。"
	}

	currentOutline := ""
	for _, ch := range state.Chapters {
		currentOutline += fmt.Sprintf("第%d章《%s》: %s\n", ch.Num, ch.Title, ch.Outline)
	}

	userPrompt := RenderPrompt(cfg.Prompts.OutlineRevision, map[string]string{
		"CurrentOutline": currentOutline,
		"UserFeedback":   userFeedback,
		"LockedChapters": lockedChapters,
	})

	systemPrompt := "你是一位小说策划编辑。请严格按照要求的JSON格式输出，不要添加任何额外文字或markdown代码块标记。已锁定的章节内容不可修改。"

	rawResp := CallAPIWithRetry(cfg, systemPrompt, userPrompt)
	rawResp = cleanJSONResponse(rawResp)

	var resp OutlineResponse
	if err := json.Unmarshal([]byte(rawResp), &resp); err != nil {
		return fmt.Errorf("解析修订大纲JSON失败: %w\n原始响应: %s", err, rawResp)
	}

	lockedMap := make(map[int]bool)
	for _, ch := range state.Chapters {
		if ch.Status == StatusAccepted {
			lockedMap[ch.Num] = true
		}
	}

	for _, newCh := range resp.Chapters {
		for i, existingCh := range state.Chapters {
			if existingCh.Num == newCh.Num && !lockedMap[newCh.Num] {
				state.Chapters[i].Title = newCh.Title
				state.Chapters[i].Outline = newCh.Outline
			}
		}
	}

	if resp.Title != "" {
		state.Title = resp.Title
	}
	if resp.CorePrompt != "" {
		state.CorePrompt = resp.CorePrompt
	}
	if resp.CoreRequirements != "" {
		state.CoreRequirements = resp.CoreRequirements
	}

	return nil
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func cleanJSONResponse(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

func GenerateOutlineAction(cfg *Config, state *Progress, progressPath string, logger *LogBroadcaster) error {
	logger.Info("正在调用 AI 生成大纲...")

	outlineResp, err := generateOutline(cfg)
	if err != nil {
		return fmt.Errorf("生成大纲失败: %w", err)
	}

	state.Title = outlineResp.Title
	state.CorePrompt = outlineResp.CorePrompt
	state.CoreRequirements = outlineResp.CoreRequirements
	state.Chapters = make([]ChapterState, len(outlineResp.Chapters))
	for i, ch := range outlineResp.Chapters {
		state.Chapters[i] = ChapterState{
			Num:     ch.Num,
			Title:   ch.Title,
			Outline: ch.Outline,
			Status:  StatusPending,
		}
	}
	snapshot := cfg.Story
	state.StoryConfigSnapshot = &snapshot

	if err := SaveProgress(progressPath, state); err != nil {
		return fmt.Errorf("保存进度失败: %w", err)
	}

	logger.Info(fmt.Sprintf("大纲生成完成，共 %d 章，标题: 《%s》", len(state.Chapters), state.Title))
	return nil
}

func ReviseOutlineAction(cfg *Config, state *Progress, progressPath, feedback string, logger *LogBroadcaster) error {
	logger.Info("正在根据意见修订大纲...")

	if err := reviseOutline(cfg, state, feedback); err != nil {
		return fmt.Errorf("修订大纲失败: %w", err)
	}

	if err := SaveProgress(progressPath, state); err != nil {
		return fmt.Errorf("保存进度失败: %w", err)
	}

	logger.Info(fmt.Sprintf("大纲已修订，共 %d 章", len(state.Chapters)))
	return nil
}

func ConfirmOutlineAction(state *Progress, progressPath string) error {
	if len(state.Chapters) == 0 {
		return fmt.Errorf("大纲为空")
	}

	state.Phase = "writing"
	return SaveProgress(progressPath, state)
}
