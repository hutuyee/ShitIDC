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
		httpx.OK(c, 200, map[string]any{"status": "none", "required": required})
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
		"reviewed_at":      cert.ReviewedAt,
	})
}

// submitCertification 提交实名认证。
func (a *App) submitCertification(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		RealName string `json:"real_name"`
		IDNumber string `json:"id_number"`
		IDType   string `json:"id_type"`
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

	approved := false
	verifiedBy := ""
	message := ""
	if impl, ok := certification.Get(providerName); ok {
		verifyCtx, cancel := context.WithTimeout(c, 15*time.Second)
		res, verr := impl.Verify(verifyCtx, cfg, secret, certification.Subject{
			RealName: in.RealName, IDNumber: in.IDNumber, IDType: in.IDType,
		})
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

	// 性别与出生日期本地解出来（身份证前 18 位自带这些信息）。
	gender := ""
	birth := ""
	if in.IDType == "idcard" {
		gender = certification.GenderFromIDCard(in.IDNumber)
		birth = certification.BirthDateFromIDCard(in.IDNumber)
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
