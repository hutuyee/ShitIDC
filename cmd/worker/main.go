package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/hutuyee/ShitIDC/internal/config"
	"github.com/hutuyee/ShitIDC/internal/database"
	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/mail"
	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/notify"
	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/provider/baota"
	"github.com/hutuyee/ShitIDC/internal/provider/bthosts"
	"github.com/hutuyee/ShitIDC/internal/provider/custom"
	"github.com/hutuyee/ShitIDC/internal/provider/magiccube"
	"github.com/hutuyee/ShitIDC/internal/provider/nokvm"
	"github.com/hutuyee/ShitIDC/internal/provider/proxmox"
	"github.com/hutuyee/ShitIDC/internal/provider/virtualizor"
	"github.com/hutuyee/ShitIDC/internal/provider/wlkangle"
	"github.com/hutuyee/ShitIDC/internal/queue"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
	"github.com/hutuyee/ShitIDC/internal/webhook"
)

type worker struct {
	st  *store.Store
	cfg config.Config
	bus *events.Bus
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	db, err := database.Open(context.Background(), cfg.PostgresDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	st := store.New(db).WithMasterKey(cfg.MasterKey)
	bus := events.New()
	q := queue.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer q.Close()
	// Webhook fan-out and the notification center also run for events
	// emitted inside workers.
	bus.Subscribe(webhook.Fanout(st, q, func(s string) (string, error) { return security.Decrypt(cfg.MasterKey, s) }))
	notify.Install(bus, st, nil)
	// 背景事件同样支持「值邮件通知管理员」（对齐魔方 EmailNoticeAdmin 插件）。
	notify.InstallAdminMail(bus, st, func(ctx context.Context, to, subject, body, provider string) error {
		return q.MailSendVia(to, subject, body, provider)
	})
	w := &worker{st: st, cfg: cfg, bus: bus}
	srv := asynq.NewServer(asynq.RedisClientOpt{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB}, asynq.Config{Concurrency: 12, Queues: map[string]int{"critical": 8, "default": 4}, RetryDelayFunc: func(n int, e error, t *asynq.Task) time.Duration {
		if n > 6 {
			n = 6
		}
		return time.Duration(1<<n) * time.Second
	}})
	mux := asynq.NewServeMux()
	mux.HandleFunc(queue.TaskServiceProvision, w.provision)
	mux.HandleFunc(queue.TaskProviderSync, w.sync)
	mux.HandleFunc(queue.TaskMailSend, w.mailSend)
	mux.HandleFunc(queue.TaskWebhookDeliver, w.webhookDeliver)
	mux.HandleFunc(queue.TaskServiceSuspend, w.suspend)
	mux.HandleFunc(queue.TaskServiceUnsuspend, w.unsuspend)
	mux.HandleFunc(queue.TaskServiceTerminate, w.terminate)
	mux.HandleFunc(queue.TaskServiceRenew, w.renew)
	mux.HandleFunc(queue.TaskServiceUpgrade, w.upgrade)
	if err := srv.Run(mux); err != nil {
		log.Fatal(err)
	}
}

// resolve builds the provider implementation for a service row.
func (w *worker) resolve(ctx context.Context, providerID int64, providerType string) (provider.Provider, error) {
	switch strings.ToLower(providerType) {
	case "manual", "":
		return provider.Manual{}, nil
	case "magiccube":
		return w.resolveMagicCube(ctx, providerID)
	case "proxmox":
		return w.resolveProxmox(ctx, providerID)
	case "virtualizor":
		return w.resolveVirtualizor(ctx, providerID)
	case "baota":
		return w.resolveBaota(ctx, providerID)
	case "nokvm":
		return w.resolveNokvm(ctx, providerID)
	case "wlkangle":
		return w.resolveWlkangle(ctx, providerID)
	case "bthosts":
		return w.resolveBthosts(ctx, providerID)
	case "custom":
		return w.resolveCustom(ctx, providerID)
	default:
		return nil, fmt.Errorf("unsupported provider_type %q", providerType)
	}
}

// providerSecret loads and decrypts one persisted upstream provider.
func (w *worker) providerSecret(ctx context.Context, providerID int64) (model.Provider, string, error) {
	if providerID <= 0 {
		return model.Provider{}, "", fmt.Errorf("provider_id missing")
	}
	pv, encrypted, err := w.st.GetProviderCredentialsByID(ctx, providerID)
	if err != nil {
		return model.Provider{}, "", err
	}
	secret, err := security.Decrypt(w.cfg.MasterKey, encrypted)
	if err != nil {
		return model.Provider{}, "", fmt.Errorf("provider secret decrypt failed: %w", err)
	}
	return pv, secret, nil
}

// resolveCustom 构建声明式上游（魔方插件导入产物）：规格存在 providers.config
// 的 "spec" 键，token 是接口密钥（对应魔方 accesshash / 服务器密码）。
func (w *worker) resolveCustom(ctx context.Context, providerID int64) (provider.Provider, error) {
	pv, secret, err := w.providerSecret(ctx, providerID)
	if err != nil {
		return nil, err
	}
	return custom.FromProvider(pv, secret)
}

func (w *worker) resolveMagicCube(ctx context.Context, providerID int64) (provider.Provider, error) {
	var mc magiccube.Config
	if providerID > 0 {
		pv, encrypted, e := w.st.GetProviderCredentialsByID(ctx, providerID)
		if e != nil {
			return nil, e
		}
		secret, e := security.Decrypt(w.cfg.MasterKey, encrypted)
		if e != nil {
			return nil, fmt.Errorf("provider secret decrypt failed: %w", e)
		}
		b, _ := json.Marshal(pv.Config)
		var saved struct {
			AuthMode     string          `json:"auth_mode"`
			TokenPrefix  string          `json:"token_prefix"`
			AllowPrivate bool            `json:"allow_private"`
			Paths        magiccube.Paths `json:"paths"`
		}
		_ = json.Unmarshal(b, &saved)
		mc = magiccube.Config{BaseURL: pv.BaseURL, Username: pv.Username, APIKey: secret, AuthMode: saved.AuthMode, TokenPrefix: saved.TokenPrefix, AllowPrivate: saved.AllowPrivate, Paths: saved.Paths}
	} else {
		// Compatibility fallback for products created before persistent provider management existed.
		mc = magiccube.Config{BaseURL: os.Getenv("MAGICCUBE_DEFAULT_BASE_URL"), Username: os.Getenv("MAGICCUBE_DEFAULT_USERNAME"), APIKey: os.Getenv("MAGICCUBE_DEFAULT_API_KEY"), AuthMode: os.Getenv("MAGICCUBE_AUTH_MODE"), TokenPrefix: os.Getenv("MAGICCUBE_TOKEN_PREFIX"), UserHeader: os.Getenv("MAGICCUBE_USER_HEADER"), APIKeyHeader: os.Getenv("MAGICCUBE_API_KEY_HEADER"), AllowPrivate: os.Getenv("MAGICCUBE_ALLOW_PRIVATE") == "true", Paths: magiccube.Paths{Login: os.Getenv("MAGICCUBE_LOGIN_PATH"), Products: os.Getenv("MAGICCUBE_PRODUCTS_PATH"), Create: os.Getenv("MAGICCUBE_CREATE_PATH"), Suspend: os.Getenv("MAGICCUBE_SUSPEND_PATH"), Unsuspend: os.Getenv("MAGICCUBE_UNSUSPEND_PATH"), Terminate: os.Getenv("MAGICCUBE_TERMINATE_PATH"), Renew: os.Getenv("MAGICCUBE_RENEW_PATH"), Test: os.Getenv("MAGICCUBE_TEST_PATH")}}
	}
	return magiccube.New(mc)
}

// resolveProxmox builds the PVE client: secret = token value, config holds
// node / api_token_id / vm_type / template settings.
func (w *worker) resolveProxmox(ctx context.Context, providerID int64) (provider.Provider, error) {
	pv, secret, err := w.providerSecret(ctx, providerID)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(pv.Config)
	var cfg proxmox.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("proxmox config decode: %w", err)
	}
	cfg.BaseURL = pv.BaseURL
	cfg.APIToken = secret
	return proxmox.New(cfg)
}

