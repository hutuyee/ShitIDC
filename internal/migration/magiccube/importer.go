package magiccubemigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/hutuyee/ShitIDC/internal/security"
)

type Profile struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	Queries struct {
		Users    string `yaml:"users"`
		Products string `yaml:"products"`
		Wallets  string `yaml:"wallets"`
		Orders   string `yaml:"orders"`
		Invoices string `yaml:"invoices"`
		Payments string `yaml:"payments"`
		Services string `yaml:"services"`
	} `yaml:"queries"`
	Notes string `yaml:"notes"`
}

type Report struct {
	Profile               string          `json:"profile"`
	Version               string          `json:"version"`
	DryRun                bool            `json:"dry_run"`
	StartedAt             time.Time       `json:"started_at"`
	FinishedAt            time.Time       `json:"finished_at"`
	UsersFound            int             `json:"users_found"`
	UsersImported         int             `json:"users_imported"`
	ProductsFound         int             `json:"products_found"`
	ProductsImported      int             `json:"products_imported"`
	WalletsFound          int             `json:"wallets_found"`
	WalletsImported       int             `json:"wallets_imported"`
	WalletBalanceCents    int64           `json:"wallet_balance_cents"`
	OrdersFound           int             `json:"orders_found"`
	OrdersImported        int             `json:"orders_imported"`
	InvoicesFound         int             `json:"invoices_found"`
	InvoicesImported      int             `json:"invoices_imported"`
	PaymentsFound         int             `json:"payments_found"`
	PaymentsImported      int             `json:"payments_imported"`
	ServicesFound         int             `json:"services_found"`
	ServicesImported      int             `json:"services_imported"`
	RequiresPasswordReset int             `json:"requires_password_reset"`
	Reconciliation        *Reconciliation `json:"reconciliation,omitempty"`
	Warnings              []string        `json:"warnings"`
	Errors                []string        `json:"errors"`
}

// Reconciliation (第三十六阶段 对账) compares source row counts against what
// actually landed in PostgreSQL after the import. Any non-empty difference
// must be investigated before cutting over.
type Reconciliation struct {
	Source      ReconcileSide `json:"source"`
	Target      ReconcileSide `json:"target"`
	Differences []string      `json:"differences"`
}

type ReconcileSide struct {
	Users         int64 `json:"users"`
	Products      int64 `json:"products"`
	Orders        int64 `json:"orders"`
	Invoices      int64 `json:"invoices"`
	Payments      int64 `json:"payments"`
	Services      int64 `json:"services"`
	WalletBalance int64 `json:"wallet_balance_cents"`
}

// reconcile runs after the import steps: source counts come from wrapping
// each profile SELECT in COUNT(*), target counts from the ShitIDC tables.
func reconcile(ctx context.Context, src *sql.DB, target *pgxpool.Pool, profile Profile, dry bool, r *Report) {
	rec := &Reconciliation{}
	srcCount := func(q string) int64 {
		var n int64
		if err := src.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+q+") AS t").Scan(&n); err != nil {
			r.Warnings = append(r.Warnings, "reconcile source count failed: "+err.Error())
			return -1
		}
		return n
	}
	targetCount := func(table string) int64 {
		var n int64
		if err := target.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE deleted_at IS NULL").Scan(&n); err != nil {
			r.Warnings = append(r.Warnings, "reconcile target count "+table+": "+err.Error())
			return -1
		}
		return n
	}
	rec.Source.Users = srcCount(profile.Queries.Users)
	rec.Source.Products = srcCount(profile.Queries.Products)
	rec.Source.Orders = srcCount(profile.Queries.Orders)
	rec.Source.Invoices = srcCount(profile.Queries.Invoices)
	rec.Source.Payments = srcCount(profile.Queries.Payments)
	rec.Source.Services = srcCount(profile.Queries.Services)
	if err := target.QueryRow(ctx, "SELECT count(*) FROM users WHERE deleted_at IS NULL").Scan(&rec.Target.Users); err != nil {
		r.Warnings = append(r.Warnings, "reconcile users: "+err.Error())
	}
	rec.Target.Products = targetCount("products")
	rec.Target.Orders = targetCount("orders")
	rec.Target.Invoices = targetCount("invoices")
	rec.Target.Payments = targetCount("payments")
	rec.Target.Services = targetCount("services")
	if err := target.QueryRow(ctx, "SELECT coalesce(sum(balance_cents),0) FROM wallet_accounts").Scan(&rec.Target.WalletBalance); err != nil {
		r.Warnings = append(r.Warnings, "reconcile wallet balance: "+err.Error())
	}
	rec.Source.WalletBalance = r.WalletBalanceCents
	check := func(name string, want, got int64) {
		if want >= 0 && got >= 0 && want != got {
			rec.Differences = append(rec.Differences, fmt.Sprintf("%s: source %d != target %d", name, want, got))
		}
	}
	check("users", rec.Source.Users, rec.Target.Users)
	check("products", rec.Source.Products, rec.Target.Products)
	check("orders", rec.Source.Orders, rec.Target.Orders)
	check("invoices", rec.Source.Invoices, rec.Target.Invoices)
	check("payments", rec.Source.Payments, rec.Target.Payments)
	check("services", rec.Source.Services, rec.Target.Services)
	if !dry && r.WalletBalanceCents > 0 && rec.Target.WalletBalance != r.WalletBalanceCents {
		rec.Differences = append(rec.Differences, fmt.Sprintf("wallet balance: imported total %d != target total %d", r.WalletBalanceCents, rec.Target.WalletBalance))
	}
	r.Reconciliation = rec
}

