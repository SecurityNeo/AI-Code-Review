package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"go.uber.org/zap"
)

// httpClientCache 按 skipVerify 配置缓存不同 Transport 的 Client
var httpClientCache sync.Map

func getHTTPClient(skipVerify bool) *http.Client {
	key := skipVerify
	if v, ok := httpClientCache.Load(key); ok {
		return v.(*http.Client)
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: skipVerify},
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport}
	httpClientCache.Store(key, client)
	return client
}

// GitLabInstanceOAuthConfig OAuth 配置（从 GitLabInstance 或 SystemConfig 读取）
type GitLabInstanceOAuthConfig struct {
	InstanceID     uint
	BaseURL        string
	ClientID       string
	ClientSecret   string
	RedirectURI    string
	AutoCreateUser bool
	SkipVerify     bool
}

// loadInstanceConfig 加载指定实例的 OAuth 配置
// instance_id=1 时优先从 gitlab_instances 表查找（ migration 后旧 system_config 字段已清空）
func loadInstanceConfig(instanceID uint) (*GitLabInstanceOAuthConfig, error) {
	// 优先从 GitLabInstance 表读取（包括 id=1）
	var inst model.GitLabInstance
	if err := model.DB.First(&inst, instanceID).Error; err == nil {
		if !inst.OAuthEnabled {
			return nil, fmt.Errorf("gitlab oauth not enabled for instance %d", instanceID)
		}
		return &GitLabInstanceOAuthConfig{
			InstanceID:     inst.ID,
			BaseURL:        inst.BaseURL,
			ClientID:       inst.OAuthClientID,
			ClientSecret:   inst.OAuthClientSecret,
			RedirectURI:    inst.OAuthRedirectURI,
			AutoCreateUser: inst.OAuthAutoCreateUser,
			SkipVerify:     inst.OAuthSkipVerify,
		}, nil
	}

	// 仅当 gitlab_instances 中无该记录时才回退到 SystemConfig（兼容旧单实例无 migration 场景）
	if instanceID == 1 {
		var cfg model.SystemConfig
		if err := model.DB.First(&cfg).Error; err != nil {
			return nil, fmt.Errorf("load system config failed: %w", err)
		}
		if !cfg.GitlabOAuthEnabled {
			return nil, fmt.Errorf("gitlab oauth not enabled")
		}
		return &GitLabInstanceOAuthConfig{
			InstanceID:     1,
			BaseURL:        cfg.GitlabBaseURL,
			ClientID:       cfg.GitlabOAuthClientID,
			ClientSecret:   cfg.GitlabOAuthClientSecret,
			RedirectURI:    cfg.GitlabOAuthRedirectURI,
			AutoCreateUser: cfg.GitlabOAuthAutoCreateUser,
			SkipVerify:     cfg.GitlabOAuthSkipVerify,
		}, nil
	}

	return nil, fmt.Errorf("gitlab instance not found: %d", instanceID)
}

// GitLabOAuthService 处理 GitLab OAuth 流程
type GitLabOAuthService struct{}

// NewGitLabOAuthService 创建 OAuth 服务
func NewGitLabOAuthService() *GitLabOAuthService {
	return &GitLabOAuthService{}
}

// BuildAuthURL 构造 GitLab 授权 URL
func (s *GitLabOAuthService) BuildAuthURL(state string, instanceID uint) (string, error) {
	oc, err := loadInstanceConfig(instanceID)
	if err != nil {
		return "", err
	}
	baseURL := strings.TrimSuffix(oc.BaseURL, "/")

	if baseURL == "" {
		return "", fmt.Errorf("GitLab 地址未配置")
	}
	if oc.ClientID == "" || oc.ClientSecret == "" {
		return "", fmt.Errorf("Client ID 或 Client Secret 未配置")
	}
	if strings.Contains(baseURL, "/api/") || strings.HasSuffix(baseURL, "/login") {
		return "", fmt.Errorf("GitLab 地址配置错误：'%s' 看起来是 CodeGuard 自身地址", baseURL)
	}

	params := url.Values{
		"client_id":     {oc.ClientID},
		"redirect_uri":  {oc.RedirectURI},
		"response_type": {"code"},
		"state":         {state},
		"scope":         {"read_user"},
	}
	return fmt.Sprintf("%s/oauth/authorize?%s", baseURL, params.Encode()), nil
}

