package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"

	"cmd2api/ent"
	"cmd2api/ent/adminkey"
	"cmd2api/internal/domain"
	"cmd2api/internal/middleware"
)

var (
	// ErrInvalidAdminKeyName 表示管理密钥名称为空。
	ErrInvalidAdminKeyName = errors.New("管理密钥名称不能为空")
	// ErrInvalidAdminKeyScope 表示管理密钥权限范围不合法。
	ErrInvalidAdminKeyScope = errors.New("管理密钥权限范围无效，仅支持 full、push_accounts 或 readonly")
	// ErrInvalidAdminKeyStatus 表示管理密钥状态不合法。
	ErrInvalidAdminKeyStatus = errors.New("管理密钥状态无效，仅支持 active 或 disabled")
	// ErrInvalidAllowedIPs 表示白名单中包含非法 IP 或 CIDR。
	ErrInvalidAllowedIPs = errors.New("IP 白名单包含非法 IP 或 CIDR")
	// ErrAdminKeyNotFound 表示指定的管理密钥不存在。
	ErrAdminKeyNotFound = errors.New("管理密钥不存在")
)

// CreateAdminKeyInput 支持完整的管理密钥创建参数（兼容 expiresInDays 与显式 expiresAt）。
type CreateAdminKeyInput struct {
	Name          string
	Scopes        string
	AllowedIPs    string
	ExpiresInDays int
	ExpiresAt     *time.Time
	Status        string
}

// UpdateAdminKeyInput 支持局部更新管理密钥元数据、白名单、过期时间与启停状态。
type UpdateAdminKeyInput struct {
	Name        *string
	Scopes      *string
	AllowedIPs  *string
	Status      *string
	ExpiresAt   *time.Time
	ClearExpiry bool
}

// AdminKeyService 负责远程管理密钥（adm-...）的签发、校验、更新与吊销。
type AdminKeyService struct {
	db *ent.Client
}

// NewAdminKeyService 构造 AdminKeyService。
func NewAdminKeyService(db *ent.Client) *AdminKeyService {
	return &AdminKeyService{db: db}
}

