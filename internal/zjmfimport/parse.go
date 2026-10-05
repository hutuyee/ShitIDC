package zjmfimport

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ParseResult 是一次转换的结果。
type ParseResult struct {
	Spec *Spec
}

var actionFuncNames = map[string]string{ // spec 动作 → 模块函数名后缀
	ActionTest:          "TestLink",
	ActionCreate:        "CreateAccount",
	ActionSuspend:       "SuspendAccount",
	ActionUnsuspend:     "UnsuspendAccount",
	ActionTerminate:     "TerminateAccount",
	ActionRenew:         "Renew",
	ActionChangePackage: "ChangePackage",
	ActionSync:          "Sync",
	ActionStatus:        "Status",
	ActionPassword:      "CrackPassword",
}

var (
	reFunc        = regexp.MustCompile(`(?i)\bfunction\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
	reDataKeys    = regexp.MustCompile(`\$data\[\s*'([^']+)'\s*\]`)
	reSignCall    = regexp.MustCompile(`(?i)_CreateSign\s*\([^)]*\$params\[\s*'([^']+)'\s*\]`)
	reCodeCheck   = regexp.MustCompile(`\$res\s*\[\s*'(code|result)'\s*\]\s*==\s*'?(\d+)'?`)
	reMsgField    = regexp.MustCompile(`\$res\s*\[\s*'(msg|message|info)'\s*\]`)
	reQuotedPath  = regexp.MustCompile(`['"](\/[A-Za-z0-9_\-\.\/]+)['"]`)
	reGetUrlPath  = regexp.MustCompile(`(?i)GetUrl\s*\(\s*\$params\s*,\s*['"]([^'"]+)['"]`)
	reActionVarR  = regexp.MustCompile(`\$r\s*(?:=|\.=)\s*['"]([^'"]+)['"]`)
	reRespIDPath  = regexp.MustCompile(`\$res\[\s*'data'\s*\]((?:\[\s*'\w+'\s*\])*)\[\s*'id'\s*\]`)
	reStatusField = regexp.MustCompile(`\$res\[\s*'data'\s*\]\[\s*'(\w+)'\s*\]`)

	// 请求体赋值：$post_data['k'] = <表达式>。表达式按可识别度映射成模板变量。
	reAssign = regexp.MustCompile(`\$(?:post_data|datas|data)\[\s*'([^']+)'\s*\]\s*=\s*([^;\n]+)`)
	reOptRef = regexp.MustCompile(`\$params\[\s*'configoptions'\s*\]\[\s*'([^']+)'\s*\]`)
	reCfRef  = regexp.MustCompile(`\$params\[\s*'customfields'\s*\]\[\s*'([^']+)'\s*\]`)
)

// FromFiles 把一个已解包的插件目录（相对路径 → 内容）转换成规格。
// mainPath 是主文件（<标识>/<标识>.php 或 <标识>.php）；传空则自动探测。
func FromFiles(mainPath string, files map[string][]byte) (*ParseResult, error) {
	if mainPath == "" {
		detected, err := detectMainFile(files)
		if err != nil {
			return nil, err
		}
		mainPath = detected
	}
	src, ok := files[mainPath]
	if !ok {
		return nil, fmt.Errorf("主文件 %s 不存在", mainPath)
	}
	id := moduleIDFromPath(mainPath)
	if id == "" {
		return nil, fmt.Errorf("无法从 %s 推断模块标识", mainPath)
	}
	spec, err := parseModule(id, string(src))
	if err != nil {
		return nil, err
	}
	return &ParseResult{Spec: spec}, nil
}