// ExchangeCode 用 authorization code 换 access_token
func (s *GitLabOAuthService) ExchangeCode(code string, instanceID uint) (string, error) {
	oc, err := loadInstanceConfig(instanceID)
	if err != nil {
		return "", err
	}
	baseURL := strings.TrimSuffix(oc.BaseURL, "/")
	tokenURL := fmt.Sprintf("%s/oauth/token", baseURL)

	data := url.Values{
		"client_id":     {oc.ClientID},
		"client_secret": {oc.ClientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {oc.RedirectURI},
	}

	resp, err := getHTTPClient(oc.SkipVerify).Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gitlab token endpoint returned %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token response failed: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("empty access_token")
	}
	return tokenResp.AccessToken, nil
}

// GitLabUserInfo GitLab API /api/v4/user 返回的结构
type GitLabUserInfo struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
	State     string `json:"state"`
}

// GetUserInfo 用 access_token 获取 GitLab 用户信息
func (s *GitLabOAuthService) GetUserInfo(accessToken string, instanceID uint) (*GitLabUserInfo, error) {
	oc, err := loadInstanceConfig(instanceID)
	if err != nil {
		return nil, err
	}
	baseURL := strings.TrimSuffix(oc.BaseURL, "/")
	apiURL := fmt.Sprintf("%s/api/v4/user", baseURL)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := getHTTPClient(oc.SkipVerify)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get user info failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab user endpoint returned %d", resp.StatusCode)
	}

	var userInfo GitLabUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, fmt.Errorf("decode user info failed: %w", err)
	}
	if userInfo.ID == 0 {
		return nil, fmt.Errorf("invalid user info: empty id")
	}
	return &userInfo, nil
}

