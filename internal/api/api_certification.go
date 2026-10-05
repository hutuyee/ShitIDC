package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/certification"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 实名认证（对应魔方 public/plugins/certification/）。
//
// 隐私要点：接口**只返回掩码**，原始姓名与证件号不落库、不回传。
// 提交时先本地校验身份证校验位，挡掉打错一位再去调用付费通道。

// myCertification 返回当前用户的实名状态。
func (a *App) myCertification(c *gin.Context) {
	pr, _ := getPrincipal(c)
	required, err := a.Store.CertificationRequired(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取实名设置失败")
		return
	}
	cert, err := a.Store.GetCertification(c, pr.User.ID)
	if errors.Is(err, store.ErrNotFound) {
		// 没提交过不是错误：返回 required 供前端决定是否拦下单。
		httpx.OK(c, 200, map[string]any{"status": "none", "required": required, "fields": a.certExtraFields(c)})
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取实名信息失败")
		return
	}
	httpx.OK(c, 200, map[string]any{
		"status":           cert.Status,
		"required":         required,
		"real_name_masked": cert.RealNameMasked,
		"id_type":          cert.IDType,
		"id_number_masked": cert.IDNumberMasked,
		"gender":           cert.Gender,
		"birth_date":       cert.BirthDate,
		"reject_reason":    cert.RejectReason,
		"submitted_at":     cert.SubmittedAt,
		"provider":         cert.Provider,
		"fields":           a.certExtraFields(c),
		"reviewed_at":      cert.ReviewedAt,
	})
}