// resolveVirtualizor builds the panel client: secret = JSON {api_key, api_pass}.
func (w *worker) resolveVirtualizor(ctx context.Context, providerID int64) (provider.Provider, error) {
	pv, secret, err := w.providerSecret(ctx, providerID)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(pv.Config)
	var cfg virtualizor.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("virtualizor config decode: %w", err)
	}
	cfg.BaseURL = pv.BaseURL
	var keys struct {
		APIKey  string `json:"api_key"`
		APIPass string `json:"api_pass"`
	}
	if err := json.Unmarshal([]byte(secret), &keys); err != nil || keys.APIKey == "" {
		return nil, fmt.Errorf("virtualizor secret 需为 JSON {\"api_key\",\"api_pass\"}")
	}
	cfg.APIKey, cfg.APIPass = keys.APIKey, keys.APIPass
	return virtualizor.New(cfg)
}

func decodeServiceID(t *asynq.Task) (string, error) {
	var p struct {
		ServiceID string `json:"service_id"`
	}
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	return p.ServiceID, nil
}

func (w *worker) provision(ctx context.Context, t *asynq.Task) error {
	serviceID, err := decodeServiceID(t)
	if err != nil {
		return err
	}
	_, userID, providerID, providerType, productRef, claimed, err := w.st.ClaimServiceForProvisioning(ctx, serviceID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	impl, err := w.resolve(ctx, providerID, providerType)
	if err != nil {
		_ = w.st.MarkService(ctx, serviceID, "failed", "", map[string]any{"error": err.Error()})
		return err
	}
	// 开通上下文：订单里的配置项与自定义字段（魔方 $params['configoptions']/
	// $params['customfields'] 的等价物），custom 供应商按模板引用这些值。
	createOpts := map[string]any{}
	if pctx, perr := w.st.GetServiceProvisionContext(ctx, serviceID); perr == nil {
		createOpts["configoptions"] = pctx.ConfigOptions
		createOpts["customfields"] = pctx.CustomFields
		createOpts["email"] = pctx.UserEmail
		if pctx.Quantity > 0 {
			createOpts["quantity"] = pctx.Quantity
		}
	}
	instance, err := impl.Create(ctx, provider.CreateRequest{RequestID: serviceID, ProductRef: productRef, UserID: userID, Options: createOpts})
	if err != nil {
		_ = w.st.MarkService(ctx, serviceID, "failed", "", map[string]any{"error": err.Error()})
		w.bus.Emit(ctx, events.ServiceFailed, map[string]any{"service_id": serviceID, "error": err.Error()})
		if strings.EqualFold(providerType, "magiccube") && os.Getenv("MAGICCUBE_RETRY_CREATE") != "true" {
			return fmt.Errorf("%w: magiccube create failed: %v", asynq.SkipRetry, err)
		}
		return err
	}
	if err := w.st.MarkService(ctx, serviceID, "active", instance.ID, instance.Data); err != nil {
		return err
	}
	w.bus.Emit(ctx, events.ServiceCreated, map[string]any{"service_id": serviceID, "user_uid": userID, "provider_ref": instance.ID})
	return w.st.CompleteOrderIfReady(ctx, serviceID)
}

func (w *worker) sync(ctx context.Context, t *asynq.Task) error {
	providers, err := w.st.ListProviders(ctx)
	if err != nil {
		return err
	}
	var failures []string
	for _, pv := range providers {
		if !pv.Active || !strings.EqualFold(pv.ProviderType, "magiccube") {
			continue
		}
		if err := w.syncOne(ctx, pv); err != nil {
			log.Printf("provider sync %s: %v", pv.Name, err)
			failures = append(failures, pv.Name+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("provider sync failures: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (w *worker) syncOne(ctx context.Context, pv model.Provider) error {
	impl, err := w.resolve(ctx, pv.ID, pv.ProviderType)
	if err != nil {
		_ = w.st.UpdateProviderCheck(ctx, pv.ID, false, err.Error())
		return err
	}
	mc, ok := impl.(*magiccube.Client)
	if !ok {
		return nil
	}
	remote, err := mc.ListProducts(ctx)
	if err != nil {
		_ = w.st.UpdateProviderCheck(ctx, pv.ID, false, err.Error())
		return err
	}
	items := make([]model.ProviderProduct, 0, len(remote))
	for _, item := range remote {
		items = append(items, model.ProviderProduct{ProviderID: pv.ID, UpstreamProductID: item.ID, Name: item.Name, Description: item.Description, PriceCents: item.PriceCents, Currency: item.Currency, BillingCycle: item.BillingCycle, RawPayload: item.Raw})
	}
	if err := w.st.SaveProviderProducts(ctx, pv.ID, items); err != nil {
		return err
	}
	log.Printf("provider sync %s: %d upstream product(s)", pv.Name, len(items))
	return nil
}

func (w *worker) mailSend(ctx context.Context, t *asynq.Task) error {
	var p struct {
		To       string `json:"to"`
		Subject  string `json:"subject"`
		Body     string `json:"body"`
		Provider string `json:"provider,omitempty"`
	}
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	if strings.TrimSpace(p.To) == "" {
		return fmt.Errorf("%w: empty recipient", asynq.SkipRetry)
	}
	// 邮件通道优先；未配置通道时回退到内置 SMTP（与 API 侧解析顺序一致）。
	var pv store.MailProvider
	var secretEnc string
	var perr error
	if strings.TrimSpace(p.Provider) != "" {
		pv, secretEnc, perr = w.st.GetMailProvider(ctx, strings.TrimSpace(p.Provider))
		if errors.Is(perr, store.ErrNotFound) {
			return fmt.Errorf("%w: mail provider %s not found", asynq.SkipRetry, p.Provider)
		}
	} else {
		pv, secretEnc, perr = w.st.ActiveMailProvider(ctx)
	}
	if perr == nil {
		impl, ok := mail.Get(pv.Provider)
		if !ok {
			return fmt.Errorf("%w: unknown mail provider %s", asynq.SkipRetry, pv.Provider)
		}
		secret := mail.Secret{}
		if strings.TrimSpace(secretEnc) != "" {
			if len(w.cfg.MasterKey) == 0 {
				return fmt.Errorf("%w: MASTER_KEY_BASE64 missing, cannot decrypt mail secret", asynq.SkipRetry)
			}
			plain, derr := security.Decrypt(w.cfg.MasterKey, secretEnc)
			if derr != nil {
				return fmt.Errorf("%w: mail secret decrypt failed", asynq.SkipRetry)
			}
			var m map[string]string
			if jerr := json.Unmarshal([]byte(plain), &m); jerr != nil {
				return fmt.Errorf("%w: mail secret is not valid json", asynq.SkipRetry)
			}
			secret = mail.Secret(m)
		}
		cfg := mail.Config{Provider: pv.Provider, Fields: pv.Config}
		if verr := impl.Validate(cfg, secret); verr != nil {
			return fmt.Errorf("%w: %v", asynq.SkipRetry, verr)
		}
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		serr := impl.Send(sctx, cfg, secret, mail.Message{To: p.To, Subject: p.Subject, HTML: p.Body})
		errStr := ""
		if serr != nil {
			errStr = serr.Error()
		}
		w.st.TouchMailProvider(sctx, pv.PublicID, serr == nil, errStr)
		if serr != nil {
			return fmt.Errorf("mail provider %s: %w", pv.Provider, serr)
		}
		return nil
	}
	if !errors.Is(perr, store.ErrNotFound) {
		return fmt.Errorf("read active mail provider: %w", perr)
	}
	settings, err := w.st.GetMailSettings(ctx)
	if err != nil {
		return fmt.Errorf("%w: SMTP not configured: %v", asynq.SkipRetry, err)
	}
	opts := mail.Options{Host: settings.SMTPHost, Port: settings.SMTPPort, Username: settings.SMTPUsername, From: settings.SMTPFrom, Encryption: settings.SMTPEncryption}
	if !opts.Enabled() {
		return fmt.Errorf("%w: SMTP not configured", asynq.SkipRetry)
	}
	if settings.SMTPPasswordEn != "" {
		plain, derr := security.Decrypt(w.cfg.MasterKey, settings.SMTPPasswordEn)
		if derr != nil {
			return fmt.Errorf("%w: smtp password decrypt failed", asynq.SkipRetry)
		}
		opts.Password = plain
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return opts.Send(sctx, p.To, p.Subject, p.Body)
}

func (w *worker) webhookDeliver(ctx context.Context, t *asynq.Task) error {
	var p struct {
		DeliveryID int64 `json:"delivery_id"`
	}
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	return webhook.ProcessDelivery(ctx, w.st, w.cfg.MasterKey, p.DeliveryID)
}

// lifecycle executes a provider action for a service claimed by the API or
// the scheduler, then finalizes the transition.
func (w *worker) lifecycle(ctx context.Context, t *asynq.Task, action string) error {
	serviceID, err := decodeServiceID(t)
	if err != nil {
		return err
	}
	var ref store.ServiceRef
	var prev string
	var ok bool
	switch action {
	case "suspend":
		ref, prev, ok, err = w.st.ClaimServiceForTransition(ctx, serviceID, "suspending", "active")
	case "unsuspend":
		ref, prev, ok, err = w.st.ClaimServiceForTransition(ctx, serviceID, "unsuspending", "suspended")
	case "terminate":
		ref, prev, ok, err = w.st.ClaimServiceForTransition(ctx, serviceID, "terminating", "active", "suspended")
	}
	_ = prev
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	impl, err := w.resolve(ctx, ref.ProviderID, ref.ProviderType)
	if err != nil {
		_ = w.st.FinalizeServiceTransition(ctx, serviceID, "", false, err.Error())
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch action {
	case "suspend":
		if err := impl.Suspend(sctx, ref.ProviderRef); err != nil {
			_ = w.st.FinalizeServiceTransition(ctx, serviceID, "", false, err.Error())
			return err
		}
		if err := w.st.FinalizeServiceTransition(ctx, serviceID, "suspended", true, ""); err != nil {
			return err
		}
		w.bus.Emit(ctx, events.ServiceSuspended, map[string]any{"service_id": serviceID, "user_uid": ref.UserID})
	case "unsuspend":
		if err := impl.Unsuspend(sctx, ref.ProviderRef); err != nil {
			_ = w.st.FinalizeServiceTransition(ctx, serviceID, "", false, err.Error())
			return err
		}
		if err := w.st.FinalizeServiceTransition(ctx, serviceID, "active", true, ""); err != nil {
			return err
		}
		w.bus.Emit(ctx, events.ServiceUnsuspended, map[string]any{"service_id": serviceID, "user_uid": ref.UserID})
	case "terminate":
		if err := impl.Terminate(sctx, ref.ProviderRef); err != nil {
			_ = w.st.FinalizeServiceTransition(ctx, serviceID, "", false, err.Error())
			return err
		}
		if err := w.st.FinalizeServiceTransition(ctx, serviceID, "terminated", true, ""); err != nil {
			return err
		}
		if err := w.st.RecordExpiredIPLog(ctx, serviceID); err != nil {
			log.Printf("expired ip log %s: %v", serviceID, err)
		}
		// 到期账单处理（对齐魔方 expired_auto_delete_bill 插件）：按配置了结
		// 已终止服务的未支付续费账单并留档，只记日志不影响终止。
		if err := w.st.RecordExpiredBillAction(ctx, serviceID); err != nil {
			log.Printf("expired bill action %s: %v", serviceID, err)
		}
		w.bus.Emit(ctx, events.ServiceTerminated, map[string]any{"service_id": serviceID, "user_uid": ref.UserID})
	}
	return nil
}

func (w *worker) suspend(ctx context.Context, t *asynq.Task) error {
	return w.lifecycle(ctx, t, "suspend")
}
func (w *worker) unsuspend(ctx context.Context, t *asynq.Task) error {
	return w.lifecycle(ctx, t, "unsuspend")
}
func (w *worker) terminate(ctx context.Context, t *asynq.Task) error {
	return w.lifecycle(ctx, t, "terminate")
}

// renew notifies the upstream provider about a paid renewal. The local
// expiry was already extended at payment time; failure here is retried by
// asynq and eventually recorded without changing the service status.
func (w *worker) renew(ctx context.Context, t *asynq.Task) error {
	serviceID, err := decodeServiceID(t)
	if err != nil {
		return err
	}
	ref, ok, err := w.st.ClaimServiceForRenew(ctx, serviceID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	impl, err := w.resolve(ctx, ref.ProviderID, ref.ProviderType)
	if err != nil {
		_ = w.st.PatchServicePayload(ctx, serviceID, map[string]any{"last_renew_ok": false, "last_renew_error": err.Error()})
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// 到期时间在续费订单支付时已推进，取一次传给上游（魔方 _Renew 约定）。
	renewReq := provider.RenewRequest{InstanceID: ref.ProviderRef}
	if inst, gerr := w.st.GetServiceInstance(ctx, serviceID); gerr == nil && inst.Service.ExpiresAt != nil {
		renewReq.ExpiresAt = *inst.Service.ExpiresAt
	}
	if err := impl.Renew(sctx, renewReq); err != nil {
		_ = w.st.PatchServicePayload(ctx, serviceID, map[string]any{"last_renew_ok": false, "last_renew_error": err.Error()})
		return err
	}
	w.bus.Emit(ctx, events.ServiceRenewed, map[string]any{"service_id": serviceID, "user_uid": ref.UserID})
	return w.st.PatchServicePayload(ctx, serviceID, map[string]any{"last_renew_ok": true, "last_renew_at": time.Now().Format(time.RFC3339), "last_renew_error": ""})
}

// upgrade notifies the upstream that a service moved to a new plan
// (magic cube server modules implement this as _ChangePackage). Providers that
// cannot change an existing instance return provider.ErrChangePackageUnsupported,
// which is surfaced on the service instead of failing the whole order.
func (w *worker) upgrade(ctx context.Context, t *asynq.Task) error {
	serviceID, err := decodeServiceID(t)
	if err != nil {
		return err
	}
	ref, ok, err := w.st.ClaimServiceForRenew(ctx, serviceID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	inst, err := w.st.GetServiceInstance(ctx, serviceID)
	if err != nil {
		_ = w.st.FinishUpgrade(ctx, serviceID, false, err.Error())
		return err
	}
	impl, err := w.resolve(ctx, ref.ProviderID, ref.ProviderType)
	if err != nil {
		_ = w.st.FinishUpgrade(ctx, serviceID, false, err.Error())
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = impl.ChangePackage(sctx, provider.ChangePackageRequest{
		InstanceID:     ref.ProviderRef,
		ProductRef:     inst.Service.ProductName,
		BillingCycle:   inst.Service.BillingCycle,
		PriceCents:     inst.Service.PriceCents,
		SelectionsJSON: inst.Data,
	})
	if errors.Is(err, provider.ErrChangePackageUnsupported) {
		// 上游不支持改配：本地记录已经切到新方案，只留一条说明，不算失败。
		_ = w.st.PatchServicePayload(ctx, serviceID, map[string]any{
			"last_upgrade_ok":    false,
			"last_upgrade_note":  "上游不支持在线改配，已按新方案记账",
			"last_upgrade_error": "",
		})
		w.bus.Emit(ctx, events.ServiceUpdated, map[string]any{"service_id": serviceID, "user_uid": ref.UserID, "kind": "upgrade_local_only"})
		return nil
	}
	if err != nil {
		_ = w.st.FinishUpgrade(ctx, serviceID, false, err.Error())
		return err
	}
	if err := w.st.FinishUpgrade(ctx, serviceID, true, ""); err != nil {
		return err
	}
	w.bus.Emit(ctx, events.ServiceUpdated, map[string]any{"service_id": serviceID, "user_uid": ref.UserID, "kind": "upgrade_applied"})
	return nil
}

// resolveBaota builds the 宝塔面板 client: config holds panel settings, the
// decrypted secret is the panel API key.
func (w *worker) resolveBaota(ctx context.Context, providerID int64) (provider.Provider, error) {
	pv, secret, err := w.providerSecret(ctx, providerID)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(pv.Config)
	var cfg baota.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("baota config decode: %w", err)
	}
	cfg.BaseURL = pv.BaseURL
	cfg.APIKey = secret
	return baota.New(cfg)
}

// resolveNokvm builds the NOKVM client: secret = 面板 API token。
func (w *worker) resolveNokvm(ctx context.Context, providerID int64) (provider.Provider, error) {
	pv, secret, err := w.providerSecret(ctx, providerID)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(pv.Config)
	var cfg nokvm.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("nokvm config decode: %w", err)
	}
	cfg.BaseURL = pv.BaseURL
	cfg.Token = secret
	return nokvm.New(cfg)
}

// resolveWlkangle builds the 未来 kangle 客户端: secret = 安全码（accesshash）。
func (w *worker) resolveWlkangle(ctx context.Context, providerID int64) (provider.Provider, error) {
	pv, secret, err := w.providerSecret(ctx, providerID)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(pv.Config)
	var cfg wlkangle.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("wlkangle config decode: %w", err)
	}
	cfg.BaseURL = pv.BaseURL
	cfg.Token = secret
	return wlkangle.New(cfg)
}

// resolveBthosts builds the Bthost 虚拟主机客户端: secret = 通讯密钥（accesshash）。
func (w *worker) resolveBthosts(ctx context.Context, providerID int64) (provider.Provider, error) {
	pv, secret, err := w.providerSecret(ctx, providerID)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(pv.Config)
	var cfg bthosts.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("bthosts config decode: %w", err)
	}
	cfg.BaseURL = pv.BaseURL
	cfg.Token = secret
	return bthosts.New(cfg)
}
