package bgm

import (
	"fmt"
	"math/rand"
	"os/exec"
	"os"
	"path/filepath"
	"strings"

	"samvideo/internal/config"
	"samvideo/internal/utils"
)

// EnsureBGM 确保背景音乐文件存在
// 优先使用 downloaded/ 目录下的真实音乐，其次用 Python 脚本生成
func EnsureBGM(cfg *config.Config) ([]string, error) {
	songsDir := filepath.Join(cfg.ResourceDir, "songs")
	if err := os.MkdirAll(songsDir, 0755); err != nil {
		return nil, fmt.Errorf("create songs dir: %w", err)
	}

	// 1. 优先收集 downloaded/ 子目录下的真实音乐
	downloadedDir := filepath.Join(songsDir, "downloaded")
	if entries, err := os.ReadDir(downloadedDir); err == nil {
		var downloaded []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".mp3") || strings.HasSuffix(name, ".m4a") || strings.HasSuffix(name, ".wav") {
				downloaded = append(downloaded, filepath.Join(downloadedDir, name))
			}
		}
		if len(downloaded) > 0 {
			utils.LogInfo("found %d downloaded BGM files", len(downloaded))
			return downloaded, nil
		}
	}

	// 2. 收集 songs/ 根目录下的已有文件
	entries, err := os.ReadDir(songsDir)
	if err == nil {
		var existing []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".mp3") {
				existing = append(existing, filepath.Join(songsDir, name))
			}
		}
		if len(existing) >= 3 {
			utils.LogInfo("found %d existing BGM files", len(existing))
			return existing, nil
		}
	}

	// 3. 用 Python 脚本生成合成音乐作为后备
	utils.LogInfo("generating BGM files via Python script...")

	scriptPath := findScript()
	if scriptPath == "" {
		return nil, fmt.Errorf("gen_bgm.py not found")
	}

	cmd := exec.Command("python3", scriptPath, songsDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gen_bgm.py failed: %w", err)
	}

	// WAV 转 MP3
	wavFiles, _ := os.ReadDir(songsDir)
	var generated []string
	for _, f := range wavFiles {
		if !strings.HasSuffix(f.Name(), ".wav") {
			continue
		}
		wavPath := filepath.Join(songsDir, f.Name())
		mp3Path := strings.TrimSuffix(wavPath, ".wav") + ".mp3"
		convCmd := exec.Command("ffmpeg", "-y", "-i", wavPath, "-c:a", "libmp3lame", "-b:a", "192k", mp3Path)
		if out, err := convCmd.CombinedOutput(); err != nil {
			utils.LogWarn("failed to convert %s: %v, %s", f.Name(), err, string(out))
			continue
		}
		os.Remove(wavPath)
		generated = append(generated, mp3Path)
		utils.LogInfo("generated: %s", mp3Path)
	}

	if len(generated) == 0 {
		return nil, fmt.Errorf("failed to generate any BGM files")
	}

	utils.LogInfo("generated %d BGM files", len(generated))
	return generated, nil
}

// SelectRandomBGM 从 BGM 列表中随机选择一首
func SelectRandomBGM(bgmFiles []string) string {
	if len(bgmFiles) == 0 {
		return ""
	}
	return bgmFiles[rand.Intn(len(bgmFiles))]
}

// findScript 查找 gen_bgm.py 脚本
func findScript() string {
	candidates := []string{
		"scripts/gen_bgm.py",
		filepath.Join("..", "scripts", "gen_bgm.py"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
