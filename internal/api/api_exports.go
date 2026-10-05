package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
	"github.com/hutuyee/ShitIDC/internal/xlsx"
)

// ---- 导出中心（对齐魔方附属插件 export_excel）----
//
// 参考插件：后台维护「自定义名称 + 导出列表 + 参数字段」，按时间区间导出
// Excel（PhpSpreadsheet）。这里用纯标准库的 internal/xlsx 生成等价的最小
// xlsx；两个内置列表见 store.ExportDatasets。

func (a *App) adminListExportDatasets(c *gin.Context) {
	httpx.OK(c, 200, store.ExportDatasets())
}

func (a *App) adminListExportConfigs(c *gin.Context) {
	v, err := a.Store.ListExportConfigs(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取导出列表失败")
		return
	}
	httpx.OK(c, 200, v)
}

type exportConfigInput struct {
	CustomName string   `json:"custom_name"`
	Dataset    string   `json:"dataset"`
	Columns    []string `json:"columns"`
}

func (a *App) adminCreateExportConfig(c *gin.Context) {
	var in exportConfigInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	name := strings.TrimSpace(in.CustomName)
	if name == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写自定义名称")
		return
	}
	cols, err := store.ValidateExportColumns(in.Dataset, in.Columns)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", err.Error())
		return
	}
	v, err := a.Store.CreateExportConfig(c, name, strings.TrimSpace(in.Dataset), cols)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存导出列表失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminUpdateExportConfig(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "导出列表 ID 无效")
		return
	}
	var in exportConfigInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	name := strings.TrimSpace(in.CustomName)
	if name == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写自定义名称")
		return
	}
	cols, err := store.ValidateExportColumns(in.Dataset, in.Columns)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", err.Error())
		return
	}
	v, err := a.Store.UpdateExportConfig(c, id, name, strings.TrimSpace(in.Dataset), cols)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "NOT_FOUND", "导出列表不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存导出列表失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminDeleteExportConfig(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "导出列表 ID 无效")
		return
	}
	err = a.Store.DeleteExportConfig(c, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "NOT_FOUND", "导出列表不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "删除导出列表失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"deleted": true})
}

// adminDownloadExport renders one saved list as an .xlsx attachment. The
// optional start/end query parameters (YYYY-MM-DD) bound 收款/结算时间.
func (a *App) adminDownloadExport(c *gin.Context) {
	id, err := strconv.ParseInt(c.Query("config_id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "导出列表 ID 无效")
		return
	}
	cfg, err := a.Store.GetExportConfig(c, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "NOT_FOUND", "导出列表不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取导出列表失败")
		return
	}
	dataset, ok := store.ExportDatasetByKey(cfg.Dataset)
	if !ok {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "导出列表对应的数据集已失效")
		return
	}
	from, err := parseExportDate(c.Query("start"))
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", err.Error())
		return
	}
	to, err := parseExportDate(c.Query("end"))
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", err.Error())
		return
	}
	if to != nil {
		// 结束日期含当天：右边界取次日零点（查询用 <）。
		next := to.AddDate(0, 0, 1)
		to = &next
	}
	rows, err := a.Store.ExportDatasetRows(c, cfg.Dataset, from, to, 5000)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取导出数据失败")
		return
	}
	selected := make([]store.ExportColumn, 0, len(cfg.Columns))
	for _, key := range cfg.Columns {
		for _, col := range dataset.Columns {
			if col.Key == key {
				selected = append(selected, col)
			}
		}
	}
	sheet := make([][]xlsx.Cell, 0, len(rows)+1)
	header := make([]xlsx.Cell, 0, len(selected))
	for _, col := range selected {
		header = append(header, xlsx.Text(col.Label))
	}
	sheet = append(sheet, header)
	for _, row := range rows {
		line := make([]xlsx.Cell, 0, len(selected))
		for _, col := range selected {
			line = append(line, exportCell(col, row[col.Key]))
		}
		sheet = append(sheet, line)
	}
	body, err := xlsx.Write(sheet)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成 Excel 失败")
		return
	}
	c.Header("Content-Disposition", `attachment; filename="export-`+cfg.Dataset+`-`+time.Now().Format("20060102150405")+`.xlsx"`)
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", body)
}

func parseExportDate(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return nil, errors.New("日期格式应为 YYYY-MM-DD")
	}
	return &t, nil
}

// exportCell formats one dataset value for the sheet: money as a number
// (yuan, 2 decimals), timestamps as text, everything else passthrough.
func exportCell(col store.ExportColumn, v any) xlsx.Cell {
	if col.Money {
		return xlsx.Number(float64(exportInt64(v)) / 100)
	}
	if col.Time {
		switch t := v.(type) {
		case time.Time:
			if t.IsZero() {
				return xlsx.Text("")
			}
			return xlsx.Text(t.Format("2006-01-02 15:04:05"))
		case *time.Time:
			if t == nil || t.IsZero() {
				return xlsx.Text("")
			}
			return xlsx.Text(t.Format("2006-01-02 15:04:05"))
		}
		return xlsx.Text("")
	}
	s, _ := v.(string)
	return xlsx.Text(s)
}

func exportInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case float64:
		return int64(t)
	}
	return 0
}

// ---- 到期产品删除IP记录（对齐魔方附属插件 expired_ip_log）----

func (a *App) adminListExpiredIPLogs(c *gin.Context) {
	v, err := a.Store.ListExpiredIPLogs(c, c.Query("query"), parseIntDefault(c.Query("limit"), 200))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取到期IP记录失败")
		return
	}
	httpx.OK(c, 200, v)
}