// detectMainFile 探测主文件：优先 <dir>/<dir>.php，其次根目录唯一 .php。
func detectMainFile(files map[string][]byte) (string, error) {
	var candidates, roots []string
	for path := range files {
		if !strings.HasSuffix(strings.ToLower(path), ".php") {
			continue
		}
		dir, base := splitPath(path)
		if dir != "" && dir != "." && strings.TrimSuffix(base, ".php") == dir {
			candidates = append(candidates, path)
			continue
		}
		if dir == "" || dir == "." {
			roots = append(roots, path)
		}
	}
	sort.Strings(candidates)
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	sort.Strings(roots)
	if len(roots) == 1 {
		return roots[0], nil
	}
	if len(roots) > 1 {
		return "", fmt.Errorf("压缩包里有多个根级 PHP 文件，无法确定主文件，请指定模块标识")
	}
	return "", fmt.Errorf("压缩包里没有找到 PHP 主文件（应为 <标识>/<标识>.php）")
}

func splitPath(p string) (dir, base string) {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

// moduleIDFromPath 从 <id>/<id>.php 或 <id>.php 推断模块标识。
func moduleIDFromPath(p string) string {
	dir, base := splitPath(p)
	base = strings.TrimSuffix(base, ".php")
	if dir != "" && dir != "." {
		if dir == base {
			return base
		}
		// 子目录名与文件名不一致（bthostx.zip 形态）时以文件名为准
		return base
	}
	return base
}

// parseModule 是转换核心：源码 → 规格。
func parseModule(id, src string) (*Spec, error) {
	clean := stripComments(src)
	funcs := extractFunctions(clean, id)
	spec := &Spec{Slug: id, Source: SourceZJMF, Actions: map[string]Action{}, Warnings: []string{}}
	warn := func(format string, args ...any) {
		spec.Warnings = append(spec.Warnings, fmt.Sprintf(format, args...))
	}

	// 1. _MetaData
	if body, ok := funcs["metadata"]; ok {
		if arr, okArr := findReturnArray(body); okArr {
			spec.DisplayName = arr.dictGet("DisplayName").asString()
			spec.SourceVersion = arr.dictGet("APIVersion").asString()
			spec.HelpDoc = arr.dictGet("HelpDoc").asString()
		}
	} else {
		warn("未找到 _MetaData()，显示名与帮助链接缺失")
	}

	// 2. _ConfigOptions
	if body, ok := funcs["configoptions"]; ok {
		spec.ConfigOptions = parseConfigOptions(body, warn)
	} else {
		warn("未找到 _ConfigOptions()，产品配置项映射需要手动维护")
	}

	// 3. 签名方案
	spec.Auth = detectAuth(clean, funcs, warn)
	// Proxmox VE 模块走 PVE ticket 认证（ProxmoxClient.pve），静态转换没有意义：
	// ShitIDC 已内置 proxmox 供应商，这里只保留元数据与配置项供参考。
	if strings.Contains(clean, "ProxmoxClient") || strings.Contains(clean, "PVEAuthCookie") ||
		strings.Contains(clean, "CSRFPreventionToken") {
		spec.Auth = Auth{Scheme: SchemeUnsupported}
		spec.Actions = map[string]Action{}
		warn("该模块使用 Proxmox VE ticket 认证，无法静态转换；ShitIDC 已内置 proxmox 供应商，请直接使用内置实现（本规格仅供查看配置项）")
	}

	// 4. 成功判据（模块级默认：取各生命周期函数中出现最多的比较）
	spec.Success = detectSuccessDefault(funcs, warn)

	// 5. 各生命周期动作（unsupported 方案不提取动作）
	found := 0
	if spec.Auth.Scheme != SchemeUnsupported {
		for act, fn := range actionFuncNames {
			body, ok := funcs[strings.ToLower(fn)]
			if !ok {
				continue
			}
			a := parseAction(body, id, spec, act)
			if a.Path == "" {
				// 函数存在但没解析出路径：仍收录，运行时报"路径未配置"。
				warn("_%s 里没有解析出上游路径，导入后请编辑规格补充 %s 动作", fn, act)
			}
			spec.Actions[act] = a
			found++
		}
		if _, ok := spec.Actions[ActionCreate]; !ok {
			warn("未找到 _CreateAccount()，该插件无法自动开通，只能作为配置参考")
		}
		if found == 0 {
			warn("没有识别到任何生命周期函数，源码可能不是经典 server 模块格式")
		}
	}

	spec.Normalize()
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return spec, nil
}

// extractFunctions 返回小写函数名 → 函数体。
func extractFunctions(clean, id string) map[string]string {
	out := map[string]string{}
	prefix := strings.ToLower(id) + "_"
	locs := reFunc.FindAllStringSubmatchIndex(clean, -1)
	for i, loc := range locs {
		name := strings.ToLower(clean[loc[2]:loc[3]])
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		short := strings.TrimPrefix(name, prefix)
		if _, dup := out[short]; dup {
			continue
		}
		end := len(clean)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if body := funcBody(clean[loc[0]:end]); body != "" {
			out[short] = body
		}
	}
	return out
}

// funcBody 截取函数体（第一个 { 到配对 }），跳过字符串字面量。
func funcBody(seg string) string {
	open := strings.Index(seg, "{")
	if open < 0 {
		return ""
	}
	depth := 0
	for i := open; i < len(seg); i++ {
		switch seg[i] {
		case '\'', '"':
			q := seg[i]
			i++
			for i < len(seg) && seg[i] != q {
				if seg[i] == '\\' {
					i++
				}
				i++
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return seg[open+1 : i]
			}
		}
	}
	return seg[open+1:]
}

// parseConfigOptions 解析 _ConfigOptions 的返回数组。
func parseConfigOptions(body string, warn func(string, ...any)) []ConfigOption {
	arr, ok := findReturnArray(body)
	if !ok {
		warn("_ConfigOptions 返回值不是可解析的数组字面量")
		return nil
	}
	out := []ConfigOption{}
	for i, item := range arr.list {
		if item.dict == nil {
			continue
		}
		opt := ConfigOption{
			Type:        item.dictGet("type").asString(),
			Name:        item.dictGet("name").asString(),
			Key:         item.dictGet("key").asString(),
			Description: item.dictGet("description").asString(),
			Placeholder: item.dictGet("placeholder").asString(),
			Default:     item.dictGet("default").asString(),
		}
		if ov := item.dictGet("options"); ov.dict != nil || len(ov.list) > 0 {
			opt.Options = parseOptionValues(ov)
		}
		if !knownOptionType(opt.Type) {
			warn("配置项 %d 的类型 %q 未被后台支持，按 textarea 处理", i+1, opt.Type)
			opt.Type = "textarea"
		}
		if opt.Key == "" && opt.Name != "" {
			warn("配置项 %q 没有声明 key，将无法自动映射到上游参数", opt.Name)
		}
		out = append(out, opt)
	}
	return out
}

func knownOptionType(t string) bool {
	switch t {
	case "text", "password", "yesno", "radio", "dropdown", "textarea":
		return true
	}
	return false
}

// parseOptionValues 兼容两种声明形态（契约文档 §6.2 options 形态冲突）：
//   - 扁平数组 ['自定义配置','套餐开通'] → value = 下标
//   - 字典 {'1': '1_允许'} → value = 键，label 去掉 "键_" 前缀
func parseOptionValues(v phpValue) []OptValue {
	out := []OptValue{}
	if len(v.list) > 0 {
		for i, e := range v.list {
			out = append(out, OptValue{Name: e.asString(), Value: fmt.Sprintf("%d", i)})
		}
		return out
	}
	for _, k := range v.keys {
		label := v.dict[k].asString()
		name := label
		if strings.HasPrefix(label, k+"_") {
			name = strings.TrimPrefix(label, k+"_")
		}
		out = append(out, OptValue{Name: name, Value: k})
	}
	return out
}

// detectAuth 识别签名算法与 token 来源。
func detectAuth(clean string, funcs map[string]string, warn func(string, ...any)) Auth {
	auth := Auth{Scheme: SchemeMD5SortUpper, TokenSource: TokenAccessHash,
		TimeParam: "time", RandomParam: "random", SignatureParam: "signature", Placement: PlacementForm}

	if signBody, ok := funcs["createsign"]; ok {
		if keys := reDataKeys.FindAllStringSubmatch(signBody, 3); len(keys) >= 2 {
			auth.TimeParam = keys[0][1]
			auth.RandomParam = keys[1][1]
		}
		switch {
		case strings.Contains(signBody, "sort(") && strings.Contains(signBody, "strtoupper"):
			auth.Scheme = SchemeMD5SortUpper
		case strings.Contains(signBody, "md5("):
			// wlkanglepro 形态（wlkanglepro.php:28-36 的调用约定）：
			// s = md5(动作名 . token . 随机数)，query 里 a=动作名、r=随机数、s=签名。
			auth.Scheme = SchemeMD5Concat
			auth.ActionParam = "a"
			auth.RandomParam = "r"
			auth.SigParam = "s"
			auth.ExtraQuery = map[string]string{"json": "1"}
		}
	} else {
		warn("未找到 _CreateSign()，按 bthosts 约定（md5 排序大写）处理，请核对")
	}

	// token 来源：CreateSign 调用点取的 $params 字段
	if m := reSignCall.FindStringSubmatch(clean); m != nil {
		if m[1] == TokenServerPasswd {
			auth.TokenSource = TokenServerPasswd
		} else {
			auth.TokenSource = TokenAccessHash
		}
	}

	// 落点：nokvm 家族（timeStamp）签名走 query，其余走表单。
	if auth.Scheme == SchemeMD5SortUpper && auth.TimeParam == "timeStamp" {
		auth.Placement = PlacementQuery
	}
	return auth
}

// detectSuccessDefault 取全模块最常见的业务码判据作为默认。
func detectSuccessDefault(funcs map[string]string, warn func(string, ...any)) Success {
	counts := map[string]int{}
	msgs := map[string]int{}
	for name, body := range funcs {
		if !isLifecycle(name) {
			continue
		}
		if m := reCodeCheck.FindStringSubmatch(body); m != nil {
			counts[m[1]+"="+m[2]]++
		}
		for _, m := range reMsgField.FindAllStringSubmatch(body, -1) {
			msgs[m[1]]++
		}
	}
	out := Success{Field: "code", Equals: "0", MessageField: "msg"}
	if len(counts) > 0 {
		best, bestN := "", -1
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if counts[k] > bestN {
				best, bestN = k, counts[k]
			}
		}
		parts := strings.SplitN(best, "=", 2)
		out.Field, out.Equals = parts[0], parts[1]
	} else {
		warn("没有识别出业务码判据，默认按 code==0 处理，请核对")
	}
	if msgs["message"] > msgs["msg"] {
		out.MessageField = "message"
	}
	return out
}

