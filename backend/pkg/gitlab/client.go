package gitlab

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type CommitInfo struct {
	ID         string `json:"id"`
	ShortID    string `json:"short_id"`
	Title      string `json:"title"`
	Message    string `json:"message"`
	CreatedAt  string `json:"created_at"`
	AuthorName string `json:"author_name"`
}

type MergeRequestInfo struct {
	Title          string `json:"title"`
	State          string `json:"state"`
	SourceBranch   string `json:"source_branch"`
	TargetBranch   string `json:"target_branch"`
	CreatedAt      string `json:"created_at"`
	Draft          bool   `json:"draft"`
	WorkInProgress bool   `json:"work_in_progress"`
}

// DiffRefs GitLab MR 的 diff_refs（用于行内评论定位）
type DiffRefs struct {
	BaseSha  string `json:"base_sha"`
	HeadSha  string `json:"head_sha"`
	StartSha string `json:"start_sha"`
}

func NewClient(gitlabHost, token string) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	return &Client{
		baseURL:    strings.TrimRight(gitlabHost, "/"),
		token:      token,
		httpClient: &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

// ExtractGitLabHost 从 project_path 提取 GitLab host
func ExtractGitLabHost(projectPath string) string {
	u, err := url.Parse(projectPath)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}

// GetMergeRequestCommits 获取 MR 的所有 commit
func (c *Client) GetMergeRequestCommits(projectID, mrIID int) ([]CommitInfo, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/commits", projectID, mrIID)
	return c.getCommits(path)
}

// GetMergeRequest 获取 MR 详情
func (c *Client) GetMergeRequest(projectID, mrIID int) (*MergeRequestInfo, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d", projectID, mrIID)
	return c.getMR(path)
}

// GetMergeRequestDiffFiles 调用 GitLab API 获取 MR 变更文件列表（含 diff 文本和统计）
func (c *Client) GetMergeRequestDiffFiles(projectID, mrIID int) ([]DiffFile, int, int, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/changes?access_raw_diffs=true", projectID, mrIID)
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, 0, 0, err
	}
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, 0, fmt.Errorf("gitlab api returned status %d", resp.StatusCode)
	}

	var result struct {
		Changes []struct {
			OldPath string `json:"old_path"`
			NewPath string `json:"new_path"`
			Diff    string `json:"diff"`
		} `json:"changes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, 0, err
	}

	var files []DiffFile
	var totalAdditions, totalDeletions int

	for _, ch := range result.Changes {
		if strings.TrimSpace(ch.Diff) == "" {
			continue
		}
		add, del := countDiffStats(ch.Diff)
		files = append(files, DiffFile{
			OldPath:   ch.OldPath,
			NewPath:   ch.NewPath,
			Diff:      ch.Diff,
			Additions: add,
			Deletions: del,
		})
		totalAdditions += add
		totalDeletions += del
	}

	return files, totalAdditions, totalDeletions, nil
}

// countDiffStats 逐行统计 diff 的 additions / deletions
func countDiffStats(diffText string) (int, int) {
	additions, deletions := 0, 0
	lines := strings.Split(diffText, "\n")
	for _, line := range lines {
		if len(line) > 0 {
			switch line[0] {
			case '+':
				if !strings.HasPrefix(line, "+++") {
					additions++
				}
			case '-':
				if !strings.HasPrefix(line, "---") {
					deletions++
				}
			}
		}
	}
	return additions, deletions
}

// ParseDiffFiles 解析 raw diff 文本（含 diff --git header），返回文件列表和统计信息
// 备用方法，当 diff 来自 raw unified diff 时使用
type DiffFile struct {
	OldPath   string
	NewPath   string
	Diff      string
	Additions int
	Deletions int
}

func ParseDiffFiles(rawDiff string) ([]DiffFile, int, int) {
	var files []DiffFile
	var totalAdditions, totalDeletions int
	lines := strings.Split(rawDiff, "\n")

	var current *DiffFile
	var inHunk bool
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "diff --git ") {
			if current != nil {
				files = append(files, *current)
			}
			current = &DiffFile{}
			inHunk = false
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			current.OldPath = strings.TrimPrefix(line, "--- ")
			if strings.HasPrefix(current.OldPath, "a/") {
				current.OldPath = current.OldPath[2:]
			}
			current.Diff += line + "\n"
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			current.NewPath = strings.TrimPrefix(line, "+++ ")
			if strings.HasPrefix(current.NewPath, "b/") {
				current.NewPath = current.NewPath[2:]
			}
			current.Diff += line + "\n"
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			current.Diff += line + "\n"
			continue
		}
		if inHunk {
			if len(line) > 0 {
				if line[0] == '+' {
					current.Additions++
					totalAdditions++
				} else if line[0] == '-' {
					current.Deletions++
					totalDeletions++
				}
			}
			current.Diff += line + "\n"
		} else {
			current.Diff += line + "\n"
		}
	}
	if current != nil {
		files = append(files, *current)
	}
	return files, totalAdditions, totalDeletions
}

func (c *Client) getCommits(path string) ([]CommitInfo, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab api returned status %d", resp.StatusCode)
	}

	var commits []CommitInfo
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return nil, err
	}
	return commits, nil
}

func (c *Client) getMR(path string) (*MergeRequestInfo, error) {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab api returned status %d", resp.StatusCode)
	}

	var mr MergeRequestInfo
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// GetMergeRequestDiffRefs 获取 MR 的 diff_refs（base_sha / head_sha / start_sha）
// 用于行内评论定位
func (c *Client) GetMergeRequestDiffRefs(projectID, mrIID int) (*DiffRefs, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d", projectID, mrIID)
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab api returned status %d", resp.StatusCode)
	}

	var result struct {
		DiffRefs *DiffRefs `json:"diff_refs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.DiffRefs == nil {
		return nil, fmt.Errorf("diff_refs not found in merge request response")
	}
	return result.DiffRefs, nil
}

