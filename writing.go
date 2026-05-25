package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func GenerateChapterAction(cfg *Config, state *Progress, progressPath string, logger *LogBroadcaster) error {
	if state.Phase != "writing" {
		return fmt.Errorf("当前不在写作阶段")
	}

	if state.CurrentChapterIndex >= len(state.Chapters) {
		return fmt.Errorf("所有章节已完成")
	}

	i := state.CurrentChapterIndex
	ch := &state.Chapters[i]

	if ch.Status == StatusAccepted {
		return fmt.Errorf("第 %d 章已确认，请确认当前章节或重置进度", ch.Num)
	}

	ch.Status = StatusWriting
	if err := SaveProgress(progressPath, state); err != nil {
		return err
	}

	logger.Info(fmt.Sprintf("正在创作第 %d 章: 《%s》", ch.Num, ch.Title))

	maxFactCheckRetries := 3
	for attempt := 0; attempt <= maxFactCheckRetries; attempt++ {
		logger.Info("正在构思并撰写正文...")
		content := generateChapterContentStreamWithRetry(cfg, state, i, logger)
		ch.Content = content

		logger.Info("正文撰写完毕，正在提炼本章摘要...")
		summary := generateChapterSummaryWithRetry(cfg, content)
		ch.Summary = summary

		logger.Info("正在对本章进行事实核查...")
		historySummary := buildHistorySummary(state, i)
		factCheckResult := generateChapterFactCheckWithRetry(cfg, content, historySummary)

		if strings.Contains(factCheckResult, "FAIL") {
			if attempt < maxFactCheckRetries {
				logger.Warn(fmt.Sprintf("[事实核查] 发现问题，正在重新生成第 %d 章（第 %d 次重试）...", ch.Num, attempt+1))
				logger.Warn(fmt.Sprintf("核查详情: %s", factCheckResult))
				continue
			}
			logger.Warn("[事实核查] 已达最大重试次数，保留当前版本。")
		} else {
			logger.Info("[事实核查] 通过 ✓")
		}
		break
	}

	if len(state.Foreshadows) > 0 {
		logger.Info("正在更新伏笔状态...")
		if err := UpdateForeshadows(cfg, state, i); err != nil {
			logger.Warn(fmt.Sprintf("伏笔状态更新失败: %v（不影响本章）", err))
		} else {
			active := 0
			resolved := 0
			for _, fs := range state.Foreshadows {
				switch fs.Status {
				case ForeshadowPlanted, ForeshadowProgressing:
					active++
				case ForeshadowResolved:
					resolved++
				}
			}
			logger.Info(fmt.Sprintf("伏笔状态已更新（活跃: %d, 已回收: %d）", active, resolved))
		}
	}

	SaveChapterMarkdown(*ch, state.Title)

	ch.Status = StatusReview
	state.CurrentChapterIndex = i
	if err := SaveProgress(progressPath, state); err != nil {
		return err
	}

	if warn := BuildForeshadowWarnings(state); warn != "" {
		logger.Warn(warn)
	}

	logger.Success(fmt.Sprintf("第 %d 章创作完成！", ch.Num))
	return nil
}