func LoadProfile(path string) (Profile, error) {
	var p Profile
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	err = yaml.Unmarshal(b, &p)
	return p, err
}

func Run(ctx context.Context, sourceDSN string, target *pgxpool.Pool, profile Profile, dry bool) (Report, error) {
	r := Report{Profile: profile.Name, Version: profile.Version, DryRun: dry, StartedAt: time.Now().UTC()}
	src, err := sql.Open("mysql", sourceDSN)
	if err != nil {
		return r, err
	}
	defer src.Close()
	if err = src.PingContext(ctx); err != nil {
		return r, fmt.Errorf("source mysql: %w", err)
	}

	steps := []struct {
		name  string
		query string
		fn    func(context.Context, *sql.DB, *pgxpool.Pool, string, bool, *Report) error
	}{
		{"users", profile.Queries.Users, importUsers},
		{"products", profile.Queries.Products, importProducts},
		{"wallets", profile.Queries.Wallets, importWallets},
		{"orders", profile.Queries.Orders, importOrders},
		{"invoices", profile.Queries.Invoices, importInvoices},
		{"payments", profile.Queries.Payments, importPayments},
		{"services", profile.Queries.Services, importServices},
	}
	for _, step := range steps {
		if strings.TrimSpace(step.query) == "" {
			continue
		}
		if err := step.fn(ctx, src, target, step.query, dry, &r); err != nil {
			r.Errors = append(r.Errors, step.name+": "+err.Error())
		}
	}
	if profile.Queries.Wallets == "" {
		r.Warnings = append(r.Warnings, "wallets query is empty; balances were not migrated")
	}
	if profile.Queries.Payments == "" {
		r.Warnings = append(r.Warnings, "payments query is empty; payment history was not migrated")
	}
	reconcile(ctx, src, target, profile, dry, &r)
	r.FinishedAt = time.Now().UTC()
	if len(r.Errors) > 0 {
		return r, fmt.Errorf("migration completed with %d errors", len(r.Errors))
	}
	return r, nil
}