func isLifecycle(name string) bool {
	for _, fn := range actionFuncNames {
		if strings.EqualFold(fn, name) {
			return true
		}
	}
	return false
}

// parseAction 从一个生命周期函数体提取动作定义。act 是 spec 动作键
// （create/status 等有各自的可选字段提取规则）。
func parseAction(body, id string, spec *Spec, act string) Action {
	a := Action{Method: "POST", InstanceIDParam: "id"}

	// 路径：md5concat（wlkanglepro）的动作名来自 $a = '...' 与 $info 数组；
	// 其它方案优先 GetUrl($params, '...'，最后是函数体里第一个 / 开头的引号串。
	reActionVarA := regexp.MustCompile(`\$a\s*=\s*['"]([^'"]+)['"]`)
	switch {
	case spec.Auth.Scheme == SchemeMD5Concat && reActionVarA.MatchString(body):
		a.Path = reActionVarA.FindStringSubmatch(body)[1]
	case reGetUrlPath.FindStringSubmatch(body) != nil:
		a.Path = reGetUrlPath.FindStringSubmatch(body)[1]
	case reActionVarR.FindStringSubmatch(body) != nil:
		a.Path = reActionVarR.FindStringSubmatch(body)[1]
	case reQuotedPath.FindStringSubmatch(body) != nil:
		a.Path = reQuotedPath.FindStringSubmatch(body)[1]
	}

	// md5concat：$info = [...] 里的字面量键值就是固定 query 参数。
	if spec.Auth.Scheme == SchemeMD5Concat {
		if pos := strings.Index(body, "$info"); pos >= 0 {
			if br := strings.IndexAny(body[pos:], "["); br >= 0 {
				if arr, _, err := parsePHPArray(body, pos+br); err == nil {
					q := map[string]string{}
					for _, k := range arr.keys {
						v := arr.dict[k]
						if k == "a" || v.dict != nil || len(v.list) > 0 {
							continue // 动作名走 path；表达式值运行时补不了
						}
						q[k] = v.asString()
					}
					if len(q) > 0 {
						a.Query = q
					}
				}
			}
		}
	}

	// 方法：模块自身的 _get( / file_get_contents / 'GET' 判 GET，其余 POST。
	idGet := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(id) + `_get\s*\(`)
	if idGet.MatchString(body) || strings.Contains(body, "file_get_contents") ||
		strings.Contains(body, "'GET'") || strings.Contains(body, `"GET"`) ||
		strings.Contains(body, "CURLOPT_HTTPGET") {
		a.Method = "GET"
	}

	// create 响应里的开通号：模块读 $res['data']['site']['id']，
	// 路径指向 id 值本身（data.site.id）。
	if act == ActionCreate {
		if m := reRespIDPath.FindStringSubmatch(body); m != nil {
			a.InstanceIDPath = "data"
			for _, seg := range segmentChain(m[1]) {
				a.InstanceIDPath += "." + seg
			}
			a.InstanceIDPath += ".id"
		}
	}

	// status 动作：原始状态字段名（如 bthosts 的 data.loca）
	if act == ActionStatus {
		if m := reStatusField.FindStringSubmatch(body); m != nil && m[1] != "id" {
			a.StatusField = "data." + m[1]
		}
	}

	a.Body = map[string]string{}
	for _, m := range reAssign.FindAllStringSubmatch(body, 32) {
		key, expr := m[1], strings.TrimSpace(m[2])
		if tmpl, ok := templateForExpr(expr, spec); ok {
			if _, dup := a.Body[key]; !dup {
				a.Body[key] = tmpl
			}
		}
	}
	if len(a.Body) == 0 {
		a.Body = nil
	}
	return a
}