func ReviseChapterAction(cfg *Config, state *Progress, progressPath, feedback string, logger *LogBroadcaster) error {
	if state.Phase != "writing" {
		return fmt.Errorf("当前不在写作阶段")
	}

	chapterIdx := state.CurrentChapterIndex
	if chapterIdx >= len(state.Chapters) {
		return fmt.Errorf("章节索引越界")
	}

	ch := &state.Chapters[chapterIdx]
	if ch.Status != StatusReview && ch.Status != StatusWriting {
		return fmt.Errorf("当前章节不在审核/写作状态")
	}

	logger.Info(fmt.Sprintf("正在修改第 %d 章《%s》...", ch.Num, ch.Title))

	revisedContent, err := reviseChapterContentStream(cfg, state, chapterIdx, feedback, logger)
	if err != nil {
		return fmt.Errorf("修改章节失败: %w", err)
	}
	ch.Content = revisedContent

	logger.Info("重新提炼摘要...")
	ch.Summary = generateChapterSummaryWithRetry(cfg, ch.Content)

	SaveChapterMarkdown(*ch, state.Title)

	if chapterIdx+1 < len(state.Chapters) {
		logger.Info("正在修订后续章节大纲...")
		if err := reviseSubsequentOutlines(cfg, state, chapterIdx, feedback); err != nil {
			logger.Warn(fmt.Sprintf("后续大纲修订失败: %v（不影响当前章节）", err))
		}
	}

	ch.Status = StatusReview
	if err := SaveProgress(progressPath, state); err != nil {
		return err
	}

	logger.Success(fmt.Sprintf("第 %d 章已修订。", ch.Num))
	return nil
}

func ConfirmChapterAction(state *Progress, progressPath string) error {
	if state.Phase != "writing" {
		return fmt.Errorf("当前不在写作阶段")
	}

	chapterIdx := state.CurrentChapterIndex
	if chapterIdx >= len(state.Chapters) {
		return fmt.Errorf("章节索引越界")
	}

	ch := &state.Chapters[chapterIdx]
	if ch.Status != StatusReview {
		return fmt.Errorf("当前章节不在审核状态，无法确认")
	}

	ch.Status = StatusAccepted
	state.CurrentChapterIndex = chapterIdx + 1
	return SaveProgress(progressPath, state)
}

func generateChapterContent(cfg *Config, state *Progress, idx int) (string, error) {
	ch := state.Chapters[idx]

	historySummary := buildHistorySummary(state, idx)

	snapshot := state.StoryConfigSnapshot
	if snapshot == nil {
		snapshot = &cfg.Story
	}

	foreshadowContext := formatActiveForeshadowsForChapter(state.Foreshadows, ch.Num)

	userPrompt := RenderPrompt(cfg.Prompts.ChapterWriting, map[string]string{
		"Title":            state.Title,
		"ChapterNum":       fmt.Sprintf("%d", ch.Num),
		"CorePrompt":       state.CorePrompt,
		"CoreRequirements": state.CoreRequirements,
		"HistorySummary":   historySummary,
		"ChapterTitle":     ch.Title,
		"ChapterOutline":   ch.Outline,
		"WritingStyle":     snapshot.WritingStyle,
		"CharacterSetting": snapshot.CharacterSetting,
		"WorldSetting":     snapshot.WorldSetting,
		"TargetWords":      fmt.Sprintf("%d", snapshot.TargetWordsPerChapter),
		"Foreshadows":      foreshadowContext,
	})

	systemPrompt := state.CorePrompt
	if systemPrompt == "" {
		systemPrompt = "你是一位小说作者。"
	}

	return CallAPI(cfg, systemPrompt, userPrompt)
}

func generateChapterContentStream(cfg *Config, state *Progress, idx int, logger *LogBroadcaster) (string, error) {
	ch := state.Chapters[idx]

	historySummary := buildHistorySummary(state, idx)

	snapshot := state.StoryConfigSnapshot
	if snapshot == nil {
		snapshot = &cfg.Story
	}

	foreshadowContext := formatActiveForeshadowsForChapter(state.Foreshadows, ch.Num)

	userPrompt := RenderPrompt(cfg.Prompts.ChapterWriting, map[string]string{
		"Title":            state.Title,
		"ChapterNum":       fmt.Sprintf("%d", ch.Num),
		"CorePrompt":       state.CorePrompt,
		"CoreRequirements": state.CoreRequirements,
		"HistorySummary":   historySummary,
		"ChapterTitle":     ch.Title,
		"ChapterOutline":   ch.Outline,
		"WritingStyle":     snapshot.WritingStyle,
		"CharacterSetting": snapshot.CharacterSetting,
		"WorldSetting":     snapshot.WorldSetting,
		"TargetWords":      fmt.Sprintf("%d", snapshot.TargetWordsPerChapter),
		"Foreshadows":      foreshadowContext,
	})

	systemPrompt := state.CorePrompt
	if systemPrompt == "" {
		systemPrompt = "你是一位小说作者。"
	}

	onChunk := func(chunk string) {
		logger.ContentChunk(idx, chunk)
	}

	return CallAPIStream(cfg, systemPrompt, userPrompt, onChunk)
}

