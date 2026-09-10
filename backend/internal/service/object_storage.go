package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ai-optimizer/backend/internal/model"
	"github.com/ai-optimizer/backend/internal/service/pipeline"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ObjectStorage 统一接口
type ObjectStorage interface {
	Put(ctx context.Context, key string, data []byte) (string, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	PresignedGetURL(ctx context.Context, key string, expiry time.Duration) (string, error)
	CheckConnection() (bool, string)
}

// storageProvider 全局单例
var storageProvider ObjectStorage

// InitObjectStorageProvider 根据默认配置初始化存储后端
func InitObjectStorageProvider(scope *model.UserAuthScope) error {
	var cfg model.ObjectStorageConfig
	db := model.DBWithScope(scope)
	if err := db.Where("is_default = ?", true).First(&cfg).Error; err != nil {
		if err.Error() == "record not found" {
			storageProvider = &noopStorage{}
			pipeline.SetSnapshotStorage(nil) // 使用 pipeline 的 noop
			zap.L().Info("无默认对象存储配置，快照将存储在数据库")
			return nil
		}
		return fmt.Errorf("读取默认对象存储配置失败: %w", err)
	}

	if !cfg.Enabled {
		storageProvider = &noopStorage{}
		pipeline.SetSnapshotStorage(nil)
		zap.L().Info("默认对象存储配置已禁用，快照将存储在数据库")
		return nil
	}

	provider, err := BuildStorageFromConfig(cfg)
	if err != nil {
		return err
	}

	// 连接测试
	ok, msg := provider.CheckConnection()
	if !ok {
		storageProvider = &noopStorage{}
		pipeline.SetSnapshotStorage(nil)
		zap.L().Warn("对象存储连接测试失败，回退到数据库存储", zap.String("error", msg))
		return nil
	}

	storageProvider = provider
	// 同步设置 pipeline 的快照存储
	pipeline.SetSnapshotStorage(provider)
	zap.L().Info("对象存储初始化成功",
		zap.String("type", cfg.Type),
		zap.String("bucket", cfg.Bucket),
		zap.String("endpoint", cfg.Endpoint))
	return nil
}

// GetObjectStorageProvider 获取当前存储实例
func GetObjectStorageProvider() ObjectStorage {
	if storageProvider == nil {
		return &noopStorage{}
	}
	return storageProvider
}

// GetObjectStorageProviderForOrg 按组织ID获取对象存储 provider
// 当该组织没有默认配置时，fallback 到全局 provider
func GetObjectStorageProviderForOrg(orgID uint) (ObjectStorage, error) {
	if orgID == 0 {
		return GetObjectStorageProvider(), nil
	}
	var cfg model.ObjectStorageConfig
	if err := model.DB.Where("org_id = ? AND is_default = ?", orgID, true).First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return GetObjectStorageProvider(), nil
		}
		return nil, fmt.Errorf("查询组织对象存储配置失败: %w", err)
	}
	if !cfg.Enabled {
		return &noopStorage{}, nil
	}
	return BuildStorageFromConfig(cfg)
}

// ReloadObjectStorageProvider 重新加载默认对象存储配置（配置变更后调用）
func ReloadObjectStorageProvider(scope *model.UserAuthScope) error {
	zap.L().Info("重新加载对象存储配置")
	return InitObjectStorageProvider(scope)
}

// BuildStorageFromConfig 根据配置构建具体实例
func BuildStorageFromConfig(cfg model.ObjectStorageConfig) (ObjectStorage, error) {
	ak, err := DecryptString(cfg.AccessKey)
	if err != nil {
		return nil, fmt.Errorf("解密 AccessKey 失败: %w", err)
	}
	sk, err := DecryptString(cfg.SecretKey)
	if err != nil {
		return nil, fmt.Errorf("解密 SecretKey 失败: %w", err)
	}

	switch cfg.Type {
	case "cos", "s3", "minio", "oss":
		// 以上均支持 S3-compatible API
		return newS3CompatibleStorage(cfg, ak, sk), nil
	default:
		return nil, fmt.Errorf("不支持的存储类型: %s", cfg.Type)
	}
}

// ========== S3-Compatible REST Client (std-lib only) ==========

type s3CompatibleStorage struct {
	cfg    model.ObjectStorageConfig
	ak, sk string
	client *http.Client
}