// templateForExpr 把赋值表达式右端翻译成运行时模板变量；认不出就丢弃
// （宁可让管理员补一条，也不能发错误的常量给上游）。
func templateForExpr(expr string, spec *Spec) (string, bool) {
	e := strings.TrimSpace(expr)
	switch {
	case reOptRef.MatchString(e):
		if m := reOptRef.FindStringSubmatch(e); m != nil {
			return "{{opt_" + m[1] + "}}", true
		}
	case reCfRef.MatchString(e):
		// customfields 里的就是上游开通号（host_id / vserverid）
		return "{{instance_id}}", true
	case strings.Contains(e, `$params['domain']`):
		return "{{domain}}", true
	case strings.Contains(e, `$params["domain"]`):
		return "{{domain}}", true
	case strings.Contains(e, `$params['uid']`):
		return "{{uid}}", true
	case strings.Contains(e, `$params['user_info']['email']`):
		return "{{email}}", true
	case strings.Contains(e, `$params['username']`):
		return "{{username}}", true
	case strings.Contains(e, `$params['password']`):
		return "{{password}}", true
	case strings.Contains(e, `$params['nextduedate']`):
		// date('Y-m-d', nextduedate) → 日期；直接用时间戳 → 时间戳
		if strings.Contains(e, "date(") {
			return "{{expire_date}}", true
		}
		return "{{expire_ts}}", true
	}
	return "", false
}

// segmentChain 把 "['site']" / "['site']['id']" 之类的链拆成 [site]。
func segmentChain(chain string) []string {
	re := regexp.MustCompile(`\[\s*'(\w+)'\s*\]`)
	out := []string{}
	for _, m := range re.FindAllStringSubmatch(chain, -1) {
		out = append(out, m[1])
	}
	return out
}