func generateChapterContentWithRetry(cfg *Config, state *Progress, idx int) string {
	retryCount := 0
	for {
		content, err := generateChapterContent(cfg, state, idx)
		if err == nil && content != "" {
			return content
		}

		retryCount++
		waitTime := getWaitTime(retryCount)
		fmt.Printf(" ⚠️ [错误] 正文生成失败: %v。第 %d 次重试，等待 %ds 后重试...\n", err, retryCount, waitTime)
		time.Sleep(time.Duration(waitTime) * time.Second)
	}
}

func generateChapterContentStreamWithRetry(cfg *Config, state *Progress, idx int, logger *LogBroadcaster) string {
	retryCount := 0
	for {
		content, err := generateChapterContentStream(cfg, state, idx, logger)
		if err == nil && content != "" {
			return content
		}

		retryCount++
		waitTime := getWaitTime(retryCount)
		fmt.Printf(" ⚠️ [错误] 流式正文生成失败: %v。第 %d 次重试，等待 %ds 后重试...\n", err, retryCount, waitTime)
		time.Sleep(time.Duration(waitTime) * time.Second)
	}
}

func generateChapterSummary(cfg *Config, content string) (string, error) {
	userPrompt := RenderPrompt(cfg.Prompts.ChapterSummary, map[string]string{
		"ChapterContent": content,
	})

	systemPrompt := "你是一位精准的小说叙事状态分析师。"
	return CallAPI(cfg, systemPrompt, userPrompt)
}

func generateChapterSummaryWithRetry(cfg *Config, content string) string {
	retryCount := 0
	for {
		summary, err := generateChapterSummary(cfg, content)
		if err == nil && summary != "" {
			return summary
		}

		retryCount++
		waitTime := getWaitTime(retryCount)
		fmt.Printf(" ⚠️ [错误] 摘要提炼失败: %v。第 %d 次重试，等待 %ds 后重试...\n", err, retryCount, waitTime)
		time.Sleep(time.Duration(waitTime) * time.Second)
	}
}

func generateChapterFactCheck(cfg *Config, content string, historySummary string) (string, error) {
	userPrompt := RenderPrompt(cfg.Prompts.FactCheck, map[string]string{
		"ChapterContent": content,
		"HistorySummary": historySummary,
		"CorePrompt":     "",
	})

	systemPrompt := "你是一位严谨的小说事实核查员。请严格按照要求的JSON格式输出。"
	return CallAPI(cfg, systemPrompt, userPrompt)
}

func generateChapterFactCheckWithRetry(cfg *Config, content string, historySummary string) string {
	retryCount := 0
	for {
		result, err := generateChapterFactCheck(cfg, content, historySummary)
		if err == nil && result != "" {
			return result
		}

		retryCount++
		waitTime := getWaitTime(retryCount)
		fmt.Printf(" ⚠️ [错误] 事实核查失败: %v。第 %d 次重试，等待 %ds 后重试...\n", err, retryCount, waitTime)
		time.Sleep(time.Duration(waitTime) * time.Second)
	}
}

func reviseChapterContent(cfg *Config, state *Progress, chapterIdx int, userFeedback string) (string, error) {
	ch := state.Chapters[chapterIdx]

	historySummary := buildHistorySummary(state, chapterIdx)

	systemPrompt := state.CorePrompt
	if systemPrompt == "" {
		systemPrompt = "你是一位小说作者。"
	}

	userPrompt := fmt.Sprintf(`请根据以下意见修改第 %d 章《%s》的正文。

【核心写作提示词】%s
【核心写作要求】%s
【前情提要】%s
【本章大纲】%s
【用户修改意见】%s

请输出修改后的完整章节正文。`,
		ch.Num, ch.Title,
		state.CorePrompt, state.CoreRequirements,
		historySummary, ch.Outline, userFeedback)

	return CallAPI(cfg, systemPrompt, userPrompt)
}