func newS3CompatibleStorage(cfg model.ObjectStorageConfig, ak, sk string) *s3CompatibleStorage {
	// MinIO 默认 region 为 us-east-1（AWS S3 兼容要求）
	if cfg.Type == "minio" && cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return &s3CompatibleStorage{
		cfg:    cfg,
		ak:     ak,
		sk:     sk,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *s3CompatibleStorage) resolveEndpoint() string {
	if s.cfg.Endpoint != "" {
		scheme := "http"
		if s.cfg.UseSSL {
			scheme = "https"
		}
		endpoint := s.cfg.Endpoint
		// MinIO：如果用户没填端口，自动补全默认 API 端口 9000
		if s.cfg.Type == "minio" && !strings.Contains(endpoint, ":") {
			endpoint = endpoint + ":9000"
		}
		return fmt.Sprintf("%s://%s", scheme, endpoint)
	}
	// 根据类型和 bucket 构建默认 endpoint
	scheme := "https"
	if !s.cfg.UseSSL {
		scheme = "http"
	}
	switch s.cfg.Type {
	case "cos":
		return fmt.Sprintf("%s://%s.cos.%s.myqcloud.com", scheme, s.cfg.Bucket, s.cfg.Region)
	case "oss":
		return fmt.Sprintf("%s://%s.oss-%s.aliyuncs.com", scheme, s.cfg.Bucket, s.cfg.Region)
	case "s3":
		return fmt.Sprintf("%s://%s.s3.%s.amazonaws.com", scheme, s.cfg.Bucket, s.cfg.Region)
	case "minio":
		// MinIO 默认 API 端口 9000
		return fmt.Sprintf("%s://localhost:9000", scheme)
	default:
		return fmt.Sprintf("%s://%s.s3.%s.amazonaws.com", scheme, s.cfg.Bucket, s.cfg.Region)
	}
}

func (s *s3CompatibleStorage) fullKey(key string) string {
	return path.Join(s.cfg.Prefix, key)
}

func (s *s3CompatibleStorage) objectURL(key string) string {
	fk := s.fullKey(key)
	endpoint := s.resolveEndpoint()
	// MinIO 使用 path-style endpoint（http://host:9000），bucket 需要显式拼在 URL 路径中
	if s.cfg.Type == "minio" {
		return fmt.Sprintf("%s/%s/%s", endpoint, s.cfg.Bucket, fk)
	}
	// S3/OSS/COS 使用 virtual-hosted endpoint（http://bucket.host/），bucket 已在 host 中
	return fmt.Sprintf("%s/%s", endpoint, fk)
}

func (s *s3CompatibleStorage) Put(ctx context.Context, key string, data []byte) (string, error) {
	urlStr := s.objectURL(key)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, urlStr, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(data))

	if err := s.signRequest(req); err != nil {
		return "", err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("PUT 失败 status=%d body=%s", resp.StatusCode, string(body))
	}
	return urlStr, nil
}

func (s *s3CompatibleStorage) Get(ctx context.Context, key string) ([]byte, error) {
	urlStr := s.objectURL(key)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	if err := s.signRequest(req); err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET 失败 status=%d body=%s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

func (s *s3CompatibleStorage) Delete(ctx context.Context, key string) error {
	urlStr := s.objectURL(key)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, urlStr, nil)
	if err != nil {
		return err
	}
	if err := s.signRequest(req); err != nil {
		return err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DELETE 失败 status=%d body=%s", resp.StatusCode, string(body))
	}
	return nil
}

func (s *s3CompatibleStorage) PresignedGetURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	fk := s.fullKey(key)
	endpoint := s.resolveEndpoint()
	urlStr := fmt.Sprintf("%s/%s", endpoint, fk)

	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	q := u.Query()
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.ak+"/"+dateStamp+"/"+s.cfg.Region+"/s3/aws4_request")
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", int(expiry.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()

	// 构造 canonical request 计算签名
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	signedHeaders, signature := s.signatureV4(req, now, "")
	q.Set("X-Amz-SignedHeaders", signedHeaders)
	q.Set("X-Amz-Signature", signature)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

func (s *s3CompatibleStorage) CheckConnection() (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testKey := fmt.Sprintf(".connection-test/%d", time.Now().Unix())
	putURL, err := s.Put(ctx, testKey, []byte("ok"))
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "S3 API Requests must be made to API port") {
			msg += "（请确认 Endpoint 使用的是 MinIO API 端口，默认为 9000；Console/Web 管理端口不支持 S3 API）"
		}
		return false, "PUT 测试失败: " + msg
	}

	_, err = s.Get(ctx, testKey)
	if err != nil {
		return false, "GET 测试失败: " + err.Error()
	}

	_ = s.Delete(ctx, testKey)
	return true, putURL
}

// ========== AWS Signature Version 4 ==========

func (s *s3CompatibleStorage) signRequest(req *http.Request) error {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", amzDate)

	_, signature := s.signatureV4(req, now, "")
	credential := s.ak + "/" + now.Format("20060102") + "/" + s.cfg.Region + "/s3/aws4_request"
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s, SignedHeaders=host, Signature=%s",
		credential, signature)
	req.Header.Set("Authorization", authHeader)
	return nil
}

func (s *s3CompatibleStorage) signatureV4(req *http.Request, t time.Time, payloadHash string) (string, string) {
	if payloadHash == "" {
		payloadHash = emptyStringSHA256
	}
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")

	// canonical request
	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQueryString := req.URL.RawQuery
	canonicalHeaders := "host:" + req.URL.Host + "\n"
	signedHeaders := "host"

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// string to sign
	credScope := dateStamp + "/" + s.cfg.Region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credScope,
		hex.EncodeToString(sha256Sum([]byte(canonicalRequest))),
	}, "\n")

	// signing key
	signingKey := getSigningKey(s.sk, dateStamp, s.cfg.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	return signedHeaders, signature
}

func getSigningKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256Sum(data []byte) []byte {
	h := sha256.New()
	h.Write(data)
	return h.Sum(nil)
}

const emptyStringSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// sortedQuery 按 key 排序 query values（用于 canonical request）
func sortedQuery(v url.Values) string {
	if v == nil {
		return ""
	}
	var keys []string
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, val := range v[k] {
			parts = append(parts, fmt.Sprintf("%s=%s", k, val))
		}
	}
	return strings.Join(parts, "&")
}

// ========== Noop Storage ==========

type noopStorage struct{}

func (n *noopStorage) Put(ctx context.Context, key string, data []byte) (string, error) {
	return "", fmt.Errorf("object storage disabled")
}
func (n *noopStorage) Get(ctx context.Context, key string) ([]byte, error) {
	return nil, fmt.Errorf("object storage disabled")
}
func (n *noopStorage) Delete(ctx context.Context, key string) error { return nil }
func (n *noopStorage) PresignedGetURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return "", fmt.Errorf("object storage disabled")
}
func (n *noopStorage) CheckConnection() (bool, string) { return false, "对象存储未启用" }
