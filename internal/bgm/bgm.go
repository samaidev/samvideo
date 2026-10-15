package bgm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"samvideo/internal/config"
	"samvideo/internal/utils"
)

// EnsureBGM 确保背景音乐文件存在，如果不存在则生成
func EnsureBGM(cfg *config.Config) ([]string, error) {
	songsDir := filepath.Join(cfg.ResourceDir, "songs")
	if err := os.MkdirAll(songsDir, 0755); err != nil {
		return nil, fmt.Errorf("create songs dir: %w", err)
	}

	entries, err := os.ReadDir(songsDir)
	if err == nil {
		var existing []string
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".mp3") || strings.HasSuffix(name, ".m4a") || strings.HasSuffix(name, ".wav") {
				existing = append(existing, filepath.Join(songsDir, name))
			}
		}
		if len(existing) > 0 {
			utils.LogInfo("found %d existing BGM files", len(existing))
			return existing, nil
		}
	}

	utils.LogInfo("generating BGM files...")

	var generated []string

	// 1. 轻柔钢琴风格（C 大调和弦：C-E-G-C）
	pianoPath := filepath.Join(songsDir, "ambient_piano.mp3")
	if err := generateAmbientPiano(pianoPath); err != nil {
		utils.LogWarn("failed to generate ambient piano BGM: %v", err)
	} else {
		generated = append(generated, pianoPath)
		utils.LogInfo("generated: %s", pianoPath)
	}

	// 2. 企业宣传风格（D 大调，温暖明亮）
	corporatePath := filepath.Join(songsDir, "corporate_uplifting.mp3")
	if err := generateCorporateUplifting(corporatePath); err != nil {
		utils.LogWarn("failed to generate corporate BGM: %v", err)
	} else {
		generated = append(generated, corporatePath)
		utils.LogInfo("generated: %s", corporatePath)
	}

	// 3. 科技感电子风格（E 小调，低频脉冲）
	techPath := filepath.Join(songsDir, "tech_electronic.mp3")
	if err := generateTechElectronic(techPath); err != nil {
		utils.LogWarn("failed to generate tech BGM: %v", err)
	} else {
		generated = append(generated, techPath)
		utils.LogInfo("generated: %s", techPath)
	}

	if len(generated) == 0 {
		return nil, fmt.Errorf("failed to generate any BGM files")
	}

	utils.LogInfo("generated %d BGM files", len(generated))
	return generated, nil
}

// generateAmbientPiano 生成轻柔钢琴风格 BGM
// C 大调和弦：C4(261.63) E4(329.63) G4(392.00) C5(523.25)
func generateAmbientPiano(outputPath string) error {
	args := []string{
		"-y",
		"-f", "lavfi", "-i", "sine=frequency=261.63:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=329.63:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=392.00:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=523.25:duration=60",
		"-filter_complex",
		"[0:a]volume=0.3[a1];[1:a]volume=0.2[a2];[2:a]volume=0.15[a3];[3:a]volume=0.1[a4];" +
			"[a1][a2][a3][a4]amix=inputs=4:duration=longest[a];" +
			"[a]aecho=0.6:0.3:1000:0.3,aecho=0.4:0.2:500:0.2,volume=0.5[out]",
		"-map", "[out]",
		"-c:a", "libmp3lame", "-b:a", "128k",
		"-t", "60",
		outputPath,
	}
	return runFFmpeg(args)
}

// generateCorporateUplifting 生成企业宣传风格 BGM
// D 大调：D4(293.66) F#4(369.99) A4(440.00) D5(587.33)
func generateCorporateUplifting(outputPath string) error {
	args := []string{
		"-y",
		"-f", "lavfi", "-i", "sine=frequency=293.66:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=369.99:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=440.00:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=587.33:duration=60",
		"-filter_complex",
		"[0:a]volume=0.25[a1];[1:a]volume=0.2[a2];[2:a]volume=0.15[a3];[3:a]volume=0.1[a4];" +
			"[a1][a2][a3][a4]amix=inputs=4:duration=longest[a];" +
			"[a]aecho=0.5:0.25:800:0.2,tremolo=f=4:d=0.3,volume=0.4[out]",
		"-map", "[out]",
		"-c:a", "libmp3lame", "-b:a", "128k",
		"-t", "60",
		outputPath,
	}
	return runFFmpeg(args)
}

// generateTechElectronic 生成科技感电子风格 BGM
// E 小调：E2(82.41) G3(196.00) B3(246.94) E4(329.63)
func generateTechElectronic(outputPath string) error {
	args := []string{
		"-y",
		"-f", "lavfi", "-i", "sine=frequency=82.41:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=196.00:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=246.94:duration=60",
		"-f", "lavfi", "-i", "sine=frequency=329.63:duration=60",
		"-filter_complex",
		"[0:a]tremolo=f=2:d=0.5,volume=0.3[base];" +
			"[1:a]volume=0.15[b1];[2:a]volume=0.1[b2];[b1][b2]amix=inputs=2:duration=longest[mid];" +
			"[3:a]volume=0.08[high];" +
			"[base][mid]amix=inputs=2:duration=longest[a1];[a1][high]amix=inputs=2:duration=longest[a];" +
			"[a]aecho=0.4:0.15:300:0.15,volume=0.45[out]",
		"-map", "[out]",
		"-c:a", "libmp3lame", "-b:a", "128k",
		"-t", "60",
		outputPath,
	}
	return runFFmpeg(args)
}

// runFFmpeg 执行 ffmpeg 命令
func runFFmpeg(args []string) error {
	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w, output: %s", err, string(out))
	}
	return nil
}