func reviseChapterContentStream(cfg *Config, state *Progress, chapterIdx int, userFeedback string, logger *LogBroadcaster) (string, error) {
	ch := state.Chapters[chapterIdx]

	historySummary := buildHistorySummary(state, chapterIdx)

	systemPrompt := state.CorePrompt
	if systemPrompt == "" {
		systemPrompt = "你是一位小说作者。"
	}

	userPrompt := fmt.Sprintf(`请根据以下意见修改第 %d 章《%s》的正文。

【核心写作提示词】%s
【核心写作要求】%s
【前情提要】%s
【本章大纲】%s
【用户修改意见】%s

请输出修改后的完整章节正文。`,
		ch.Num, ch.Title,
		state.CorePrompt, state.CoreRequirements,
		historySummary, ch.Outline, userFeedback)

	onChunk := func(chunk string) {
		logger.ContentChunk(chapterIdx, chunk)
	}

	return CallAPIStream(cfg, systemPrompt, userPrompt, onChunk)
}

func reviseSubsequentOutlines(cfg *Config, state *Progress, currentIdx int, userFeedback string) error {
	subsequentChapters := ""
	for i := currentIdx + 1; i < len(state.Chapters); i++ {
		ch := state.Chapters[i]
		if ch.Status != StatusAccepted {
			subsequentChapters += fmt.Sprintf("第%d章《%s》: %s\n", ch.Num, ch.Title, ch.Outline)
		}
	}
	if subsequentChapters == "" {
		return nil
	}

	lockedChapters := ""
	for i := 0; i <= currentIdx; i++ {
		ch := state.Chapters[i]
		lockedChapters += fmt.Sprintf("第%d章《%s》（摘要）: %s\n", ch.Num, ch.Title, ch.Summary)
	}

	userPrompt := RenderPrompt(cfg.Prompts.OutlineRevision, map[string]string{
		"CurrentOutline": subsequentChapters,
		"UserFeedback":   fmt.Sprintf("用户对第%d章提出了修改意见：%s\n请根据此意见修订后续章节大纲。", state.Chapters[currentIdx].Num, userFeedback),
		"LockedChapters": lockedChapters,
	})

	systemPrompt := "你是一位小说策划编辑。请严格按照要求的JSON格式输出，不要添加任何额外文字或markdown代码块标记。已锁定的章节内容不可修改。"

	rawResp := CallAPIWithRetry(cfg, systemPrompt, userPrompt)
	rawResp = cleanJSONResponse(rawResp)

	var resp OutlineResponse
	if err := json.Unmarshal([]byte(rawResp), &resp); err != nil {
		return fmt.Errorf("解析修订大纲JSON失败: %w", err)
	}

	for _, newCh := range resp.Chapters {
		for i, existingCh := range state.Chapters {
			if existingCh.Num == newCh.Num && existingCh.Status != StatusAccepted {
				state.Chapters[i].Title = newCh.Title
				state.Chapters[i].Outline = newCh.Outline
			}
		}
	}

	return nil
}

func buildHistorySummary(state *Progress, idx int) string {
	startIdx := 0
	if idx > 5 {
		startIdx = idx - 5
	}
	var history string
	for i := startIdx; i < idx; i++ {
		if state.Chapters[i].Summary != "" {
			history += fmt.Sprintf("[第%d章摘要]: %s\n", state.Chapters[i].Num, state.Chapters[i].Summary)
		}
	}
	if history == "" {
		history = "当前为故事开端，无历史前情。"
	}
	return history
}
