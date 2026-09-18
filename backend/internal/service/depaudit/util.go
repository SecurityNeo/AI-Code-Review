package depaudit

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

func itoa(n int) string { return strconv.Itoa(n) }

// sanitizeFileName 生成安全的目录/文件名（并附加 ID 防重名）
func sanitizeFileName(name string, id uint) string {
	name = strings.TrimSpace(name)
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_", "\n", "_", "\r", "_", " ", "_")
	name = replacer.Replace(name)
	if name == "" {
		name = "project"
	}
	return name + "_" + strconv.FormatUint(uint64(id), 10)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// truncateStr 截断字符串到最多 n 个 rune
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// readAllLines 将文本按行拆分为二维字符串数组（简单 CSV，不支持引号转义）
func readAllLines(r io.Reader) ([][]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	var out [][]string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, strings.Split(line, ","))
	}
	return out, scanner.Err()
}