func importUsers(ctx context.Context, src *sql.DB, target *pgxpool.Pool, q string, dry bool, r *Report) error {
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var legacy, email, legacyHash string
		var created sql.NullTime
		if err := rows.Scan(&legacy, &email, &legacyHash, &created); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		r.UsersFound++
		r.RequiresPasswordReset++
		if dry {
			continue
		}
		random, _ := security.RandomToken(32)
		hash, _ := security.HashPassword("Reset_" + random)
		tx, err := target.Begin(ctx)
		if err != nil {
			return err
		}
		var publicID string
		var userID int64
		createdAt := time.Now().UTC()
		if created.Valid {
			createdAt = created.Time
		}
		err = tx.QueryRow(ctx, `INSERT INTO users(email,status,created_at) VALUES(lower($1),'active',$2) ON CONFLICT(email) DO UPDATE SET email=EXCLUDED.email RETURNING id,public_id::text`, email, createdAt).Scan(&userID, &publicID)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO user_security(user_id,password_hash,legacy_password_hash) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET legacy_password_hash=EXCLUDED.legacy_password_hash`, userID, hash, legacyHash)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,currency) VALUES($1,'CNY') ON CONFLICT(user_id,currency) DO NOTHING`, userID)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) SELECT $1,id FROM roles WHERE name='customer' ON CONFLICT DO NOTHING`, userID)
		}
		if err == nil {
			err = putRef(ctx, tx, "user", legacy, publicID)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			r.Errors = append(r.Errors, "user "+legacy+": "+err.Error())
			continue
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		r.UsersImported++
	}
	return rows.Err()
}

func importProducts(ctx context.Context, src *sql.DB, target *pgxpool.Pool, q string, dry bool, r *Report) error {
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var legacy, name, desc, currency, cycle, providerType string
		var providerRef sql.NullString
		var price int64
		if err := rows.Scan(&legacy, &name, &desc, &price, &currency, &cycle, &providerType, &providerRef); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		r.ProductsFound++
		if dry {
			continue
		}
		tx, err := target.Begin(ctx)
		if err != nil {
			return err
		}
		var id int64
		var publicID string
		err = tx.QueryRow(ctx, `INSERT INTO products(name,description,provider_type,provider_product_ref) VALUES($1,$2,$3,$4) RETURNING id,public_id::text`, name, desc, providerType, providerRef).Scan(&id, &publicID)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO product_prices(product_id,billing_cycle,currency,amount_cents) VALUES($1,$2,$3,$4)`, id, cycle, currency, price)
		}
		if err == nil {
			err = putRef(ctx, tx, "product", legacy, publicID)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			r.Errors = append(r.Errors, "product "+legacy+": "+err.Error())
			continue
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		r.ProductsImported++
	}
	return rows.Err()
}

