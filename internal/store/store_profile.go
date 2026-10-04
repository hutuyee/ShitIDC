package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// GetProfile returns the user's editable profile; missing rows come back as
// an empty profile so first-time visitors get a blank form, not a 404.
func (s *Store) GetProfile(ctx context.Context, userID int64) (model.UserProfile, error) {
	var p model.UserProfile
	err := s.DB.QueryRow(ctx, `SELECT nickname,real_name,company,phone,qq,country,province,city,address,updated_at
FROM user_profiles WHERE user_id=$1`, userID).
		Scan(&p.Nickname, &p.RealName, &p.Company, &p.Phone, &p.QQ, &p.Country, &p.Province, &p.City, &p.Address, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UserProfile{Country: "中国"}, nil
	}
	if err != nil {
		return model.UserProfile{}, err
	}
	return p, nil
}

// SaveProfile upserts the user's profile. Empty country is normalised to the
// default so the field never holds a meaningless blank.
func (s *Store) SaveProfile(ctx context.Context, userID int64, p model.UserProfile) error {
	p.Nickname = strings.TrimSpace(p.Nickname)
	p.RealName = strings.TrimSpace(p.RealName)
	p.Company = strings.TrimSpace(p.Company)
	p.Phone = strings.TrimSpace(p.Phone)
	p.QQ = strings.TrimSpace(p.QQ)
	p.Country = strings.TrimSpace(p.Country)
	p.Province = strings.TrimSpace(p.Province)
	p.City = strings.TrimSpace(p.City)
	p.Address = strings.TrimSpace(p.Address)
	if p.Country == "" {
		p.Country = "中国"
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO user_profiles(user_id,nickname,real_name,company,phone,qq,country,province,city,address,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now())
ON CONFLICT(user_id) DO UPDATE SET
 nickname=excluded.nickname,real_name=excluded.real_name,company=excluded.company,phone=excluded.phone,qq=excluded.qq,
 country=excluded.country,province=excluded.province,city=excluded.city,address=excluded.address,updated_at=now()`,
		userID, p.Nickname, p.RealName, p.Company, p.Phone, p.QQ, p.Country, p.Province, p.City, p.Address)
	return err
}