// List 按 ID 倒序返回全部已签发的远程管理密钥（仅含前缀与元数据，不含明文）。
func (s *AdminKeyService) List(ctx context.Context) ([]*ent.AdminKey, error) {
	rows, err := s.db.AdminKey.Query().
		Order(ent.Desc(adminkey.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询管理密钥列表失败: %w", err)
	}
	return rows, nil
}

// Create 生成并持久化一把新的远程管理密钥，仅在创建时一次性返回明文密钥 plainKey。
func (s *AdminKeyService) Create(
	ctx context.Context,
	name, scopes, allowedIPs string,
	expiresInDays int,
) (plainKey string, row *ent.AdminKey, err error) {
	return s.CreateWithInput(ctx, CreateAdminKeyInput{
		Name:          name,
		Scopes:        scopes,
		AllowedIPs:    allowedIPs,
		ExpiresInDays: expiresInDays,
	})
}

// CreateWithInput 支持通过结构化入参（含显式 ExpiresAt / Status）创建管理密钥。
func (s *AdminKeyService) CreateWithInput(
	ctx context.Context,
	in CreateAdminKeyInput,
) (plainKey string, row *ent.AdminKey, err error) {
	cleanName := strings.TrimSpace(in.Name)
	if cleanName == "" {
		return "", nil, ErrInvalidAdminKeyName
	}

	normScope, err := normalizeAdminKeyScope(in.Scopes)
	if err != nil {
		return "", nil, err
	}

	normIPs, err := normalizeAndValidateAllowedIPs(in.AllowedIPs)
	if err != nil {
		return "", nil, err
	}

	normStatus := domain.StatusActive
	if strings.TrimSpace(in.Status) != "" {
		normStatus, err = normalizeAdminKeyStatus(in.Status)
		if err != nil {
			return "", nil, err
		}
	}

	plain, prefix, hash, err := middleware.GenerateAdminKey()
	if err != nil {
		return "", nil, err
	}

	builder := s.db.AdminKey.Create().
		SetName(cleanName).
		SetKeyPrefix(prefix).
		SetKeyHash(hash).
		SetScopes(normScope).
		SetAllowedIps(normIPs).
		SetStatus(normStatus)

	if in.ExpiresAt != nil {
		builder = builder.SetExpiresAt(*in.ExpiresAt)
	} else if in.ExpiresInDays > 0 {
		exp := time.Now().UTC().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		builder = builder.SetExpiresAt(exp)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("保存管理密钥失败: %w", err)
	}
	return plain, created, nil
}

// Update 局部更新指定管理密钥的名称、权限范围、IP 白名单、有效期或启停状态。
func (s *AdminKeyService) Update(ctx context.Context, id int64, in UpdateAdminKeyInput) (*ent.AdminKey, error) {
	existing, err := s.db.AdminKey.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAdminKeyNotFound
		}
		return nil, fmt.Errorf("查询管理密钥失败: %w", err)
	}

	upd := existing.Update()

	if in.Name != nil {
		cleanName := strings.TrimSpace(*in.Name)
		if cleanName == "" {
			return nil, ErrInvalidAdminKeyName
		}
		upd = upd.SetName(cleanName)
	}
	if in.Scopes != nil {
		normScope, sErr := normalizeAdminKeyScope(*in.Scopes)
		if sErr != nil {
			return nil, sErr
		}
		upd = upd.SetScopes(normScope)
	}
	if in.AllowedIPs != nil {
		normIPs, ipErr := normalizeAndValidateAllowedIPs(*in.AllowedIPs)
		if ipErr != nil {
			return nil, ipErr
		}
		upd = upd.SetAllowedIps(normIPs)
	}
	if in.Status != nil {
		normStatus, stErr := normalizeAdminKeyStatus(*in.Status)
		if stErr != nil {
			return nil, stErr
		}
		upd = upd.SetStatus(normStatus)
	}
	if in.ClearExpiry {
		upd = upd.ClearExpiresAt()
	} else if in.ExpiresAt != nil {
		upd = upd.SetExpiresAt(*in.ExpiresAt)
	}

	updated, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("更新管理密钥失败: %w", err)
	}
	return updated, nil
}

// Delete 永久吊销并删除指定的管理密钥。
func (s *AdminKeyService) Delete(ctx context.Context, id int64) error {
	if err := s.db.AdminKey.DeleteOneID(id).Exec(ctx); err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminKeyNotFound
		}
		return fmt.Errorf("删除管理密钥失败: %w", err)
	}
	return nil
}

func normalizeAdminKeyScope(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", domain.AdminKeyScopeFull:
		return domain.AdminKeyScopeFull, nil
	case domain.AdminKeyScopeReadOnly, "read_only", "read-only":
		return domain.AdminKeyScopeReadOnly, nil
	case domain.AdminKeyScopePushAccounts, "push", "push-accounts", "import", "replenish":
		return domain.AdminKeyScopePushAccounts, nil
	default:
		return "", ErrInvalidAdminKeyScope
	}
}

func normalizeAdminKeyStatus(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case domain.StatusActive:
		return domain.StatusActive, nil
	case domain.StatusDisabled:
		return domain.StatusDisabled, nil
	default:
		return "", ErrInvalidAdminKeyStatus
	}
}

func normalizeAndValidateAllowedIPs(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	parts := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
	if len(parts) == 0 {
		return "", nil
	}

	normalized := make([]string, 0, len(parts))
	for _, entry := range parts {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			_, cidr, err := net.ParseCIDR(entry)
			if err != nil || cidr == nil {
				return "", fmt.Errorf("%w: %q", ErrInvalidAllowedIPs, entry)
			}
			normalized = append(normalized, cidr.String())
			continue
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			return "", fmt.Errorf("%w: %q", ErrInvalidAllowedIPs, entry)
		}
		normalized = append(normalized, ip.String())
	}
	return strings.Join(normalized, ", "), nil
}