// submitCertification 提交实名认证。
func (a *App) submitCertification(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		RealName string            `json:"real_name"`
		IDNumber string            `json:"id_number"`
		IDType   string            `json:"id_type"`
		Extra    map[string]string `json:"extra"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.RealName = strings.TrimSpace(in.RealName)
	in.IDNumber = strings.ToUpper(strings.TrimSpace(in.IDNumber))
	if in.RealName == "" || in.IDNumber == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "姓名与证件号都不能为空")
		return
	}
	if in.IDType == "" {
		in.IDType = "idcard"
	}
	// 身份证做本地校验：校验位不对就没必要花钱调上游。
	if in.IDType == "idcard" {
		if err := certification.ValidateChinaIDCard(in.IDNumber); err != nil {
			httpx.Fail(c, 400, "INVALID_ID_NUMBER", err.Error())
			return
		}
	}
	// 已通过的记录不允许被静默覆盖：必须先由管理员驳回。
	if existing, err := a.Store.GetCertification(c, pr.User.ID); err == nil && existing.Status == "approved" {
		httpx.Fail(c, 409, "ALREADY_CERTIFIED", "已通过实名认证，如需修改请联系管理员")
		return
	}

	provider, secretEnc, err := a.Store.ActiveCertificationProvider(c)
	// 没配通道时退化为人工审核，而不是让用户卡死。
	providerName := "manual"
	cfg := certification.Config{Provider: providerName}
	secret := certification.Secret{}
	if err == nil {
		providerName = provider.Provider
		cfg = certification.Config{Provider: provider.Provider, Fields: provider.Config}
		secret, err = a.certSecret(secretEnc)
		if err != nil {
			httpx.Fail(c, 500, "CERT_SECRET_INVALID", err.Error())
			return
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取实名通道失败")
		return
	}

	// 性别与出生日期本地解出来（身份证前 18 位自带这些信息）。
	gender := ""
	birth := ""
	if in.IDType == "idcard" {
		gender = certification.GenderFromIDCard(in.IDNumber)
		birth = certification.BirthDateFromIDCard(in.IDNumber)
	}

	subject := certification.Subject{RealName: in.RealName, IDNumber: in.IDNumber, IDType: in.IDType, Extra: in.Extra}
	approved := false
	verifiedBy := ""
	message := ""
	if impl, ok := certification.Get(providerName); ok {
		// 通道可声明额外输入（银行卡号、手机号）；必填项在调用上游前挡住。
		if schemer, ok := impl.(certification.Schemer); ok {
			for _, f := range schemer.Fields(cfg) {
				if f.Required && subject.ExtraField(f.Key) == "" {
					httpx.Fail(c, 400, "CERT_FIELD_REQUIRED", "缺少必填项："+f.Label)
					return
				}
			}
		}
		// 扫码类通道：先落 pending 记录，前端拿二维码轮询。
		if challenger, ok := impl.(certification.Challenger); ok {
			chCtx, cancel := context.WithTimeout(c, 20*time.Second)
			ch, cerr := challenger.Challenge(chCtx, cfg, secret, subject)
			cancel()
			if cerr != nil {
				a.Store.MarkCertificationProviderHealth(c, provider.PublicID, false, cerr.Error())
				httpx.Fail(c, 502, "CERT_PROVIDER_FAILED", "发起实名认证失败："+cerr.Error())
				return
			}
			cert, err := a.Store.SubmitCertification(c, pr.User.ID, store.CertificationInput{
				RealName: in.RealName, IDNumber: in.IDNumber, IDType: in.IDType,
				Provider: providerName, Approved: false,
				ProviderRef: ch.Token, ProviderURL: ch.URL,
				Gender: gender, BirthDate: birth,
			})
			if err != nil {
				httpx.Fail(c, 400, "CERT_SUBMIT_FAILED", err.Error())
				return
			}
			_ = a.Store.Audit(c, pr.User.ID, "certification.submit", "certification", cert.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": cert.Status, "provider": providerName, "challenge": true})
			httpx.OK(c, 200, map[string]any{
				"status":   "pending",
				"provider": providerName,
				"url":      ch.URL,
				"message":  ch.Message,
			})
			return
		}
		verifyCtx, cancel := context.WithTimeout(c, 15*time.Second)
		res, verr := impl.Verify(verifyCtx, cfg, secret, subject)
		cancel()
		if verr != nil {
			a.Store.MarkCertificationProviderHealth(c, provider.PublicID, false, verr.Error())
			httpx.Fail(c, 502, "CERT_PROVIDER_FAILED", "实名核验失败："+verr.Error())
			return
		}
		a.Store.MarkCertificationProviderHealth(c, provider.PublicID, true, "")
		approved = res.Match
		message = res.Message
		if approved {
			verifiedBy = providerName
		}
	}

	cert, err := a.Store.SubmitCertification(c, pr.User.ID, store.CertificationInput{
		RealName: in.RealName, IDNumber: in.IDNumber, IDType: in.IDType,
		Provider: providerName, Approved: approved, VerifiedBy: verifiedBy,
		Gender: gender, BirthDate: birth,
	})
	if err != nil {
		httpx.Fail(c, 400, "CERT_SUBMIT_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "certification.submit", "certification", cert.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": cert.Status})
	httpx.OK(c, 200, map[string]any{
		"status":           cert.Status,
		"real_name_masked": cert.RealNameMasked,
		"id_number_masked": cert.IDNumberMasked,
		"provider":         providerName,
		"message":          message,
	})
}

// certSecret 解密实名通道凭据。
func (a *App) certSecret(enc string) (certification.Secret, error) {
	if strings.TrimSpace(enc) == "" {
		return certification.Secret{}, nil
	}
	if len(a.Cfg.MasterKey) == 0 {
		return nil, errors.New("服务器未配置 MASTER_KEY_BASE64，无法解密实名通道凭据")
	}
	plain, err := security.Decrypt(a.Cfg.MasterKey, enc)
	if err != nil {
		return nil, errors.New("解密实名通道凭据失败")
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, errors.New("实名通道凭据不是合法 JSON")
	}
	return certification.Secret(out), nil
}

// certExtraFields 返回默认通道声明给前端的额外输入字段（扫码通道用它渲染表单）。
func (a *App) certExtraFields(ctx context.Context) []certification.Field {
	provider, _, err := a.Store.ActiveCertificationProvider(ctx)
	if err != nil {
		return nil
	}
	impl, ok := certification.Get(provider.Provider)
	if !ok {
		return nil
	}
	schemer, ok := impl.(certification.Schemer)
	if !ok {
		return nil
	}
	return schemer.Fields(certification.Config{Provider: provider.Provider, Fields: provider.Config})
}

// pollCertification 轮询扫码类认证的结果；前端在 pending 时每 3 秒调用一次。
func (a *App) pollCertification(c *gin.Context) {
	pr, _ := getPrincipal(c)
	cert, err := a.Store.GetCertification(c, pr.User.ID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.OK(c, 200, map[string]any{"status": "none"})
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取实名信息失败")
		return
	}
	if cert.Status != "pending" {
		// 已出终态（管理员也可能直接审核过）：直接返回结果。
		httpx.OK(c, 200, map[string]any{"status": cert.Status, "message": cert.RejectReason})
		return
	}
	if cert.ProviderRef == "" {
		// 人工审核通道没有可轮询的上游，等管理员处理。
		httpx.OK(c, 200, map[string]any{"status": "pending", "message": "等待管理员审核"})
		return
	}
	provider, secretEnc, err := a.Store.ActiveCertificationProvider(c)
	if err != nil {
		// 通道被删除/停用：保持 pending，绝不把用户误判为失败。
		httpx.OK(c, 200, map[string]any{"status": "pending", "url": cert.ProviderURL, "message": "认证通道暂不可用，请稍后刷新"})
		return
	}
	if provider.Provider != cert.Provider {
		httpx.OK(c, 200, map[string]any{"status": "pending", "url": cert.ProviderURL, "message": "默认认证通道已变更，请按原二维码完成操作"})
		return
	}
	secret, err := a.certSecret(secretEnc)
	if err != nil {
		httpx.Fail(c, 500, "CERT_SECRET_INVALID", err.Error())
		return
	}
	impl, ok := certification.Get(provider.Provider)
	if !ok {
		httpx.OK(c, 200, map[string]any{"status": "pending", "url": cert.ProviderURL})
		return
	}
	challenger, ok := impl.(certification.Challenger)
	if !ok {
		httpx.OK(c, 200, map[string]any{"status": "pending", "url": cert.ProviderURL})
		return
	}
	qCtx, cancel := context.WithTimeout(c, 15*time.Second)
	res, qerr := challenger.Query(qCtx, certification.Config{Provider: provider.Provider, Fields: provider.Config}, secret, cert.ProviderRef)
	cancel()
	if qerr != nil {
		// 查询失败不改状态：前端下一个周期会继续轮询。
		a.Store.MarkCertificationProviderHealth(c, provider.PublicID, false, qerr.Error())
		httpx.OK(c, 200, map[string]any{"status": "pending", "url": cert.ProviderURL, "message": "查询超时，稍后自动重试"})
		return
	}
	a.Store.MarkCertificationProviderHealth(c, provider.PublicID, true, "")
	if res.Pending {
		httpx.OK(c, 200, map[string]any{"status": "pending", "url": cert.ProviderURL, "message": res.Message})
		return
	}
	if err := a.Store.ResolveCertification(c, pr.User.ID, res.Match, res.Message); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// 记录已不是 pending（管理员刚处理过），下一次轮询会读到真实状态。
			httpx.OK(c, 200, map[string]any{"status": "pending"})
			return
		}
		httpx.Fail(c, 400, "CERT_RESOLVE_FAILED", err.Error())
		return
	}
	status := "rejected"
	if res.Match {
		status = "approved"
	}
	_ = a.Store.Audit(c, pr.User.ID, "certification.poll", "certification", cert.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": status, "provider": provider.Provider})
	httpx.OK(c, 200, map[string]any{"status": status, "message": res.Message})
}

// ---- 管理端：实名审核 ----

// adminListCertifications 列出实名记录，可按状态过滤（默认列待审核）。
func (a *App) adminListCertifications(c *gin.Context) {
	items, err := a.Store.ListCertifications(c, c.Query("status"), parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "CERT_LIST_FAILED", "读取实名记录失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminReviewCertification 通过或驳回一条实名记录。
func (a *App) adminReviewCertification(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	// 驳回必须写明原因：用户据此知道要改什么。
	if !in.Approve && strings.TrimSpace(in.Reason) == "" {
		httpx.Fail(c, 400, "REASON_REQUIRED", "驳回时必须填写原因")
		return
	}
	if err := a.Store.ReviewCertification(c, c.Param("id"), in.Approve, in.Reason, pr.User.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CERT_NOT_FOUND", "实名记录不存在")
			return
		}
		httpx.Fail(c, 400, "CERT_REVIEW_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "certification.review", "certification", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSetCertificationRequired 开关「下单必须实名」。
func (a *App) adminSetCertificationRequired(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Required bool `json:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	raw := "false"
	if in.Required {
		raw = "true"
	}
	if _, err := a.Store.DB.Exec(c, `INSERT INTO system_settings(key,value) VALUES('certification_required',$1::jsonb) ON CONFLICT (key) DO UPDATE SET value=$1::jsonb,updated_at=now()`, raw); err != nil {
		httpx.Fail(c, 500, "SETTING_FAILED", "保存设置失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "certification.required_set", "setting", "certification_required", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"required": in.Required})
}

// ---- 管理端：实名核验通道 ----

// adminListCertificationProviders 列出全部实名通道与可用通道实现。
func (a *App) adminListCertificationProviders(c *gin.Context) {
	items, err := a.Store.ListCertificationProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "CERT_PROVIDERS_FAILED", "读取实名通道失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"providers": items, "available": certification.Names()})
}

// adminCreateCertificationProvider 新增实名通道。
// secret 是通道凭据 JSON（如 {"app_code":"..."}），加密入库。
func (a *App) adminCreateCertificationProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Name      string            `json:"name"`
		Provider  string            `json:"provider"`
		Config    map[string]string `json:"config"`
		Secret    map[string]string `json:"secret"`
		IsDefault bool              `json:"is_default"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	if in.Name == "" || in.Provider == "" {
		httpx.Fail(c, 400, "INVALID_CERT_PROVIDER", "通道名称与类型不能为空")
		return
	}
	impl, ok := certification.Get(in.Provider)
	if !ok {
		httpx.Fail(c, 400, "CERT_PROVIDER_UNKNOWN", "未知的实名核验通道："+in.Provider)
		return
	}
	cfg := certification.Config{Provider: in.Provider, Fields: in.Config}
	secret := certification.Secret(in.Secret)
	if err := impl.Validate(cfg, secret); err != nil {
		httpx.Fail(c, 400, "CERT_PROVIDER_INVALID", err.Error())
		return
	}
	secretJSON, err := json.Marshal(secret)
	if err != nil {
		httpx.Fail(c, 400, "CERT_PROVIDER_INVALID", "凭据格式错误")
		return
	}
	enc := ""
	if len(secret) > 0 {
		if len(a.Cfg.MasterKey) == 0 {
			httpx.Fail(c, 500, "MASTER_KEY_MISSING", "服务器未配置 MASTER_KEY_BASE64，无法加密实名通道凭据")
			return
		}
		enc, err = security.Encrypt(a.Cfg.MasterKey, string(secretJSON))
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "加密实名通道凭据失败")
			return
		}
	}
	v, err := a.Store.CreateCertificationProvider(c, in.Name, in.Provider, in.Config, enc, in.IsDefault)
	if err != nil {
		httpx.Fail(c, 400, "CERT_PROVIDER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "certification_provider.create", "certification_provider", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"provider": v.Provider})
	httpx.OK(c, 201, v)
}

// adminSetDefaultCertificationProvider 把某个通道设为默认（并激活）。
func (a *App) adminSetDefaultCertificationProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.SetCertificationProviderDefault(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CERT_PROVIDER_NOT_FOUND", "实名通道不存在")
			return
		}
		httpx.Fail(c, 400, "CERT_PROVIDER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "certification_provider.set_default", "certification_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteCertificationProvider 删除实名通道。
func (a *App) adminDeleteCertificationProvider(c *gin.Context) {
	pr, _ := getPrincipal(c)
	if err := a.Store.DeleteCertificationProvider(c, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "CERT_PROVIDER_NOT_FOUND", "实名通道不存在")
			return
		}
		httpx.Fail(c, 400, "CERT_PROVIDER_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "certification_provider.delete", "certification_provider", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}
