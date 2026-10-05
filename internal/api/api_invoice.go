package api

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 发票申请（对齐魔方 CBAP 插件 IdcsmartInvoice）。
//
// 用户端：/invoice_config、/invoice_title、/invoice_address、/invoice_project、
// /invoice_request（可开票订单）、/invoice/price（试算）、/invoice（申请列表 /
// 创建 / 详情 / 作废 / 下载发票文件）。
// 后台：/admin/invoice*（列表 / 审核 / 驳回 / 发出 / 冲红 / 文件）与发票设置、
// 发票项目、抬头 / 地址查看。用户端仅在 invoice_manage 开启后可用。

// invoiceConfigOrFail 读取发票设置并校验用户端功能开关。
func (a *App) invoiceConfigOrFail(c *gin.Context) (store.InvoiceConfig, bool) {
	cfg, err := a.Store.GetInvoiceConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_FAILED", "读取发票设置失败")
		return store.InvoiceConfig{}, false
	}
	if !cfg.Manage {
		httpx.Fail(c, 403, "INVOICE_DISABLED", "发票功能未开启")
		return store.InvoiceConfig{}, false
	}
	return cfg, true
}

// myInvoiceConfig 用户端读取发票设置（插件 GET /invoice_config）。
func (a *App) myInvoiceConfig(c *gin.Context) {
	cfg, ok := a.invoiceConfigOrFail(c)
	if !ok {
		return
	}
	httpx.OK(c, 200, cfg)
}

// ---- 发票抬头 ----

