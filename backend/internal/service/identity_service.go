package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/LuckyKuang/sub2api-plus/internal/pkg/logger"
	"github.com/LuckyKuang/sub2api-plus/internal/pkg/outboundidentity"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Fingerprint represents account fingerprint data
type Fingerprint struct {
	ClientID                string
	UserAgent               string
	StainlessLang           string
	StainlessPackageVersion string
	StainlessOS             string
	StainlessArch           string
	StainlessRuntime        string
	StainlessRuntimeVersion string
	UpdatedAt               int64 `json:",omitempty"` // Unix timestamp，用于判断是否需要续期TTL
}

// IdentityCache defines cache operations for identity service
type IdentityCache interface {
	GetFingerprint(ctx context.Context, accountID int64) (*Fingerprint, error)
	SetFingerprint(ctx context.Context, accountID int64, fp *Fingerprint) error
	// GetMaskedSessionID 获取固定的会话ID（用于会话ID伪装功能）
	// 返回的 sessionID 是一个 UUID 格式的字符串
	// 如果不存在或已过期（15分钟无请求），返回空字符串
	GetMaskedSessionID(ctx context.Context, accountID int64) (string, error)
	// SetMaskedSessionID 设置固定的会话ID，TTL 为 15 分钟
	// 每次调用都会刷新 TTL
	SetMaskedSessionID(ctx context.Context, accountID int64, sessionID string) error
}

// ClaudeCodeDeviceStore persists one private Claude Code device ID per
// Anthropic OAuth or setup-token account. The candidate is stored only when the
// account has none; the stored device ID is returned in both cases.
type ClaudeCodeDeviceStore interface {
	GetOrCreateClaudeCodeDeviceID(ctx context.Context, accountID int64, candidate string) (string, error)
}

// IdentityService 管理OAuth账号的请求身份指纹
type IdentityService struct {
	cache   IdentityCache
	devices ClaudeCodeDeviceStore
	// deviceIDs memoizes persisted device IDs, which never change once stored.
	deviceIDs sync.Map
}

// NewIdentityService 创建新的IdentityService
func NewIdentityService(cache IdentityCache, devices ClaudeCodeDeviceStore) *IdentityService {
	return &IdentityService{cache: cache, devices: devices}
}

// GetOrCreateFingerprint retains the account device ID while replacing cached
// client declarations with the trusted outbound identity. Caller headers are
// intentionally ignored, including on cache creation and version changes.
func (s *IdentityService) GetOrCreateFingerprint(ctx context.Context, account *Account, _ http.Header) (*Fingerprint, error) {
	if account == nil {
		return nil, errors.New("fingerprint requires an account")
	}
	identity, ok := outboundidentity.Default(ctx, "claude")
	if !ok {
		identity = builtInOutboundIdentity("claude")
	}
	headers := http.Header{}
	identity.Apply(headers)
	fp := &Fingerprint{
		UserAgent:               identity.UserAgent,
		StainlessLang:           headers.Get("X-Stainless-Lang"),
		StainlessPackageVersion: headers.Get("X-Stainless-Package-Version"),
		StainlessOS:             headers.Get("X-Stainless-OS"),
		StainlessArch:           headers.Get("X-Stainless-Arch"),
		StainlessRuntime:        headers.Get("X-Stainless-Runtime"),
		StainlessRuntimeVersion: headers.Get("X-Stainless-Runtime-Version"),
	}
	cached, err := s.cache.GetFingerprint(ctx, account.ID)
	if err != nil {
		cached = nil
	}
	if cached != nil {
		fp.ClientID = cached.ClientID
		fp.UpdatedAt = cached.UpdatedAt
	}
	fp.ClientID = s.accountDeviceID(ctx, account, fp.ClientID)
	if cached == nil || *fp != *cached || time.Since(time.Unix(fp.UpdatedAt, 0)) > 24*time.Hour {
		fp.UpdatedAt = time.Now().Unix()
		if err := s.cache.SetFingerprint(ctx, account.ID, fp); err != nil {
			logger.LegacyPrintf("service.identity", "Failed to persist account device identity: account_id=%d", account.ID)
		}
	}
	// A configured device applies to outbound requests only. The cache and store
	// keep the persisted device, so clearing the override restores it.
	if override := account.ClaudeCodeDeviceOverride(); override != "" {
		fp.ClientID = override
	}
	return fp, nil
}