// UpdateMergeRequestTitle 更新 MR title
func (c *Client) UpdateMergeRequestTitle(projectID, mrIID int, title string) error {
	path := fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d", projectID, mrIID)
	reqBody := fmt.Sprintf(`{"title":"%s"}`, title)
	req, err := http.NewRequest("PUT", c.baseURL+path, strings.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gitlab api returned status %d", resp.StatusCode)
	}
	return nil
}

// ParseMRFidFromURL 从 MR URL 解析 project_id 和 mr_iid
func ParseMRFidFromURL(mrURL string) (int, int, error) {
	u, err := url.Parse(mrURL)
	if err != nil {
		return 0, 0, err
	}
	// URL 格式: https://gitlab.example.com/group/project/-/merge_requests/3
	path := u.Path
	parts := strings.Split(path, "/-/merge_requests/")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid mr url format: %s", mrURL)
	}
	mrIID, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("cannot parse mr iid from url: %s", mrURL)
	}
	return 0, mrIID, nil // project_id 需要另外获取
}

// FetchMRDetails 为一条 MergeRequestReviewLog 获取并更新 GitLab 详情
func FetchMRDetails(log *model.MergeRequestReviewLog, projectID int, token string) error {
	if log.URL == "" || projectID == 0 {
		return nil
	}

	_, mrIID, err := ParseMRFidFromURL(log.URL)
	if err != nil {
		zap.L().Warn("parse mr iid from url failed", zap.String("url", log.URL), zap.Error(err))
		return nil // 不阻塞同步
	}

	host := ExtractGitLabHost(log.URL)
	if host == "" {
		return nil
	}

	client := NewClient(host, token)

	// 获取 MR 详情
	mr, err := client.GetMergeRequest(projectID, mrIID)
	if err != nil {
		zap.L().Warn("get merge request from gitlab failed",
			zap.String("url", log.URL),
			zap.Int("project_id", projectID),
			zap.Int("mr_iid", mrIID),
			zap.Error(err))
		return nil
	}
	log.MRTitle = mr.Title
	log.MRState = mr.State
	log.IsDraft = mr.Draft || mr.WorkInProgress
	log.MRCreatedAt = parseGitLabTime(mr.CreatedAt)

	// 获取 commits
	commits, err := client.GetMergeRequestCommits(projectID, mrIID)
	if err != nil {
		zap.L().Warn("get merge request commits from gitlab failed",
			zap.String("url", log.URL),
			zap.Int("project_id", projectID),
			zap.Int("mr_iid", mrIID),
			zap.Error(err))
		return nil
	}

	commitsJSON, err := json.Marshal(commits)
	if err != nil {
		zap.L().Error("marshal commits failed", zap.Error(err))
		return nil
	}
	log.Commits = string(commitsJSON)

	return nil
}

func parseGitLabTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.000",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// RepositoryFile 表示 GitLab 仓库文件的元数据
// GetRepositoryFileRaw 获取文件原始内容（不通过 base64 解码）
func (c *Client) GetRepositoryFileRaw(projectID int, filePath, branch string) (string, error) {
	// file_path 需要替换 / 为 %2F 进行路径编码
	encodedPath := url.PathEscape(filePath)
	path := fmt.Sprintf("/api/v4/projects/%d/repository/files/%s/raw?ref=%s", projectID, encodedPath, url.QueryEscape(branch))
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("file not found: %s (ref=%s)", filePath, branch)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gitlab api returned status %d for %s", resp.StatusCode, filePath)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