func (a *App) listMyInvoiceTitles(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	items, err := a.Store.ListInvoiceTitles(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_TITLES_FAILED", "读取发票抬头失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

func (a *App) createMyInvoiceTitle(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in store.InvoiceTitleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.CreateInvoiceTitle(c, p.User.ID, in)
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_TITLE_CREATE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 201, v)
}

func (a *App) updateMyInvoiceTitle(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in store.InvoiceTitleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdateInvoiceTitle(c, p.User.ID, c.Param("id"), in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_TITLE_NOT_FOUND", "发票抬头不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_TITLE_UPDATE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) deleteMyInvoiceTitle(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	err := a.Store.DeleteInvoiceTitle(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_TITLE_NOT_FOUND", "发票抬头不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_TITLE_DELETE_FAILED", "删除发票抬头失败")
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 收件地址 ----

func (a *App) listMyInvoiceAddresses(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	items, err := a.Store.ListInvoiceAddresses(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_ADDRESSES_FAILED", "读取收件地址失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

func (a *App) createMyInvoiceAddress(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in store.InvoiceAddressInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.CreateInvoiceAddress(c, p.User.ID, in)
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_ADDRESS_CREATE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 201, v)
}

func (a *App) updateMyInvoiceAddress(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in store.InvoiceAddressInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdateInvoiceAddress(c, p.User.ID, c.Param("id"), in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_ADDRESS_NOT_FOUND", "收件地址不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_ADDRESS_UPDATE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) deleteMyInvoiceAddress(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	err := a.Store.DeleteInvoiceAddress(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_ADDRESS_NOT_FOUND", "收件地址不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_ADDRESS_DELETE_FAILED", "删除收件地址失败")
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 发票项目 / 可开票订单 / 试算 / 申请 ----

func (a *App) listMyInvoiceProjects(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	items, err := a.Store.ListInvoiceProjects(c)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_PROJECTS_FAILED", "读取发票项目失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

// listMyInvoiceRequestableOrders 返回可开票订单（插件 GET /invoice_request）。
func (a *App) listMyInvoiceRequestableOrders(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	items, err := a.Store.InvoiceRequestableOrders(c, p.User.ID, c.Query("status"))
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_ORDERS_FAILED", "读取可开票订单失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

// quoteMyInvoice 试算需支付的税金 / 快递费（插件 POST /invoice/price）。
func (a *App) quoteMyInvoice(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in store.InvoiceQuoteInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.InvoiceQuote(c, p.User.ID, in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_QUOTE_NOT_FOUND", "订单或发票项目不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_QUOTE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) listMyInvoiceRequests(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	items, total, err := a.Store.ListUserInvoiceRequests(c, p.User.ID, c.Query("status"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_REQUESTS_FAILED", "读取发票申请失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items, "count": total, "page": page, "limit": limit})
}

func (a *App) createMyInvoiceRequest(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in store.InvoiceCreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.CreateInvoiceRequest(c, p.User.ID, in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_CREATE_NOT_FOUND", "订单 / 抬头 / 收件地址 / 发票项目不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_CREATE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 201, v)
}

func (a *App) getMyInvoiceRequest(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	v, err := a.Store.GetInvoiceRequest(c, p.User.ID, c.Param("id"), false)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_REQUEST_NOT_FOUND", "发票申请不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_REQUEST_FAILED", "读取发票申请失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) cancelMyInvoiceRequest(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	err := a.Store.CancelInvoiceRequest(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_REQUEST_NOT_FOUND", "发票申请不存在")
		return
	}
	if errors.Is(err, store.ErrInvalidState) {
		httpx.Fail(c, 400, "INVOICE_CANCEL_STATE", "该状态的发票申请不可作废")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_CANCEL_FAILED", "作废发票申请失败")
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// myInvoiceFile 用户下载自己的发票文件（插件 GET /invoice/:id/invoice_filename）。
func (a *App) myInvoiceFile(c *gin.Context) {
	if _, ok := a.invoiceConfigOrFail(c); !ok {
		return
	}
	p, _ := getPrincipal(c)
	ref, err := a.Store.InvoiceRequestFilename(c, p.User.ID, c.Param("id"), false)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_FILE_NOT_FOUND", "发票文件不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_FILE_FAILED", "读取发票文件失败")
		return
	}
	a.invoiceServeFile(c, ref, "invoice"+filepath.Ext(ref))
}

// ---- 后台发票管理 ----

func (a *App) adminListInvoiceRequests(c *gin.Context) {
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	items, total, err := a.Store.ListAdminInvoiceRequests(c, c.Query("status"), c.Query("keywords"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_REQUESTS_FAILED", "读取发票申请失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items, "count": total, "page": page, "limit": limit})
}

func (a *App) adminGetInvoiceRequest(c *gin.Context) {
	v, err := a.Store.GetInvoiceRequest(c, 0, c.Param("id"), true)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_REQUEST_NOT_FOUND", "发票申请不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_REQUEST_FAILED", "读取发票申请失败")
		return
	}
	httpx.OK(c, 200, v)
}

// adminInvoiceActionError 统一映射后台发票操作的存储层错误。
func (a *App) adminInvoiceActionError(c *gin.Context, code string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, code, "发票申请不存在")
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, code, "当前状态不允许该操作")
	default:
		httpx.Fail(c, 400, code, err.Error())
	}
}

// adminConfirmInvoiceRequest 审核通过：pending → wait_send。
func (a *App) adminConfirmInvoiceRequest(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ReviewNotes string `json:"review_notes"`
	}
	_ = c.ShouldBindJSON(&in)
	if err := a.Store.ConfirmInvoiceRequest(c, c.Param("id"), in.ReviewNotes); err != nil {
		a.adminInvoiceActionError(c, "INVOICE_CONFIRM_FAILED", err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.confirm", "invoice_request", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminRejectInvoiceRequest 驳回：pending / wait_send → reject。
func (a *App) adminRejectInvoiceRequest(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.RejectInvoiceRequest(c, c.Param("id"), in.Reason); err != nil {
		a.adminInvoiceActionError(c, "INVOICE_REJECT_FAILED", err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.reject", "invoice_request", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminSendInvoiceRequest 发出：wait_send → sent。
// 支持 JSON（parcel_number）或 multipart（parcel_number + 快递单照片 file，可选）。
func (a *App) adminSendInvoiceRequest(c *gin.Context) {
	p, _ := getPrincipal(c)
	publicID := c.Param("id")
	parcelNumber := ""
	if strings.Contains(c.ContentType(), "json") {
		var in struct {
			ParcelNumber string `json:"parcel_number"`
		}
		_ = c.ShouldBindJSON(&in)
		parcelNumber = in.ParcelNumber
	} else {
		parcelNumber = c.PostForm("parcel_number")
	}
	parcelImage := ""
	if fh, ferr := c.FormFile("file"); ferr == nil && fh != nil {
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if !inspectionImageExts[ext] {
			httpx.Fail(c, 400, "FILE_TYPE_REJECTED", "快递单照片仅支持 png / jpg / jpeg / webp")
			return
		}
		if fh.Size > 5<<20 {
			httpx.Fail(c, 400, "FILE_TOO_LARGE", "快递单照片不能超过 5 MB")
			return
		}
		f, oerr := fh.Open()
		if oerr != nil {
			httpx.Fail(c, 400, "FILE_INVALID", "文件读取失败")
			return
		}
		content, rerr := io.ReadAll(io.LimitReader(f, 5<<20))
		f.Close()
		if rerr != nil || len(content) == 0 {
			httpx.Fail(c, 400, "FILE_INVALID", "文件内容为空")
			return
		}
		if !attachmentMagic(fh.Filename, content) {
			httpx.Fail(c, 400, "FILE_CONTENT_REJECTED", "文件内容与扩展名不符")
			return
		}
		ref, uerr := a.invoiceStoreUpload(c, publicID, ext, content)
		if uerr != nil {
			httpx.Fail(c, 500, "FILE_SAVE_FAILED", "快递单照片保存失败")
			return
		}
		parcelImage = ref
	}
	if err := a.Store.SendInvoiceRequest(c, publicID, parcelNumber, parcelImage); err != nil {
		a.adminInvoiceActionError(c, "INVOICE_SEND_FAILED", err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.send", "invoice_request", publicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminFlushInvoiceRequest 冲红：sent → flushed。
func (a *App) adminFlushInvoiceRequest(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.FlushInvoiceRequest(c, c.Param("id")); err != nil {
		a.adminInvoiceActionError(c, "INVOICE_FLUSH_FAILED", err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.flush", "invoice_request", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

var invoiceFileExts = map[string]bool{".pdf": true, ".ofd": true, ".xml": true, ".zip": true}

// adminUploadInvoiceFile 上传电子发票文件（插件 POST /invoice/:id/upload）。
func (a *App) adminUploadInvoiceFile(c *gin.Context) {
	p, _ := getPrincipal(c)
	publicID := c.Param("id")
	if _, err := a.Store.GetInvoiceRequest(c, 0, publicID, true); err != nil {
		httpx.Fail(c, 404, "INVOICE_REQUEST_NOT_FOUND", "发票申请不存在")
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, 400, "FILE_REQUIRED", "请选择要上传的发票文件")
		return
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if !invoiceFileExts[ext] {
		httpx.Fail(c, 400, "FILE_TYPE_REJECTED", "发票文件仅支持 pdf / ofd / xml / zip")
		return
	}
	if fh.Size > 10<<20 {
		httpx.Fail(c, 400, "FILE_TOO_LARGE", "发票文件不能超过 10 MB")
		return
	}
	f, err := fh.Open()
	if err != nil {
		httpx.Fail(c, 400, "FILE_INVALID", "文件读取失败")
		return
	}
	content, err := io.ReadAll(io.LimitReader(f, 10<<20))
	f.Close()
	if err != nil || len(content) == 0 {
		httpx.Fail(c, 400, "FILE_INVALID", "文件内容为空")
		return
	}
	if ext == ".ofd" {
		if len(content) < 2 || content[0] != 'P' || content[1] != 'K' {
			httpx.Fail(c, 400, "FILE_CONTENT_REJECTED", "OFD 文件内容校验失败")
			return
		}
	} else if !attachmentMagic(fh.Filename, content) {
		httpx.Fail(c, 400, "FILE_CONTENT_REJECTED", "文件内容与扩展名不符")
		return
	}
	ref, err := a.invoiceStoreUpload(c, publicID, ext, content)
	if err != nil {
		httpx.Fail(c, 500, "FILE_SAVE_FAILED", "发票文件保存失败")
		return
	}
	if err := a.Store.SetInvoiceRequestFile(c, publicID, ref); err != nil {
		a.adminInvoiceActionError(c, "INVOICE_UPLOAD_FAILED", err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.upload", "invoice_request", publicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 201, gin.H{"invoice_filename": ref, "name": filepath.Base(fh.Filename)})
}

// adminDeleteInvoiceFile 删除发票文件（插件 DELETE /invoice/:id/invoice_filename）。
func (a *App) adminDeleteInvoiceFile(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteInvoiceRequestFile(c, c.Param("id")); err != nil {
		a.adminInvoiceActionError(c, "INVOICE_FILE_DELETE_FAILED", err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.file_delete", "invoice_request", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminInvoiceParcelImage 后台查看快递单照片。
func (a *App) adminInvoiceParcelImage(c *gin.Context) {
	ref, err := a.Store.InvoiceRequestParcelImage(c, 0, c.Param("id"), true)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_PARCEL_IMAGE_NOT_FOUND", "快递单照片不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_PARCEL_IMAGE_FAILED", "读取快递单照片失败")
		return
	}
	a.invoiceServeFile(c, ref, "parcel"+filepath.Ext(ref))
}

// adminInvoiceFile 后台下载发票文件。
func (a *App) adminInvoiceFile(c *gin.Context) {
	ref, err := a.Store.InvoiceRequestFilename(c, 0, c.Param("id"), true)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_FILE_NOT_FOUND", "发票文件不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_FILE_FAILED", "读取发票文件失败")
		return
	}
	a.invoiceServeFile(c, ref, "invoice"+filepath.Ext(ref))
}

// invoiceStoreUpload 保存发票相关文件：对象存储优先，否则落本机 uploads/invoices/<sub>/。
// 返回相对 uploads/invoices 的引用；oss: 前缀表示对象存储。
func (a *App) invoiceStoreUpload(c *gin.Context, sub, ext string, content []byte) (string, error) {
	stored := uuid.NewString() + ext
	key := "uploads/invoices/" + sub + "/" + stored
	if target, oerr := a.activeOSSTarget(c); oerr == nil && target != nil {
		if uerr := target.impl.Upload(c, target.cfg, target.secret, key, content, mime.TypeByExtension(ext), "private"); uerr == nil {
			return "oss:" + key, nil
		}
	}
	dir := filepath.Join(a.Cfg.Storage.Dir, "uploads", "invoices", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, stored), content, 0o644); err != nil {
		return "", err
	}
	return sub + "/" + stored, nil
}

// invoiceServeFile 输出发票文件：对象存储 302 到签名地址，本机文件强制下载。
func (a *App) invoiceServeFile(c *gin.Context, ref, downloadName string) {
	if strings.HasPrefix(ref, "oss:") {
		key := strings.TrimPrefix(ref, "oss:")
		target, oerr := a.activeOSSTarget(c)
		if oerr != nil || target == nil {
			httpx.Fail(c, 502, "INVOICE_FILE_UNAVAILABLE", "对象存储通道不可用")
			return
		}
		signed, serr := target.impl.SignedURL(c, target.cfg, target.secret, key, 3*time.Minute)
		if serr != nil {
			httpx.Fail(c, 502, "INVOICE_FILE_UNAVAILABLE", "生成下载地址失败")
			return
		}
		c.Redirect(http.StatusFound, signed)
		return
	}
	full := filepath.Join(a.Cfg.Storage.Dir, "uploads", "invoices", filepath.Clean(ref))
	if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(filepath.Join(a.Cfg.Storage.Dir, "uploads"))+string(os.PathSeparator)) {
		httpx.Fail(c, 400, "INVOICE_FILE_INVALID", "文件路径非法")
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		httpx.Fail(c, 404, "INVOICE_FILE_NOT_FOUND", "发票文件已丢失")
		return
	}
	c.Header("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(downloadName, "\"", "_")+"\"")
	c.Data(http.StatusOK, "application/octet-stream", data)
}

// ---- 发票设置 / 项目 / 抬头 / 地址（后台） ----

// adminGetInvoiceConfig 读取发票设置（插件 GET /invoice_config）。
func (a *App) adminGetInvoiceConfig(c *gin.Context) {
	cfg, err := a.Store.GetInvoiceConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_CONFIG_FAILED", "读取发票设置失败")
		return
	}
	httpx.OK(c, 200, cfg)
}

// adminSaveInvoiceConfig 保存发票设置（插件 PUT /invoice_config）。
func (a *App) adminSaveInvoiceConfig(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in store.InvoiceConfig
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SaveInvoiceConfig(c, in); err != nil {
		httpx.Fail(c, 400, "INVOICE_CONFIG_FAILED", "保存发票设置失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.config", "invoice_config", "", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminListInvoiceProjects 发票项目列表（插件 GET /invoice_project）。
func (a *App) adminListInvoiceProjects(c *gin.Context) {
	items, err := a.Store.ListInvoiceProjects(c)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_PROJECTS_FAILED", "读取发票项目失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

// adminCreateInvoiceProject 新增发票项目（插件 POST /invoice_project）。
func (a *App) adminCreateInvoiceProject(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in store.InvoiceProjectInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.CreateInvoiceProject(c, in)
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_PROJECT_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.project_create", "invoice_project", v.ID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 201, v)
}

// adminUpdateInvoiceProject 修改发票项目（插件 PUT /invoice_project/:id）。
func (a *App) adminUpdateInvoiceProject(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in store.InvoiceProjectInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdateInvoiceProject(c, c.Param("id"), in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_PROJECT_NOT_FOUND", "发票项目不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_PROJECT_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.project_update", "invoice_project", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminDeleteInvoiceProject 删除发票项目（插件 DELETE /invoice_project/:id）。
func (a *App) adminDeleteInvoiceProject(c *gin.Context) {
	p, _ := getPrincipal(c)
	err := a.Store.DeleteInvoiceProject(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INVOICE_PROJECT_NOT_FOUND", "发票项目不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_PROJECT_DELETE_FAILED", "删除发票项目失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.project_delete", "invoice_project", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminListInvoiceTitles 后台查看用户发票抬头（插件 GET /invoice_title）。
func (a *App) adminListInvoiceTitles(c *gin.Context) {
	items, err := a.Store.ListAllInvoiceTitles(c, c.Query("keywords"), 200)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_TITLES_FAILED", "读取发票抬头失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

// adminBatchDeleteInvoiceTitles 后台批量删除抬头（插件 DELETE /invoice_title）。
func (a *App) adminBatchDeleteInvoiceTitles(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	n, err := a.Store.DeleteInvoiceTitles(c, in.IDs)
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_TITLE_DELETE_FAILED", "删除发票抬头失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.title_delete", "invoice_title", strings.Join(in.IDs, ","), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"deleted": n})
}

// adminListInvoiceAddresses 后台查看用户收件地址（插件 GET /invoice_address）。
func (a *App) adminListInvoiceAddresses(c *gin.Context) {
	items, err := a.Store.ListAllInvoiceAddresses(c, c.Query("keywords"), 200)
	if err != nil {
		httpx.Fail(c, 500, "INVOICE_ADDRESSES_FAILED", "读取收件地址失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items})
}

// adminBatchDeleteInvoiceAddresses 后台批量删除收件地址（插件 DELETE /invoice_address）。
func (a *App) adminBatchDeleteInvoiceAddresses(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	n, err := a.Store.DeleteInvoiceAddresses(c, in.IDs)
	if err != nil {
		httpx.Fail(c, 400, "INVOICE_ADDRESS_DELETE_FAILED", "删除收件地址失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "invoice.address_delete", "invoice_address", strings.Join(in.IDs, ","), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"deleted": n})
}
