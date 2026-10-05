package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Generic system_settings helpers for the V2 feature set. Each concern gets
// its own JSON key (第一阶段: one config per concern), mirroring the mail key.

// settingGet loads one settings blob into out; missing key leaves the zero
// value and returns ErrNotFound.
func (s *Store) settingGet(ctx context.Context, key string, out any) error {
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM system_settings WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (s *Store) settingSave(ctx context.Context, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO system_settings(key,value,updated_at) VALUES($1,$2,now())
ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=now()`, key, b)
	return err
}

// ---- referral settings (推广系统) ----

type ReferralSettings struct {
	Enabled bool `json:"enabled"`
	Percent int  `json:"percent"` // commission percent of the paid order total
}

func (s *Store) GetReferralSettings(ctx context.Context) (ReferralSettings, error) {
	out := ReferralSettings{}
	err := s.settingGet(ctx, "referral", &out)
	if errors.Is(err, ErrNotFound) {
		return ReferralSettings{}, nil
	}
	return out, err
}

func (s *Store) SaveReferralSettings(ctx context.Context, settings ReferralSettings) error {
	if settings.Percent < 0 {
		settings.Percent = 0
	}
	if settings.Percent > 50 {
		settings.Percent = 50
	}
	return s.settingSave(ctx, "referral", settings)
}

// ---- branding (多品牌) ----

type BrandingSettings struct {
	SiteName     string `json:"site_name"`
	LogoURL      string `json:"logo_url"`
	PrimaryColor string `json:"primary_color"`
}

func (s *Store) GetBranding(ctx context.Context) (BrandingSettings, error) {
	out := BrandingSettings{}
	err := s.settingGet(ctx, "branding", &out)
	if errors.Is(err, ErrNotFound) {
		return BrandingSettings{}, nil
	}
	return out, err
}

func (s *Store) SaveBranding(ctx context.Context, settings BrandingSettings) error {
	return s.settingSave(ctx, "branding", settings)
}

// ---- storefront / risk settings ----

type StorefrontSettings struct {
	ActiveTheme      string `json:"active_theme"`
	BaseCurrency     string `json:"base_currency"`
	MaxOrdersPerHour int    `json:"max_orders_per_hour"` // 0 = default (10)
}

func (s *Store) GetStorefrontSettings(ctx context.Context) (StorefrontSettings, error) {
	out := StorefrontSettings{}
	err := s.settingGet(ctx, "storefront", &out)
	if errors.Is(err, ErrNotFound) {
		return StorefrontSettings{}, nil
	}
	return out, err
}

func (s *Store) SaveStorefrontSettings(ctx context.Context, settings StorefrontSettings) error {
	return s.settingSave(ctx, "storefront", settings)
}

// ---- marketplace settings (插件市场) ----

// MarketplaceSettings 控制市场索引来源。
type MarketplaceSettings struct {
	// IndexURL 为空时用内置默认索引（见 internal/marketplace.DefaultIndexURL）。
	IndexURL string `json:"index_url"`
}

// GetMarketplaceSettings 读取市场设置；没有保存过时返回零值。
func (s *Store) GetMarketplaceSettings(ctx context.Context) (MarketplaceSettings, error) {
	var v MarketplaceSettings
	err := s.settingGet(ctx, "marketplace", &v)
	if errors.Is(err, ErrNotFound) {
		return MarketplaceSettings{}, nil
	}
	return v, err
}

// SaveMarketplaceSettings 保存市场设置。
func (s *Store) SaveMarketplaceSettings(ctx context.Context, v MarketplaceSettings) error {
	return s.settingSave(ctx, "marketplace", v)
}