// accountDeviceID returns the account's Claude Code device ID. Anthropic OAuth
// and setup-token accounts persist it so cache eviction and restarts keep the
// same device; the first stored value adopts a valid cached ID so an upgrade
// does not rotate active accounts. Other accounts keep the cache-only ID.
func (s *IdentityService) accountDeviceID(ctx context.Context, account *Account, cachedID string) string {
	if s.devices == nil || !account.IsAnthropicOAuthOrSetupToken() {
		if cachedID != "" {
			return cachedID
		}
		return generateClientID()
	}
	if memo, ok := s.deviceIDs.Load(account.ID); ok {
		if stored, ok := memo.(string); ok {
			return stored
		}
	}
	candidate := cachedID
	if !isClaudeCodeDeviceID(candidate) {
		candidate = generateClientID()
	}
	stored, err := s.devices.GetOrCreateClaudeCodeDeviceID(ctx, account.ID, candidate)
	if err == nil && !isClaudeCodeDeviceID(stored) {
		err = errors.New("stored device ID is malformed")
	}
	if err != nil {
		// Serve the cached device for now; the next request retries persistence.
		logger.LegacyPrintf("service.identity", "Failed to resolve persistent Claude Code device identity: account_id=%d err=%v", account.ID, err)
		if cachedID != "" {
			return cachedID
		}
		return candidate
	}
	s.deviceIDs.Store(account.ID, stored)
	return stored
}

// ApplyFingerprint 将指纹应用到请求头（覆盖原有的x-stainless-*头）
// 使用 setHeaderRaw 保持原始大小写（如 X-Stainless-OS 而非 X-Stainless-Os）
func (s *IdentityService) ApplyFingerprint(req *http.Request, fp *Fingerprint) {
	if fp == nil {
		return
	}

	// 设置user-agent
	if fp.UserAgent != "" {
		setHeaderRaw(req.Header, "User-Agent", fp.UserAgent)
	}

	// 设置x-stainless-*头（保持与 claude.DefaultHeaders 一致的大小写）
	if fp.StainlessLang != "" {
		setHeaderRaw(req.Header, "X-Stainless-Lang", fp.StainlessLang)
	}
	if fp.StainlessPackageVersion != "" {
		setHeaderRaw(req.Header, "X-Stainless-Package-Version", fp.StainlessPackageVersion)
	}
	if fp.StainlessOS != "" {
		setHeaderRaw(req.Header, "X-Stainless-OS", fp.StainlessOS)
	}
	if fp.StainlessArch != "" {
		setHeaderRaw(req.Header, "X-Stainless-Arch", fp.StainlessArch)
	}
	if fp.StainlessRuntime != "" {
		setHeaderRaw(req.Header, "X-Stainless-Runtime", fp.StainlessRuntime)
	}
	if fp.StainlessRuntimeVersion != "" {
		setHeaderRaw(req.Header, "X-Stainless-Runtime-Version", fp.StainlessRuntimeVersion)
	}
}

// RewriteUserID 重写body中的metadata.user_id
// 支持旧拼接格式和新 JSON 格式的 user_id 解析，
// 根据 fingerprintUA 版本选择输出格式。
//
// 重要：此函数使用 json.RawMessage 保留其他字段的原始字节，
// 避免重新序列化导致 thinking 块等内容被修改。
func (s *IdentityService) RewriteUserID(body []byte, accountID int64, accountUUID, cachedClientID, fingerprintUA string) ([]byte, error) {
	if len(body) == 0 || accountUUID == "" || cachedClientID == "" {
		return body, nil
	}

	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return body, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return body, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return body, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return body, nil
	}

	// 解析 user_id（兼容旧拼接格式和新 JSON 格式）
	parsed := ParseMetadataUserID(userID)
	if parsed == nil {
		return body, nil
	}

	sessionTail := parsed.SessionID // 原始session UUID

	// 生成新的session hash: SHA256(accountID::sessionTail) -> UUID格式
	seed := fmt.Sprintf("%d::%s", accountID, sessionTail)
	newSessionHash := generateUUIDFromSeed(seed)

	// 根据客户端版本选择输出格式
	version := ExtractCLIVersion(fingerprintUA)
	newUserID := FormatMetadataUserID(cachedClientID, accountUUID, newSessionHash, version)
	if newUserID == userID {
		return body, nil
	}

	newBody, err := sjson.SetBytes(body, "metadata.user_id", newUserID)
	if err != nil {
		return body, nil
	}
	return newBody, nil
}

