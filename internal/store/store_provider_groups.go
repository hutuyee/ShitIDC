package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// 接口分组与容量分配（对应魔方 shd_server_groups）。
//
// 商品绑定到分组后，开通时由核心按策略挑一个组内接口。挑选逻辑抽成纯函数
// pickProvider，便于对「满了怎么办」「权重怎么起作用」这类边界写测试。

// 分组策略。
const (
	StrategyLeastLoaded = "least_loaded"
	StrategyFillFirst   = "fill_first"
	StrategyRoundRobin  = "round_robin"
)

// ProviderGroup 是一个接口分组。
type ProviderGroup struct {
	PublicID    string `json:"id"`
	Name        string `json:"name"`
	Strategy    string `json:"strategy"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	Members     int    `json:"members"`
	CreatedAt   string `json:"created_at"`
}

// GroupMember 是分组里的一个接口及其容量状况。
type GroupMember struct {
	ProviderID   int64  `json:"-"`
	PublicID     string `json:"provider_id"`
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"`
	Active       bool   `json:"active"`
	MaxServices  int    `json:"max_services"`
	Weight       int    `json:"weight"`
	// Current 是该接口当前承载的服务数（不含已终止/失败的）。
	Current int `json:"current"`
	// Available 是剩余容量；MaxServices=0 时恒为 -1（不限）。
	Available int `json:"available"`
}

// hasRoom 报告这个接口还能不能再开一台。
func (m GroupMember) hasRoom() bool {
	if !m.Active {
		return false
	}
	if m.MaxServices <= 0 {
		return true
	}
	return m.Current < m.MaxServices
}

// pickProvider 按策略从分组里挑一个接口。返回 -1 表示组里没有可用接口。
//
// 纯函数：不查库、不改状态，所以可以直接对着表驱动测试。
func pickProvider(strategy string, members []GroupMember) int64 {
	usable := make([]GroupMember, 0, len(members))
	for _, m := range members {
		if m.hasRoom() {
			usable = append(usable, m)
		}
	}
	if len(usable) == 0 {
		return -1
	}
	// 统一按 权重降序 → 当前负载升序 → ProviderID 升序 排序，保证结果稳定；
	// 同一组数据每次挑到同一个接口，排查问题时不会因为顺序抖动而困惑。
	sort.SliceStable(usable, func(i, j int) bool {
		if usable[i].Weight != usable[j].Weight {
			return usable[i].Weight > usable[j].Weight
		}
		if usable[i].Current != usable[j].Current {
			return usable[i].Current < usable[j].Current
		}
		return usable[i].ProviderID < usable[j].ProviderID
	})

	switch strategy {
	case StrategyFillFirst:
		// 凑满一个再下一个：优先挑「已经装了东西但还没满」的接口，
		// 全都空着时挑第一个。
		for _, m := range usable {
			if m.Current > 0 {
				return m.ProviderID
			}
		}
		return usable[0].ProviderID
	case StrategyRoundRobin:
		// 轮转：负载最少的那个自然就是「下一个」，与 least_loaded 结果一致，
		// 但这里显式取模以便将来支持外部传序号。
		total := 0
		for _, m := range usable {
			total += m.Current
		}
		idx := total % len(usable)
		return usable[idx].ProviderID
	default: // least_loaded
		// 排序后第一个就是负载最小、权重最高的那个。
		for _, m := range usable {
			if m.MaxServices <= 0 {
				return m.ProviderID
			}
		}
		return usable[0].ProviderID
	}
}

// normalizeStrategy 校验并归一化策略名。
func normalizeStrategy(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return StrategyLeastLoaded, nil
	}
	switch s {
	case StrategyLeastLoaded, StrategyFillFirst, StrategyRoundRobin:
		return s, nil
	}
	return "", fmt.Errorf("分组策略仅支持 least_loaded / fill_first / round_robin")
}

// ---- 分组 CRUD ----

