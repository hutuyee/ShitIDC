package model

import "time"

type User struct {
	ID            int64     `json:"uid"`
	PublicID      string    `json:"id"`
	Email         string    `json:"email"`
	Status        string    `json:"status"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
}

// UserProfile is the self-service contact/detail info a user can edit
// in the 个人中心 (nickname, phone, QQ, billing address, ...).
type UserProfile struct {
	Nickname  string    `json:"nickname"`
	RealName  string    `json:"real_name"`
	Company   string    `json:"company"`
	Phone     string    `json:"phone"`
	QQ        string    `json:"qq"`
	Country   string    `json:"country"`
	Province  string    `json:"province"`
	City      string    `json:"city"`
	Address   string    `json:"address"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Product struct {
	ID           int64  `json:"-"`
	PublicID     string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	ProviderID   string `json:"provider_id,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	ProviderType string `json:"provider_type"`
	Active       bool   `json:"active"`
	PriceCents   int64  `json:"price_cents"`
	Currency     string `json:"currency"`
	BillingCycle string `json:"billing_cycle"`
	GroupID      string `json:"group_id,omitempty"`
	GroupName    string `json:"group_name,omitempty"`
	SortWeight   int    `json:"sort_weight,omitempty"`
	// 库存与限购（魔方 stock_control / qty / allow_qty / maximum_customer_purchase_quantity）
	StockControl   bool `json:"stock_control"`
	StockQty       int  `json:"stock_qty"`
	SoldCount      int  `json:"sold_count"`
	AllowQty       bool `json:"allow_qty"`
	MaxPerCustomer int  `json:"max_per_customer"`
	IsFeatured     bool `json:"is_featured"`
	// Available 是可售数量：未开启库存控制时为 -1（不限）。
	Available int `json:"available"`
	// 计费类型（魔方 pay_type）：recurring 周期 / onetime 一次性 / free 免费 / trial 试用
	PayType string `json:"pay_type"`
	// 试用配置：pay_type=trial 时生效
	TrialDays       int   `json:"trial_days"`
	TrialPriceCents int64 `json:"trial_price_cents"`
	// 开通后 N 天自动删除（魔方 auto_terminate_days），0 = 不自动删除
	AutoTerminateDays int `json:"auto_terminate_days"`
	// Prices 是该商品的全部在售价格档（管理端编辑器用于回填多周期价格）。
	Prices    []ProductPrice `json:"prices,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// ConfigOption 是商品可配置项（魔方 product_config_options）。
// OptionType: 1=下拉 2=单选 3=开关 4=数量。
type ConfigOption struct {
	PublicID    string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	OptionType  int                 `json:"option_type"`
	Required    bool                `json:"required"`
	SortWeight  int                 `json:"sort_weight"`
	QtyMin      int                 `json:"qty_min"`
	QtyMax      int                 `json:"qty_max"`
	Values      []ConfigOptionValue `json:"values"`
}

// ConfigOptionValue 是配置项的一个候选项，price_cents 是相对商品基础价的加价。
type ConfigOptionValue struct {
	PublicID   string `json:"id"`
	Label      string `json:"label"`
	PriceCents int64  `json:"price_cents"`
	SetupCents int64  `json:"setup_cents"`
	IsDefault  bool   `json:"is_default"`
	Hidden     bool   `json:"hidden"`
	SortWeight int    `json:"sort_weight"`
}

// ConfigSelection 是买家在配置项上的选择，存进订单明细并随开通请求下发。
type ConfigSelection struct {
	OptionID   string `json:"option_id"`
	OptionName string `json:"option_name"`
	ValueID    string `json:"value_id,omitempty"`
	ValueLabel string `json:"value_label,omitempty"`
	Quantity   int    `json:"quantity,omitempty"`
	PriceCents int64  `json:"price_cents"`
}

// ProductCustomField 是商品自定义字段（魔方 customfields，type=product）。
type ProductCustomField struct {
	PublicID    string   `json:"id"`
	Name        string   `json:"name"`
	FieldKey    string   `json:"field_key"`
	FieldType   string   `json:"field_type"`
	Options     []string `json:"options"`
	Description string   `json:"description"`
	Placeholder string   `json:"placeholder"`
	Required    bool     `json:"required"`
	AdminOnly   bool     `json:"admin_only"`
	ShowOnOrder bool     `json:"show_on_order"`
	Regex       string   `json:"regex"`
	SortWeight  int      `json:"sort_weight"`
}

type Provider struct {
	ID            int64          `json:"-"`
	PublicID      string         `json:"id"`
	Name          string         `json:"name"`
	ProviderType  string         `json:"provider_type"`
	BaseURL       string         `json:"base_url"`
	Username      string         `json:"username"`
	Config        map[string]any `json:"config"`
	Active        bool           `json:"active"`
	Status        string         `json:"status"`
	LastCheckedAt *time.Time     `json:"last_checked_at,omitempty"`
	LastSyncAt    *time.Time     `json:"last_sync_at,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type ProviderProduct struct {
	ID                int64          `json:"-"`
	ProviderID        int64          `json:"-"`
	UpstreamProductID string         `json:"upstream_product_id"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	PriceCents        int64          `json:"price_cents"`
	Currency          string         `json:"currency"`
	BillingCycle      string         `json:"billing_cycle"`
	RawPayload        map[string]any `json:"raw_payload,omitempty"`
	SyncedAt          time.Time      `json:"synced_at"`
}

type OrderItem struct {
	ProductName    string `json:"product_name"`
	BillingCycle   string `json:"billing_cycle"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	Quantity       int    `json:"quantity"`
	SubtotalCents  int64  `json:"subtotal_cents"`
}

type OrderPayment struct {
	Method  string     `json:"method"`
	PayType string     `json:"type,omitempty"`
	PaidAt  *time.Time `json:"paid_at,omitempty"`
}

type Order struct {
	ID            int64         `json:"-"`
	PublicID      string        `json:"id"`
	UserUID       int64         `json:"user_uid"`
	Status        string        `json:"status"`
	Kind          string        `json:"kind"`
	TotalCents    int64         `json:"total_cents"`
	DiscountCents int64         `json:"discount_cents"`
	CouponCode    string        `json:"coupon_code,omitempty"`
	Currency      string        `json:"currency"`
	Items         []OrderItem   `json:"items"`
	Payment       *OrderPayment `json:"payment,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	PaidAt        *time.Time    `json:"paid_at,omitempty"`
	CancelledAt   *time.Time    `json:"cancelled_at,omitempty"`
}

type Invoice struct {
	PublicID   string    `json:"id"`
	OrderID    string    `json:"order_id"`
	Status     string    `json:"status"`
	TotalCents int64     `json:"total_cents"`
	Currency   string    `json:"currency"`
	DueAt      time.Time `json:"due_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type Wallet struct {
	BalanceCents int64  `json:"balance_cents"`
	Currency     string `json:"currency"`
}

type WalletTransaction struct {
	PublicID      string    `json:"id"`
	Type          string    `json:"type"`
	AmountCents   int64     `json:"amount_cents"`
	BalanceBefore int64     `json:"balance_before_cents"`
	BalanceAfter  int64     `json:"balance_after_cents"`
	Currency      string    `json:"currency"`
	ReferenceType string    `json:"reference_type"`
	ReferenceID   string    `json:"reference_id"`
	Description   string    `json:"description"`
	CreatedAt     time.Time `json:"created_at"`
}

type Service struct {
	PublicID     string     `json:"id"`
	Status       string     `json:"status"`
	ProviderType string     `json:"provider_type"`
	ProviderRef  string     `json:"provider_ref"`
	ProductName  string     `json:"product_name"`
	BillingCycle string     `json:"billing_cycle"`
	PriceCents   int64      `json:"price_cents"`
	Currency     string     `json:"currency"`
	ExpiresAt    *time.Time `json:"expires_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Ticket struct {
	PublicID         string     `json:"id"`
	Subject          string     `json:"subject"`
	Status           string     `json:"status"`
	Priority         string     `json:"priority"`
	UserUID          int64      `json:"user_uid,omitempty"`
	UserEmail        string     `json:"user_email,omitempty"`
	LastReplyAt      *time.Time `json:"last_reply_at,omitempty"`
	LastReplyIsStaff bool       `json:"last_reply_is_staff"`
	CreatedAt        time.Time  `json:"created_at"`
}

type TicketMessage struct {
	PublicID    string    `json:"id"`
	SenderUID   int64     `json:"sender_uid"`
	SenderEmail string    `json:"sender_email"`
	IsStaff     bool      `json:"is_staff"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
}

type TicketDetail struct {
	Ticket
	Messages []TicketMessage `json:"messages"`
}

type PaymentProvider struct {
	ID         int64          `json:"-"`
	PublicID   string         `json:"id"`
	Name       string         `json:"name"`
	Method     string         `json:"method"`
	GatewayURL string         `json:"gateway_url"`
	MerchantID string         `json:"merchant_id"`
	Config     map[string]any `json:"config"`
	Active     bool           `json:"active"`
	HasSecret  bool           `json:"has_secret"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type AdminUser struct {
	UID           int64      `json:"uid"`
	PublicID      string     `json:"id"`
	Email         string     `json:"email"`
	Status        string     `json:"status"`
	EmailVerified bool       `json:"email_verified"`
	BalanceCents  int64      `json:"balance_cents"`
	Currency      string     `json:"currency"`
	OrderCount    int64      `json:"order_count"`
	CreatedAt     time.Time  `json:"created_at"`
	LastLoginAt   *time.Time `json:"last_login_at,omitempty"`
}

type AuditEntry struct {
	ID        int64     `json:"id"`
	ActorUID  int64     `json:"actor_uid"`
	ActorMail string    `json:"actor_email,omitempty"`
	Action    string    `json:"action"`
	Kind      string    `json:"object_type"`
	ObjID     string    `json:"object_id"`
	RequestID string    `json:"request_id,omitempty"`
	IP        string    `json:"ip,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	Before    any       `json:"before_data,omitempty"`
	After     any       `json:"after_data,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Webhook struct {
	ID             int64      `json:"-"`
	PublicID       string     `json:"id"`
	Name           string     `json:"name"`
	URL            string     `json:"url"`
	Events         []string   `json:"events"`
	Active         bool       `json:"active"`
	LastDeliveryAt *time.Time `json:"last_delivery_at,omitempty"`
	LastDeliveryOK *bool      `json:"last_delivery_ok,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type WebhookDelivery struct {
	PublicID     string     `json:"id"`
	WebhookID    int64      `json:"-"`
	Event        string     `json:"event"`
	Status       string     `json:"status"`
	Attempts     int        `json:"attempts"`
	ResponseCode *int       `json:"response_code,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Payload      any        `json:"payload,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
}

type Refund struct {
	PublicID    string    `json:"id"`
	OrderID     string    `json:"order_id"`
	PaymentTxn  string    `json:"payment_transaction_id"`
	AmountCents int64     `json:"amount_cents"`
	Currency    string    `json:"currency"`
	Method      string    `json:"method"`
	Reason      string    `json:"reason"`
	CreatedAt   time.Time `json:"created_at"`
}

// ProductGroup is a display grouping for the storefront (第五阶段 Product -> Product Group).
type ProductGroup struct {
	PublicID     string    `json:"id"`
	Name         string    `json:"name"`
	SortWeight   int       `json:"sort_weight"`
	ProductCount int64     `json:"product_count"`
	CreatedAt    time.Time `json:"created_at"`
}

// ProductPrice is one purchasable billing cycle of a product.
type ProductPrice struct {
	BillingCycle string `json:"billing_cycle"`
	Currency     string `json:"currency"`
	AmountCents  int64  `json:"amount_cents"`
	Active       bool   `json:"active"`
}

// Announcement is a platform notice shown on the client dashboard.
type Announcement struct {
	PublicID  string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Active    bool      `json:"active"`
	Pinned    bool      `json:"pinned"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// InvoiceItem is one line on a bill.
type InvoiceItem struct {
	Description string `json:"description"`
	AmountCents int64  `json:"amount_cents"`
}

// InvoiceDetail is a single bill with its line items and owning order status.
type InvoiceDetail struct {
	Invoice
	Items       []InvoiceItem `json:"items"`
	OrderStatus string        `json:"order_status"`
	PaidAt      *time.Time    `json:"paid_at,omitempty"`
}

// SessionDevice is one active login session shown in 设备管理.
type SessionDevice struct {
	ID        int64     `json:"id"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ServiceDetail is one service with its provisioned instance data
// (credentials the upstream returned on creation).
type ServiceDetail struct {
	Service
	Config    map[string]any `json:"config,omitempty"`
	AutoRenew bool           `json:"auto_renew"`
}

// Coupon is a discount code (优惠系统). Type fixed = cents off, percent = 1..100.
type Coupon struct {
	PublicID       string     `json:"id"`
	Code           string     `json:"code"`
	Type           string     `json:"type"`
	Value          int64      `json:"value"`
	MaxUses        *int       `json:"max_uses,omitempty"`
	MaxUsesPerUser int        `json:"max_uses_per_user"`
	UsedCount      int64      `json:"used_count"`
	MinAmountCents int64      `json:"min_amount_cents"`
	ProductIDs     []string   `json:"product_ids"`
	StartsAt       *time.Time `json:"starts_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Active         bool       `json:"active"`
	CreatedAt      time.Time  `json:"created_at"`
}

// Notification is one in-app notice (站内通知中心).
type Notification struct {
	PublicID  string     `json:"id"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Link      string     `json:"link"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// TicketAttachment is an uploaded file bound to a ticket (§43).
type TicketAttachment struct {
	PublicID   string    `json:"id"`
	TicketID   string    `json:"ticket_id"`
	UploaderID int64     `json:"uploader_id"`
	Filename   string    `json:"filename"`
	Mime       string    `json:"mime"`
	SizeBytes  int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
}

// MailTemplate is an editable notification template with {{placeholder}}s.
type MailTemplate struct {
	Name      string    `json:"name"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Currency is a display/settlement currency with its rate to the base.
type Currency struct {
	Code   string `json:"code"`
	Rate   string `json:"rate"`
	Symbol string `json:"symbol"`
	Active bool   `json:"active"`
}

// Extension is one registered WASM extension package (第十阶段).
type Extension struct {
	PublicID    string    `json:"id"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Description string    `json:"description"`
	Permissions []string  `json:"permissions"`
	Active      bool      `json:"active"`
	SourcePath  string    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
