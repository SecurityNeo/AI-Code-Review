package pipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// RepoManager 管理本地 git 仓库镜像（方案 C：持久化代码库）
// 目录结构：{CODEGUARD_WORKSPACE}/repos/project_{project_id}/{branch}/
type RepoManager struct {
	baseDir string
	mu      sync.RWMutex
	cache   map[uint]map[string]time.Time // projectID -> branch -> lastUsed
}

var (
	defaultRepoManager *RepoManager
	onceRepoManager    sync.Once
)

// GetRepoManager 获取默认 RepoManager 实例（单例）
func GetRepoManager() *RepoManager {
	onceRepoManager.Do(func() {
		defaultRepoManager = NewRepoManager()
	})
	return defaultRepoManager
}

// NewRepoManager 创建 RepoManager
func NewRepoManager() *RepoManager {
	baseDir := os.Getenv("CODEGUARD_WORKSPACE")
	if baseDir == "" {
		baseDir = "/tmp/codeguard/repos"
	}
	os.MkdirAll(baseDir, 0755)
	zap.L().Info("RepoManager 初始化", zap.String("workspace", baseDir))
	return &RepoManager{
		baseDir: baseDir,
		cache:   make(map[uint]map[string]time.Time),
	}
}

// GetRepoDir 获取仓库目录路径
func (m *RepoManager) GetRepoDir(projectID uint, branch string) string {
	return filepath.Join(m.baseDir, fmt.Sprintf("project_%d", projectID), branch)
}

// EnsureRepo 确保本地仓库存在且是最新代码
// 如果不存在 → clone
// 如果存在  → fetch + checkout
func (m *RepoManager) EnsureRepo(projectPath, accessToken, branch string, projectID uint) (string, error) {
	repoDir := m.GetRepoDir(projectID, branch)

	// 尝试复用已有仓库
	if m.isValidRepo(repoDir) {
		fetchErr := m.fetchAndCheckout(repoDir, branch)
		if fetchErr == nil {
			m.updateCache(projectID, branch)
			return repoDir, nil
		}
		// fetch 失败，删除重新 clone
		zap.L().Warn("fetch 失败，重新 clone", zap.String("dir", repoDir), zap.Error(fetchErr))
		os.RemoveAll(repoDir)
	}

	// clone
	gitURL := m.buildGitURL(projectPath, accessToken)
	if err := m.clone(gitURL, repoDir, branch); err != nil {
		return "", err
	}

	m.updateCache(projectID, branch)
	return repoDir, nil
}

// isValidRepo 检查目录是否是有效的 git 仓库
func (m *RepoManager) isValidRepo(repoDir string) bool {
	gitDir := filepath.Join(repoDir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return false
	}
	// 额外验证 git status 能正常工作
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--git-dir")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

// buildGitURL 构建带认证的 git URL
// 支持 https 格式：{project_path}.git → https://oauth2:{token}@host/group/project.git
func (m *RepoManager) buildGitURL(projectPath, accessToken string) string {
	url := strings.TrimSuffix(strings.TrimSpace(projectPath), "/")
	if !strings.HasSuffix(url, ".git") {
		url += ".git"
	}

	if accessToken == "" {
		return url
	}

	// 在 scheme 后插入 oauth2:{token}@
	if strings.HasPrefix(url, "https://") {
		return strings.Replace(url, "https://", fmt.Sprintf("https://oauth2:%s@", accessToken), 1)
	}
	if strings.HasPrefix(url, "http://") {
		return strings.Replace(url, "http://", fmt.Sprintf("http://oauth2:%s@", accessToken), 1)
	}
	return url
}

func (m *RepoManager) clone(gitURL, repoDir, branch string) error {
	err := os.MkdirAll(filepath.Dir(repoDir), 0755)
	if err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	args := []string{"clone", "--depth", "1", "--single-branch", "--branch", branch, gitURL, repoDir}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_NO_VERIFY=1")

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone 失败: %w\n输出: %s", err, string(out))
	}
	zap.L().Info("git clone 成功", zap.String("branch", branch), zap.String("dir", repoDir))
	return nil
}

func (m *RepoManager) fetchAndCheckout(repoDir, branch string) error {
	// fetch 最新
	cmd := exec.Command("git", "-C", repoDir, "fetch", "--depth", "1", "origin", branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_NO_VERIFY=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch 失败: %w\n输出: %s", err, string(out))
	}

	// 强制 checkout（丢弃本地修改）
	cmd = exec.Command("git", "-C", repoDir, "checkout", "-f", "-B", branch, "origin/"+branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSL_NO_VERIFY=1")
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout 失败: %w\n输出: %s", err, string(out))
	}

	zap.L().Info("git fetch + checkout 成功", zap.String("branch", branch))
	return nil
}

func (m *RepoManager) updateCache(projectID uint, branch string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cache[projectID] == nil {
		m.cache[projectID] = make(map[string]time.Time)
	}
	m.cache[projectID][branch] = time.Now()
}

// CleanupInactive 清理超过 maxAge 未使用的仓库
func (m *RepoManager) CleanupInactive(maxAge time.Duration) (int, error) {
	m.mu.RLock()
	now := time.Now()
	var toDelete []string
	for pid, branches := range m.cache {
		for branch, lastUsed := range branches {
			if now.Sub(lastUsed) > maxAge {
				toDelete = append(toDelete, m.GetRepoDir(pid, branch))
			}
		}
	}
	m.mu.RUnlock()

	deleted := 0
	for _, dir := range toDelete {
		if err := os.RemoveAll(dir); err != nil {
			zap.L().Warn("清理仓库失败", zap.String("dir", dir), zap.Error(err))
		} else {
			deleted++
			zap.L().Info("清理不活跃仓库", zap.String("dir", dir))
		}
	}
	return deleted, nil
}