func importWallets(ctx context.Context, src *sql.DB, target *pgxpool.Pool, q string, dry bool, r *Report) error {
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var userLegacy, currency string
		var balance int64
		if err := rows.Scan(&userLegacy, &balance, &currency); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		r.WalletsFound++
		r.WalletBalanceCents += balance
		if dry {
			continue
		}
		userID, err := resolveID(ctx, target, "user", userLegacy, "users")
		if err != nil {
			r.Errors = append(r.Errors, "wallet user "+userLegacy+": "+err.Error())
			continue
		}
		tx, err := target.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return err
		}
		var accountID, before int64
		err = tx.QueryRow(ctx, `INSERT INTO wallet_accounts(user_id,currency,balance_cents) VALUES($1,$2,0) ON CONFLICT(user_id,currency) DO UPDATE SET currency=EXCLUDED.currency RETURNING id,balance_cents`, userID, currency).Scan(&accountID, &before)
		if err == nil {
			var count int
			err = tx.QueryRow(ctx, `SELECT count(*) FROM wallet_transactions WHERE account_id=$1`, accountID).Scan(&count)
			if err == nil && count > 0 {
				err = fmt.Errorf("target wallet already has transactions; refusing to overwrite")
			}
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE wallet_accounts SET balance_cents=$1,updated_at=now() WHERE id=$2`, balance, accountID)
		}
		if err == nil && balance != 0 {
			_, err = tx.Exec(ctx, `INSERT INTO wallet_transactions(account_id,type,amount_cents,balance_before_cents,balance_after_cents,currency,reference_type,reference_id,description,idempotency_key) VALUES($1,'credit',$2,0,$2,$3,'migration','magiccube','MagicCube opening balance',$4)`, accountID, balance, currency, "migration:wallet:"+userLegacy+":"+currency)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			r.Errors = append(r.Errors, "wallet "+userLegacy+": "+err.Error())
			continue
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		r.WalletsImported++
	}
	return rows.Err()
}

func importOrders(ctx context.Context, src *sql.DB, target *pgxpool.Pool, q string, dry bool, r *Report) error {
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var legacy, userLegacy, status, currency string
		var total int64
		var created sql.NullTime
		if err := rows.Scan(&legacy, &userLegacy, &status, &total, &currency, &created); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		r.OrdersFound++
		if dry {
			continue
		}
		userID, err := resolveID(ctx, target, "user", userLegacy, "users")
		if err != nil {
			r.Errors = append(r.Errors, "order "+legacy+": "+err.Error())
			continue
		}
		createdAt := time.Now().UTC()
		if created.Valid {
			createdAt = created.Time
		}
		var publicID string
		err = target.QueryRow(ctx, `INSERT INTO orders(user_id,status,total_cents,currency,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$5) RETURNING public_id::text`, userID, status, total, currency, createdAt).Scan(&publicID)
		if err == nil {
			_, err = target.Exec(ctx, `INSERT INTO migration_refs(source,entity_type,legacy_id,new_public_id) VALUES('magiccube','order',$1,$2) ON CONFLICT DO NOTHING`, legacy, publicID)
		}
		if err != nil {
			r.Errors = append(r.Errors, "order "+legacy+": "+err.Error())
			continue
		}
		r.OrdersImported++
	}
	return rows.Err()
}

func importInvoices(ctx context.Context, src *sql.DB, target *pgxpool.Pool, q string, dry bool, r *Report) error {
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var legacy, orderLegacy, userLegacy, status, currency string
		var total int64
		var due, paid, created sql.NullTime
		if err := rows.Scan(&legacy, &orderLegacy, &userLegacy, &status, &total, &currency, &due, &paid, &created); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		r.InvoicesFound++
		if dry {
			continue
		}
		userID, err := resolveID(ctx, target, "user", userLegacy, "users")
		if err != nil {
			r.Errors = append(r.Errors, "invoice "+legacy+": "+err.Error())
			continue
		}
		orderID, err := resolveID(ctx, target, "order", orderLegacy, "orders")
		if err != nil {
			r.Errors = append(r.Errors, "invoice "+legacy+": "+err.Error())
			continue
		}
		dueAt := time.Now().UTC()
		if due.Valid {
			dueAt = due.Time
		}
		createdAt := time.Now().UTC()
		if created.Valid {
			createdAt = created.Time
		}
		var publicID string
		err = target.QueryRow(ctx, `INSERT INTO invoices(order_id,user_id,status,total_cents,currency,due_at,paid_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING public_id::text`, orderID, userID, status, total, currency, dueAt, nullableTime(paid), createdAt).Scan(&publicID)
		if err == nil {
			_, err = target.Exec(ctx, `INSERT INTO migration_refs(source,entity_type,legacy_id,new_public_id) VALUES('magiccube','invoice',$1,$2) ON CONFLICT DO NOTHING`, legacy, publicID)
		}
		if err != nil {
			r.Errors = append(r.Errors, "invoice "+legacy+": "+err.Error())
			continue
		}
		r.InvoicesImported++
	}
	return rows.Err()
}

func importPayments(ctx context.Context, src *sql.DB, target *pgxpool.Pool, q string, dry bool, r *Report) error {
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var legacy, userLegacy, method, transactionID, currency, status string
		var orderLegacy, invoiceLegacy sql.NullString
		var amount int64
		var created sql.NullTime
		if err := rows.Scan(&legacy, &userLegacy, &orderLegacy, &invoiceLegacy, &method, &transactionID, &amount, &currency, &status, &created); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		r.PaymentsFound++
		if dry {
			continue
		}
		userID, err := resolveID(ctx, target, "user", userLegacy, "users")
		if err != nil {
			r.Errors = append(r.Errors, "payment "+legacy+": "+err.Error())
			continue
		}
		var orderID, invoiceID any
		if orderLegacy.Valid && orderLegacy.String != "" {
			v, e := resolveID(ctx, target, "order", orderLegacy.String, "orders")
			if e != nil {
				r.Errors = append(r.Errors, "payment "+legacy+": "+e.Error())
				continue
			}
			orderID = v
		}
		if invoiceLegacy.Valid && invoiceLegacy.String != "" {
			v, e := resolveID(ctx, target, "invoice", invoiceLegacy.String, "invoices")
			if e != nil {
				r.Errors = append(r.Errors, "payment "+legacy+": "+e.Error())
				continue
			}
			invoiceID = v
		}
		createdAt := time.Now().UTC()
		if created.Valid {
			createdAt = created.Time
		}
		var publicID string
		err = target.QueryRow(ctx, `INSERT INTO payments(user_id,order_id,invoice_id,method,transaction_id,amount_cents,currency,status,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(transaction_id) DO UPDATE SET transaction_id=EXCLUDED.transaction_id RETURNING public_id::text`, userID, orderID, invoiceID, method, transactionID, amount, currency, status, createdAt).Scan(&publicID)
		if err == nil {
			_, err = target.Exec(ctx, `INSERT INTO migration_refs(source,entity_type,legacy_id,new_public_id) VALUES('magiccube','payment',$1,$2) ON CONFLICT DO NOTHING`, legacy, publicID)
		}
		if err != nil {
			r.Errors = append(r.Errors, "payment "+legacy+": "+err.Error())
			continue
		}
		r.PaymentsImported++
	}
	return rows.Err()
}

func importServices(ctx context.Context, src *sql.DB, target *pgxpool.Pool, q string, dry bool, r *Report) error {
	rows, err := src.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var legacy, userLegacy, orderLegacy, productLegacy, status, providerType string
		var providerRef sql.NullString
		var expires, created sql.NullTime
		if err := rows.Scan(&legacy, &userLegacy, &orderLegacy, &productLegacy, &status, &providerType, &providerRef, &expires, &created); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		r.ServicesFound++
		if dry {
			continue
		}
		userID, err := resolveID(ctx, target, "user", userLegacy, "users")
		if err != nil {
			r.Errors = append(r.Errors, "service "+legacy+": "+err.Error())
			continue
		}
		orderID, err := resolveID(ctx, target, "order", orderLegacy, "orders")
		if err != nil {
			r.Errors = append(r.Errors, "service "+legacy+": "+err.Error())
			continue
		}
		productID, err := resolveID(ctx, target, "product", productLegacy, "products")
		if err != nil {
			r.Errors = append(r.Errors, "service "+legacy+": "+err.Error())
			continue
		}
		createdAt := time.Now().UTC()
		if created.Valid {
			createdAt = created.Time
		}
		var publicID string
		err = target.QueryRow(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,status,provider_type,provider_ref,expires_at,created_at,updated_at) VALUES($1,$2,NULL,$3,$4,$5,$6,$7,$8,$8) RETURNING public_id::text`, userID, orderID, productID, status, providerType, providerRef, nullableTime(expires), createdAt).Scan(&publicID)
		if err == nil {
			_, err = target.Exec(ctx, `INSERT INTO migration_refs(source,entity_type,legacy_id,new_public_id) VALUES('magiccube','service',$1,$2) ON CONFLICT DO NOTHING`, legacy, publicID)
		}
		if err != nil {
			r.Errors = append(r.Errors, "service "+legacy+": "+err.Error())
			continue
		}
		r.ServicesImported++
	}
	return rows.Err()
}

func putRef(ctx context.Context, tx pgx.Tx, entity, legacy, publicID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO migration_refs(source,entity_type,legacy_id,new_public_id) VALUES('magiccube',$1,$2,$3) ON CONFLICT DO NOTHING`, entity, legacy, publicID)
	return err
}

func resolveID(ctx context.Context, target *pgxpool.Pool, entity, legacy, table string) (int64, error) {
	allowed := map[string]bool{"users": true, "products": true, "orders": true, "invoices": true}
	if !allowed[table] {
		return 0, fmt.Errorf("unsupported target table")
	}
	query := fmt.Sprintf(`SELECT t.id FROM migration_refs r JOIN %s t ON t.public_id=r.new_public_id WHERE r.source='magiccube' AND r.entity_type=$1 AND r.legacy_id=$2`, table)
	var id int64
	if err := target.QueryRow(ctx, query, entity, legacy).Scan(&id); err != nil {
		return 0, fmt.Errorf("missing %s mapping for legacy id %s: %w", entity, legacy, err)
	}
	return id, nil
}

func nullableTime(v sql.NullTime) any {
	if v.Valid {
		return v.Time
	}
	return nil
}

func SaveReport(path string, r Report) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
