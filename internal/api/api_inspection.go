package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
	"github.com/hutuyee/ShitIDC/internal/xlsx"
)

// 异常巡查记录（对齐魔方 CBAP 插件 abnormal_inspection_records）。
//
// 契约对照插件前端 template/admin/api/index.js：
//   GET/POST        /abnormal_inspection_records
//   PUT/DELETE      /abnormal_inspection_records/:id
//   GET             /abnormal_inspection_records/export_excel（xlsx 附件）
// 列表参数 start_time/end_time/keywords/page/limit/orderby/sort 同名兼容；
// 时间字段按插件口径输出 Unix 秒。截图存取走本站上传通道（本地 + 可选对象存储）。

// inspectionView 把一条记录转成插件契约的字段形状（时间转 Unix 秒，截图附 URL）。
func inspectionView(rec store.AbnormalInspectionRecord) gin.H {
	imgs := make([]gin.H, 0, len(rec.Images))
	for i, img := range rec.Images {
		imgs = append(imgs, gin.H{"name": img.Name, "url": fmt.Sprintf("/api/v1/admin/abnormal-inspection-records/%s/images/%d", rec.PublicID, i), "stored": img.Stored})
	}
	var payTime any
	if rec.PayTime != nil {
		payTime = rec.PayTime.Unix()
	}
	return gin.H{
		"id": rec.PublicID, "client_id": rec.ClientID, "username": rec.Username, "company": rec.Company,
		"email": rec.Email, "phone": rec.Phone, "phone_code": rec.PhoneCode,
		"host_id": rec.HostID, "product_name": rec.ProductName, "host_name": rec.HostName,
		"order_id": rec.OrderID, "pay_time": payTime,
		"ip": rec.IP, "matter": rec.Matter, "measure": rec.Measure, "process_time": rec.ProcessTime.Unix(),
		"img": imgs, "admin_name": rec.AdminName, "created_at": rec.CreatedAt, "updated_at": rec.UpdatedAt,
	}
}

// parseInspectionQueryTime 解析列表时间参数：支持 Unix 秒与 RFC3339。
func parseInspectionQueryTime(raw string) *time.Time {
	return parseCostPayTime(raw)
}

// inspectionBody 是新增/修改记录的请求体；img 兼容 ["文件名"] 与 [{stored,name}] 两种形状。
type inspectionBody struct {
	ClientID    string `json:"client_id"`
	HostID      string `json:"host_id"`
	IP          string `json:"ip"`
	Matter      string `json:"matter"`
	Measure     string `json:"measure"`
	ProcessTime any    `json:"process_time"`
	Img         []any  `json:"img"`
}

func (in *inspectionBody) toInput() (store.AbnormalInspectionInput, error) {
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.HostID = strings.TrimSpace(in.HostID)
	in.IP = strings.TrimSpace(in.IP)
	in.Matter = strings.TrimSpace(in.Matter)
	in.Measure = strings.TrimSpace(in.Measure)
	if in.ClientID == "" || in.HostID == "" {
		return store.AbnormalInspectionInput{}, errors.New("必须选择用户与关联产品")
	}
	if parsed := net.ParseIP(in.IP); parsed == nil || parsed.To4() == nil {
		return store.AbnormalInspectionInput{}, errors.New("ip格式为xxx.xxx.xxx.xxx")
	}
	if in.Matter == "" || utf8.RuneCountInString(in.Matter) > 2000 {
		return store.AbnormalInspectionInput{}, errors.New("异常事项不能为空且不超过 2000 字")
	}
	if in.Measure == "" || utf8.RuneCountInString(in.Measure) > 2000 {
		return store.AbnormalInspectionInput{}, errors.New("处理措施不能为空且不超过 2000 字")
	}
	processTime, ok := bodyCostPayTime(in.ProcessTime)
	if !ok {
		return store.AbnormalInspectionInput{}, errors.New("请选择处理时间")
	}
	images := []store.InspectionImage{}
	for _, raw := range in.Img {
		switch x := raw.(type) {
		case string:
			name := filepath.Base(strings.TrimSpace(x))
			images = append(images, store.InspectionImage{Stored: name, Name: name})
		case map[string]any:
			stored, _ := x["stored"].(string)
			name, _ := x["name"].(string)
			images = append(images, store.InspectionImage{Stored: strings.TrimSpace(stored), Name: strings.TrimSpace(name)})
		}
	}
	return store.AbnormalInspectionInput{
		ClientPublicID: in.ClientID, ServicePublicID: in.HostID, IP: in.IP,
		Matter: in.Matter, Measure: in.Measure, ProcessTime: processTime, Images: images,
	}, nil
}