// RewriteUserIDWithMasking 重写body中的metadata.user_id，支持会话ID伪装
// 如果账号启用了会话ID伪装（session_id_masking_enabled），
// 则在完成常规重写后，将 session 部分替换为固定的伪装ID（15分钟内保持不变）
//
// 重要：此函数使用 json.RawMessage 保留其他字段的原始字节，
// 避免重新序列化导致 thinking 块等内容被修改。
func (s *IdentityService) RewriteUserIDWithMasking(ctx context.Context, body []byte, account *Account, accountUUID, cachedClientID, fingerprintUA string) ([]byte, error) {
	// 先执行常规的 RewriteUserID 逻辑
	newBody, err := s.RewriteUserID(body, account.ID, accountUUID, cachedClientID, fingerprintUA)
	if err != nil {
		return newBody, err
	}

	// 检查是否启用会话ID伪装
	if !account.IsSessionIDMaskingEnabled() {
		return newBody, nil
	}

	metadata := gjson.GetBytes(newBody, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return newBody, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return newBody, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return newBody, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return newBody, nil
	}

	// 解析已重写的 user_id
	uidParsed := ParseMetadataUserID(userID)
	if uidParsed == nil {
		return newBody, nil
	}

	// 获取或生成固定的伪装 session ID
	maskedSessionID, err := s.cache.GetMaskedSessionID(ctx, account.ID)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to get masked session ID for account %d: %v", account.ID, err)
		return newBody, nil
	}

	if maskedSessionID == "" {
		// 首次或已过期，生成新的伪装 session ID
		maskedSessionID = generateRandomUUID()
		logger.LegacyPrintf("service.identity", "Generated new masked session ID for account %d: %s", account.ID, maskedSessionID)
	}

	// 刷新 TTL（每次请求都刷新，保持 15 分钟有效期）
	if err := s.cache.SetMaskedSessionID(ctx, account.ID, maskedSessionID); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to set masked session ID for account %d: %v", account.ID, err)
	}

	// 用 FormatMetadataUserID 重建（保持与 RewriteUserID 相同的格式）
	version := ExtractCLIVersion(fingerprintUA)
	newUserID := FormatMetadataUserID(uidParsed.DeviceID, uidParsed.AccountUUID, maskedSessionID, version)

	slog.Debug("session_id_masking_applied",
		"account_id", account.ID,
		"before", userID,
		"after", newUserID,
	)

	if newUserID == userID {
		return newBody, nil
	}

	maskedBody, setErr := sjson.SetBytes(newBody, "metadata.user_id", newUserID)
	if setErr != nil {
		return newBody, nil
	}
	return maskedBody, nil
}

// generateRandomUUID 生成随机 UUID v4 格式字符串
func generateRandomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// fallback: 使用时间戳生成
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		b = h[:16]
	}

	// 设置 UUID v4 版本和变体位
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// generateClientID 生成64位十六进制客户端ID（32字节随机数）
func generateClientID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// 极罕见的情况，使用时间戳+固定值作为fallback
		logger.LegacyPrintf("service.identity", "Warning: crypto/rand.Read failed: %v, using fallback", err)
		// 使用SHA256(当前纳秒时间)作为fallback
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(h[:])
	}
	return hex.EncodeToString(b)
}

// isClaudeCodeDeviceID reports whether id has the generated device ID format:
// 64 lowercase hexadecimal characters.
func isClaudeCodeDeviceID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// generateUUIDFromSeed 从种子生成确定性UUID v4格式字符串
func generateUUIDFromSeed(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]

	// 设置UUID v4版本和变体位
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