// FindOrCreateUser 根据 GitLab 用户信息查找或创建本地用户（事务保护 + 多实例绑定）
func (s *GitLabOAuthService) FindOrCreateUser(info *GitLabUserInfo, instanceID uint) (*model.User, bool, error) {
	tx := model.DB.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 1. 按 (gitlab_instance_id, gitlab_user_id) 精确匹配绑定的用户
	var binding model.GitLabUserBinding
	if err := tx.Where("gitlab_instance_id = ? AND gitlab_user_id = ?", instanceID, info.ID).First(&binding).Error; err == nil {
		// 已存在绑定，返回对应本地用户
		var user model.User
		if err := tx.First(&user, binding.UserID).Error; err == nil {
			tx.Commit()
			return &user, false, nil
		}
		// 数据不一致：binding 存在但 user 已被删除，删除孤儿绑定后重新创建
		zap.L().Warn("orphan binding found: user missing, deleting and recreating",
			zap.Uint("binding_id", binding.ID), zap.Uint("user_id", binding.UserID))
		if err := tx.Where("id = ?", binding.ID).Delete(&model.GitLabUserBinding{}).Error; err != nil {
			tx.Rollback()
			zap.L().Error("delete orphan binding failed", zap.Error(err))
			return nil, false, fmt.Errorf("清理孤儿绑定失败: %w", err)
		}
		// 继续执行下面的自动创建逻辑（在同一事务中）
	}

	// 2. 未匹配到绑定，检查是否允许自动创建
	oc, err := loadInstanceConfig(instanceID)
	if err != nil {
		tx.Rollback()
		return nil, false, err
	}

	// 需要先获取实例所属组织以绑定用户
	var gitlabInstance model.GitLabInstance
	if err := model.DB.First(&gitlabInstance, instanceID).Error; err != nil {
		tx.Rollback()
		return nil, false, fmt.Errorf("无法获取 GitLab 实例信息: %w", err)
	}

	// 2.5 检查本地是否已存在同名用户
	var existingUser model.User
	if err := tx.Where("username = ?", info.Username).First(&existingUser).Error; err == nil {
		// 已有同名本地用户，复用该用户并创建 GitLab 绑定
		newBinding := model.GitLabUserBinding{
			UserID:           existingUser.ID,
			GitLabInstanceID: instanceID,
			GitLabUserID:     uint(info.ID),
			GitLabUsername:   info.Username,
			GitLabEmail:      info.Email,
			GitLabAvatarURL:  info.AvatarURL,
			IsPrimary:        true,
		}
		if err := tx.Create(&newBinding).Error; err != nil {
			tx.Rollback()
			zap.L().Error("create gitlab user binding for existing user failed", zap.Error(err))
			return nil, false, fmt.Errorf("创建用户绑定失败: %w", err)
		}
		// 更新现有用户的 GitLab 相关信息
		updates := map[string]interface{}{
			"gitlab_username": info.Username,
			"gitlab_email":    info.Email,
		}
		if existingUser.DisplayName == "" {
			updates["display_name"] = info.Name
		}
		if existingUser.AvatarURL == "" {
			updates["avatar_url"] = info.AvatarURL
		}
		// 如果 default_org_id 与当前实例所属组织不同，更新为当前实例组织
		if existingUser.DefaultOrgID != gitlabInstance.OrgID {
			updates["default_org_id"] = gitlabInstance.OrgID
		}
		tx.Model(&existingUser).Updates(updates)

		// 确保该用户在目标组织中有 org_users 记录（developer 角色）
		var ouCount int64
		tx.Model(&model.OrgUser{}).Where("org_id = ? AND user_id = ?", gitlabInstance.OrgID, existingUser.ID).Count(&ouCount)
		if ouCount == 0 {
			ou := model.OrgUser{
				OrgID:     gitlabInstance.OrgID,
				UserID:    existingUser.ID,
				Role:      "developer",
				Status:    "active",
				IsDefault: existingUser.DefaultOrgID == 0,
			}
			if err := tx.Create(&ou).Error; err != nil {
				tx.Rollback()
				zap.L().Error("create org_user for existing user failed", zap.Error(err))
				return nil, false, fmt.Errorf("创建组织用户关联失败: %w", err)
			}
		}

		tx.Commit()
		return &existingUser, false, nil
	}

	if !oc.AutoCreateUser {
		tx.Rollback()
		return nil, false, fmt.Errorf("用户不存在，请联系管理员绑定")
	}

	// 3. 自动创建用户
	newUser := model.User{
		Username:       info.Username,
		DisplayName:    info.Name,
		Role:           model.RoleUser,
		LoginType:      "gitlab",
		GitlabUserID:   func() *uint64 { v := uint64(info.ID); return &v }(),
		GitlabUsername: info.Username,
		GitlabEmail:    info.Email,
		AvatarURL:      info.AvatarURL,
	}

	if err := tx.Create(&newUser).Error; err != nil {
		tx.Rollback()
		zap.L().Error("create gitlab user failed", zap.Error(err))
		return nil, false, fmt.Errorf("创建用户失败: %w", err)
	}

	// 4. 创建 GitLab 实例绑定记录
	newBinding := model.GitLabUserBinding{
		UserID:           newUser.ID,
		GitLabInstanceID: instanceID,
		GitLabUserID:     uint(info.ID),
		GitLabUsername:   info.Username,
		GitLabEmail:      info.Email,
		GitLabAvatarURL:  info.AvatarURL,
		IsPrimary:        true,
	}
	if err := tx.Create(&newBinding).Error; err != nil {
		tx.Rollback()
		zap.L().Error("create gitlab user binding failed", zap.Error(err))
		return nil, false, fmt.Errorf("创建用户绑定失败: %w", err)
	}

	// 查询 GitLab 实例所属组织
	var inst model.GitLabInstance
	if err := tx.First(&inst, instanceID).Error; err != nil {
		tx.Rollback()
		zap.L().Error("query gitlab instance failed", zap.Error(err))
		return nil, false, fmt.Errorf("查询 GitLab 实例失败: %w", err)
	}
	targetOrgID := inst.OrgID
	if targetOrgID == 0 {
		targetOrgID = 1 // fallback 到根组织
	}

	// 5. 创建 org_user 关联（绑定到 GitLab 实例所属组织）
	orgUser := model.OrgUser{
		OrgID:     targetOrgID,
		UserID:    newUser.ID,
		Role:      "developer",
		IsDefault: true,
		Status:    "active",
	}
	if err := tx.Create(&orgUser).Error; err != nil {
		tx.Rollback()
		zap.L().Error("create org_user failed", zap.Error(err))
		return nil, false, fmt.Errorf("创建组织关联失败: %w", err)
	}

	// 6. 更新用户 default_org_id 为 GitLab 实例所属组织
	if err := tx.Model(&newUser).Update("default_org_id", targetOrgID).Error; err != nil {
		tx.Rollback()
		return nil, false, fmt.Errorf("更新用户默认组织失败: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, false, fmt.Errorf("事务提交失败: %w", err)
	}

	return &newUser, true, nil
}

// --- 自包含 HMAC State 管理 ---

// getOAuthStateSecret 获取 OAuth State 签名密钥（优先环境变量，fallback系统配置）
func getOAuthStateSecret() string {
	secret := os.Getenv("OAUTH_STATE_SECRET")
	if secret != "" {
		return secret
	}
	// fallback：使用 GitLab OAuth Client Secret 作为签名密钥
	var cfg model.SystemConfig
	if err := model.DB.First(&cfg).Error; err == nil && cfg.GitlabOAuthClientSecret != "" {
		return cfg.GitlabOAuthClientSecret
	}
	// 生产环境未配置密钥时 panic，防止使用可预测密钥
	panic("OAuth State Secret 未配置：必须设置 OAUTH_STATE_SECRET 环境变量或配置 GitLab OAuth Client Secret")
}

// GenerateSelfContainedState 生成自包含 HMAC State
// 格式: base64(inst_id|nonce|timestamp|signature)
func GenerateSelfContainedState(instanceID uint) string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	nonceStr := hex.EncodeToString(nonce)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	inst := strconv.FormatUint(uint64(instanceID), 10)

	payload := fmt.Sprintf("inst=%s&nonce=%s&ts=%s", inst, nonceStr, ts)
	mac := hmac.New(sha256.New, []byte(getOAuthStateSecret()))
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	state := base64.URLEncoding.EncodeToString([]byte(payload + "|sig=" + sig))
	return state
}

// ParseSelfContainedState 解析并验证自包含 HMAC State
// 返回 instanceID 和验证结果
func ParseSelfContainedState(state string) (uint, bool) {
	data, err := base64.URLEncoding.DecodeString(state)
	if err != nil {
		return 0, false
	}

	parts := strings.SplitN(string(data), "|sig=", 2)
	if len(parts) != 2 {
		return 0, false
	}
	payload, sig := parts[0], parts[1]

	// 验证签名
	mac := hmac.New(sha256.New, []byte(getOAuthStateSecret()))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return 0, false
	}

	// 解析参数
	values, err := url.ParseQuery(payload)
	if err != nil {
		return 0, false
	}

	// 检查时间戳是否过期（5分钟）
	tsStr := values.Get("ts")
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return 0, false
	}
	if time.Now().Unix()-ts > 300 {
		return 0, false // state 过期
	}

	instStr := values.Get("inst")
	instID, err := strconv.ParseUint(instStr, 10, 64)
	if err != nil {
		return 0, false
	}

	return uint(instID), true
}