// adminListInspectionRecords 分页列出异常巡查记录。
func (a *App) adminListInspectionRecords(c *gin.Context) {
	f := store.AbnormalInspectionFilter{
		Keywords:  strings.TrimSpace(c.Query("keywords")),
		StartTime: parseInspectionQueryTime(c.Query("start_time")),
		EndTime:   parseInspectionQueryTime(c.Query("end_time")),
		Page:      parseIntDefault(c.Query("page"), 1),
		Limit:     parseIntDefault(c.Query("limit"), 20),
	}
	list, count, err := a.Store.ListAbnormalInspectionRecords(c, f)
	if err != nil {
		httpx.Fail(c, 500, "INSPECTION_FAILED", "读取异常巡查记录失败")
		return
	}
	items := make([]gin.H, 0, len(list))
	for _, rec := range list {
		items = append(items, inspectionView(rec))
	}
	httpx.OK(c, 200, gin.H{"list": items, "count": count})
}

// adminCreateInspectionRecord 新增异常巡查记录。
func (a *App) adminCreateInspectionRecord(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var raw inspectionBody
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in, err := raw.toInput()
	if err != nil {
		httpx.Fail(c, 400, "INSPECTION_INVALID", err.Error())
		return
	}
	rec, err := a.Store.CreateAbnormalInspectionRecord(c, pr.User.ID, in)
	if err != nil {
		httpx.Fail(c, 400, "INSPECTION_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "abnormal_inspection.create", "abnormal_inspection", rec.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"client_id": rec.ClientID, "host_id": rec.HostID, "ip": rec.IP})
	httpx.OK(c, 201, inspectionView(rec))
}

// adminUpdateInspectionRecord 修改异常巡查记录。
func (a *App) adminUpdateInspectionRecord(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var raw inspectionBody
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in, err := raw.toInput()
	if err != nil {
		httpx.Fail(c, 400, "INSPECTION_INVALID", err.Error())
		return
	}
	err = a.Store.UpdateAbnormalInspectionRecord(c, c.Param("id"), pr.User.ID, in)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INSPECTION_NOT_FOUND", "记录不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "INSPECTION_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "abnormal_inspection.update", "abnormal_inspection", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil,
		map[string]any{"ip": in.IP, "process_time": in.ProcessTime})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminDeleteInspectionRecord 删除异常巡查记录。
func (a *App) adminDeleteInspectionRecord(c *gin.Context) {
	pr, _ := getPrincipal(c)
	err := a.Store.DeleteAbnormalInspectionRecord(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "INSPECTION_NOT_FOUND", "记录不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INSPECTION_DELETE_FAILED", "删除失败")
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "abnormal_inspection.delete", "abnormal_inspection", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminExportInspectionRecords 导出异常巡查记录为 xlsx（对齐插件 export_excel）。
func (a *App) adminExportInspectionRecords(c *gin.Context) {
	f := store.AbnormalInspectionFilter{
		Keywords:  strings.TrimSpace(c.Query("keywords")),
		StartTime: parseInspectionQueryTime(c.Query("start_time")),
		EndTime:   parseInspectionQueryTime(c.Query("end_time")),
	}
	list, err := a.Store.ListAbnormalInspectionRecordsForExport(c, f)
	if err != nil {
		httpx.Fail(c, 500, "INSPECTION_FAILED", "读取异常巡查记录失败")
		return
	}
	fmtTime := func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }
	sheet := [][]xlsx.Cell{{
		xlsx.Text("用户"), xlsx.Text("公司"), xlsx.Text("联系方式"), xlsx.Text("订单ID"), xlsx.Text("购买时间"),
		xlsx.Text("异常时IP"), xlsx.Text("异常事项"), xlsx.Text("处理措施"), xlsx.Text("处理时间"), xlsx.Text("最新提交人"),
	}}
	for _, rec := range list {
		contact := rec.Phone
		if contact == "" {
			contact = rec.Email
		} else if rec.Email != "" {
			contact = contact + " / " + rec.Email
		}
		payTime := "--"
		if rec.PayTime != nil {
			payTime = fmtTime(*rec.PayTime)
		}
		sheet = append(sheet, []xlsx.Cell{
			xlsx.Text(rec.Username), xlsx.Text(rec.Company), xlsx.Text(contact), xlsx.Text(rec.OrderID), xlsx.Text(payTime),
			xlsx.Text(rec.IP), xlsx.Text(rec.Matter), xlsx.Text(rec.Measure), xlsx.Text(fmtTime(rec.ProcessTime)), xlsx.Text(rec.AdminName),
		})
	}
	body, err := xlsx.Write(sheet)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成 Excel 失败")
		return
	}
	name := "异常巡查记录-" + time.Now().Format("20060102150405") + ".xlsx"
	c.Header("Content-Disposition", `attachment; filename="inspection-records-`+time.Now().Format("20060102150405")+`.xlsx"; filename*=UTF-8''`+urlEncodeFilename(name))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", body)
}

// inspectionImageExts 是允许的截图类型（插件 accept=image/*，这里收紧到常见位图格式）。
var inspectionImageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true}