// CreateProviderGroup 新建一个接口分组。
func (s *Store) CreateProviderGroup(ctx context.Context, name, strategy, description string) (ProviderGroup, error) {
	strategy, err := normalizeStrategy(strategy)
	if err != nil {
		return ProviderGroup{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ProviderGroup{}, fmt.Errorf("分组名称不能为空")
	}
	var g ProviderGroup
	var created string
	err = s.DB.QueryRow(ctx, `INSERT INTO provider_groups(name,strategy,description) VALUES($1,$2,$3)
RETURNING public_id::text,name,strategy,description,active,created_at::text`,
		name, strategy, strings.TrimSpace(description)).
		Scan(&g.PublicID, &g.Name, &g.Strategy, &g.Description, &g.Active, &created)
	if err != nil {
		if isUniqueViolation(err) {
			return ProviderGroup{}, fmt.Errorf("分组名称已存在")
		}
		return ProviderGroup{}, err
	}
	g.CreatedAt = created
	return g, nil
}

// ListProviderGroups 列出全部分组及其成员数。
func (s *Store) ListProviderGroups(ctx context.Context) ([]ProviderGroup, error) {
	rows, err := s.DB.Query(ctx, `SELECT g.public_id::text,g.name,g.strategy,g.description,g.active,g.created_at::text,count(m.id)
FROM provider_groups g LEFT JOIN provider_group_members m ON m.group_id=g.id
GROUP BY g.id ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderGroup{}
	for rows.Next() {
		var g ProviderGroup
		if err := rows.Scan(&g.PublicID, &g.Name, &g.Strategy, &g.Description, &g.Active, &g.CreatedAt, &g.Members); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeleteProviderGroup 删除分组；商品上的引用会被置空。
func (s *Store) DeleteProviderGroup(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM provider_groups WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddProviderToGroup 把一个接口加入分组（或更新它的容量与权重）。
func (s *Store) AddProviderToGroup(ctx context.Context, groupPublicID, providerPublicID string, maxServices, weight int) error {
	if maxServices < 0 {
		return fmt.Errorf("容量上限不能为负数")
	}
	tag, err := s.DB.Exec(ctx, `INSERT INTO provider_group_members(group_id,provider_id,max_services,weight)
SELECT g.id,p.id,$3,$4 FROM provider_groups g, providers p WHERE g.public_id=$1 AND p.public_id=$2
ON CONFLICT (group_id,provider_id) DO UPDATE SET max_services=excluded.max_services,weight=excluded.weight`,
		groupPublicID, providerPublicID, maxServices, weight)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveProviderFromGroup 把一个接口移出分组。
func (s *Store) RemoveProviderFromGroup(ctx context.Context, groupPublicID, providerPublicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM provider_group_members m
USING provider_groups g, providers p
WHERE m.group_id=g.id AND m.provider_id=p.id AND g.public_id=$1 AND p.public_id=$2`,
		groupPublicID, providerPublicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListGroupMembers 列出分组内的接口及其当前负载。
// Current 只统计「还活着」的服务（active/suspended/pending/provisioning），
// 已终止或失败的不占容量，否则接口迟早会因为历史服务而被判定为满。
func (s *Store) ListGroupMembers(ctx context.Context, groupPublicID string) ([]GroupMember, error) {
	rows, err := s.DB.Query(ctx, `SELECT m.provider_id,p.public_id::text,p.name,p.provider_type,p.active,m.max_services,m.weight,
  (SELECT count(*) FROM services sv WHERE sv.provider_id=p.id AND sv.status NOT IN ('terminated','failed')) AS current
FROM provider_group_members m
JOIN provider_groups g ON g.id=m.group_id
JOIN providers p ON p.id=m.provider_id
WHERE g.public_id=$1
ORDER BY m.weight DESC, p.id`, groupPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupMember{}
	for rows.Next() {
		var m GroupMember
		if err := rows.Scan(&m.ProviderID, &m.PublicID, &m.Name, &m.ProviderType, &m.Active, &m.MaxServices, &m.Weight, &m.Current); err != nil {
			return nil, err
		}
		if m.MaxServices > 0 {
			m.Available = m.MaxServices - m.Current
			if m.Available < 0 {
				m.Available = 0
			}
		} else {
			m.Available = -1
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ResolveProviderForGroup 按分组策略挑一个接口，返回其在 providers 表里的内部 ID。
// 组里没有可用接口时返回 ErrNotFound，调用方据此把服务标记为失败并提示扩容。
func (s *Store) ResolveProviderForGroup(ctx context.Context, groupPublicID string) (int64, error) {
	var strategy string
	err := s.DB.QueryRow(ctx, `SELECT strategy FROM provider_groups WHERE public_id=$1 AND active=true`, groupPublicID).Scan(&strategy)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	members, err := s.ListGroupMembers(ctx, groupPublicID)
	if err != nil {
		return 0, err
	}
	id := pickProvider(strategy, members)
	if id < 0 {
		return 0, ErrNotFound
	}
	return id, nil
}