// urlEncodeFilename 生成 RFC 5987 的中文文件名编码。
func urlEncodeFilename(name string) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(name)*3)
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '-' || b == '_' {
			out = append(out, b)
			continue
		}
		out = append(out, '%', hex[b>>4], hex[b&0x0f])
	}
	return string(out)
}

// adminUploadInspectionImage 上传一张异常截图（multipart 字段 file）。
// 配了对象存储通道就转存 OSS（记 oss: 前缀），否则留在本机 uploads/inspection/。
func (a *App) adminUploadInspectionImage(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, 400, "FILE_REQUIRED", "请选择要上传的截图")
		return
	}
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if !inspectionImageExts[ext] {
		httpx.Fail(c, 400, "FILE_TYPE_REJECTED", "截图仅支持 png / jpg / jpeg / webp / gif")
		return
	}
	if fileHeader.Size > 5<<20 {
		httpx.Fail(c, 400, "FILE_TOO_LARGE", "截图不能超过 5 MB")
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		httpx.Fail(c, 400, "FILE_INVALID", "文件读取失败")
		return
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, 5<<20))
	if err != nil || len(content) == 0 {
		httpx.Fail(c, 400, "FILE_INVALID", "文件内容为空")
		return
	}
	if !attachmentMagic(fileHeader.Filename, content) {
		httpx.Fail(c, 400, "FILE_CONTENT_REJECTED", "文件内容与扩展名不符")
		return
	}
	stored := uuid.NewString() + ext
	key := "uploads/inspection/" + stored
	name := filepath.Base(fileHeader.Filename)
	if utf8.RuneCountInString(name) > 128 {
		name = "截图" + ext
	}
	// 对象存储可用时只留 OSS 一份；否则写本机。
	if target, oerr := a.activeOSSTarget(c); oerr == nil && target != nil {
		if uerr := target.impl.Upload(c, target.cfg, target.secret, key, content, mime.TypeByExtension(ext), "public-read"); uerr == nil {
			httpx.OK(c, 201, gin.H{"stored": "oss:" + key, "name": name})
			return
		}
	}
	dir := filepath.Join(a.Cfg.Storage.Dir, "uploads", "inspection")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "存储目录创建失败")
		return
	}
	if err := os.WriteFile(filepath.Join(dir, stored), content, 0o644); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "截图保存失败")
		return
	}
	httpx.OK(c, 201, gin.H{"stored": stored, "name": name})
}

// adminInspectionImage 读取一条记录的第 index 张截图（本地文件或 302 到签名地址）。
func (a *App) adminInspectionImage(c *gin.Context) {
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 0 {
		httpx.Fail(c, 404, "IMAGE_NOT_FOUND", "截图不存在")
		return
	}
	img, err := a.Store.GetAbnormalInspectionImage(c, c.Param("id"), index)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "IMAGE_NOT_FOUND", "截图不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INSPECTION_FAILED", "读取截图失败")
		return
	}
	if strings.HasPrefix(img.Stored, "oss:") {
		key := strings.TrimPrefix(img.Stored, "oss:")
		target, oerr := a.activeOSSTarget(c)
		if oerr != nil || target == nil {
			httpx.Fail(c, 502, "IMAGE_UNAVAILABLE", "对象存储通道不可用")
			return
		}
		signed, serr := target.impl.SignedURL(c, target.cfg, target.secret, key, 3*time.Minute)
		if serr != nil {
			httpx.Fail(c, 502, "IMAGE_UNAVAILABLE", "生成截图地址失败")
			return
		}
		c.Redirect(http.StatusFound, signed)
		return
	}
	if !store.ValidInspectionStoredFileName(img.Stored) {
		httpx.Fail(c, 400, "IMAGE_INVALID", "截图路径非法")
		return
	}
	full := filepath.Join(a.Cfg.Storage.Dir, "uploads", "inspection", img.Stored)
	data, rerr := os.ReadFile(full)
	if rerr != nil {
		httpx.Fail(c, 404, "IMAGE_NOT_FOUND", "截图文件已丢失")
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, mime.TypeByExtension(filepath.Ext(img.Stored)), data)
}
