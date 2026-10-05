// Package zjmfimport 把魔方财务（ZJMF）的经典 server 插件（PHP）转换成
// ShitIDC 的声明式上游规格（ProviderSpec JSON）。
//
// 魔方插件是 PHP 源码，ShitIDC 是 Go，不可能"直接运行"对方插件。但经典
// server 模块的结构高度公式化（见 docs/zjmf-server-module-contract.md）：
// 每个生命周期函数就是「组装签名 → POST/GET 一个上游路径 → 按业务码判成功」，
// 签名算法只有三种已知形态。因此转换器静态解析 PHP 源码，抽出这些事实，
// 生成一份可执行的规格；运行时由 internal/provider/custom 按规格发 HTTP。
//
// 解析不出的部分不会编造：规格里留空并在 Warnings 里说明，导入后可在后台编辑。
package zjmfimport

// SpecVersion 是 ProviderSpec 的当前版本，运行时拒绝更高的版本。
const SpecVersion = 1

// Source 标记规格来源。
const SourceZJMF = "zjmf"

// 动作键（spec.actions 的 map key）。命名与 provider.Provider 的方法一一对应。
const (
	ActionTest          = "test"           // _TestLink
	ActionCreate        = "create"         // _CreateAccount
	ActionSuspend       = "suspend"        // _SuspendAccount
	ActionUnsuspend     = "unsuspend"      // _UnsuspendAccount
	ActionTerminate     = "terminate"      // _TerminateAccount
	ActionRenew         = "renew"          // _Renew
	ActionChangePackage = "change_package" // _ChangePackage
	ActionSync          = "sync"           // _Sync
	ActionStatus        = "status"         // _Status
	ActionPassword      = "password"       // _CrackPassword
)

// 签名方案（auth.scheme）。
const (
	// SchemeMD5SortUpper: bthosts/nokvm 形态 —— 取 [time, random, token]
	// 三个字符串做字典序排序后无分隔符拼接，md5 后转大写；token 不随请求发送。
	SchemeMD5SortUpper = "md5sort_upper"
	// SchemeMD5Concat: wlkanglepro 形态 —— query 带 a=<动作名>、r=<随机数>、
	// s=md5(action + token + random)，见 wlkanglepro.php:28-36 的调用约定。
	SchemeMD5Concat = "md5concat"
	// SchemeUnsupported: 模块使用无法静态转换的认证（如 Proxmox VE ticket）。
	// 此时规格只保留元数据与配置项，生命周期动作一律为空。
	SchemeUnsupported = "unsupported"
)

// token 来源（auth.token_source）：对应魔方接口设置里的哪个字段。
const (
	TokenAccessHash   = "accesshash"
	TokenServerPasswd = "server_password"
)

// 签名参数落点（auth.placement）。
const (
	PlacementForm  = "form"
	PlacementQuery = "query"
)

// Spec 是一份可执行的上游驱动描述。JSON 存进 providers.config 的 "spec" 键。
type Spec struct {
	SpecVersion   int               `json:"spec_version"`
	Slug          string            `json:"slug"`                     // 模块标识（目录名）
	DisplayName   string            `json:"display_name,omitempty"`   // _MetaData DisplayName
	Source        string            `json:"source"`                   // 固定 "zjmf"
	SourceVersion string            `json:"source_version,omitempty"` // _MetaData APIVersion
	HelpDoc       string            `json:"help_doc,omitempty"`
	Auth          Auth              `json:"auth"`
	Success       Success           `json:"success_when"`
	Actions       map[string]Action `json:"actions"`
	ConfigOptions []ConfigOption    `json:"config_options,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
}

// Auth 描述请求签名方式。
type Auth struct {
	Scheme string `json:"scheme"`
	// md5sort_upper：token 取自接口设置的字段（决定语义，运行时统一读密钥列）。
	TokenSource string `json:"token_source,omitempty"`
	// md5sort_upper：time/random/签名 三个参数的名字与落点。
	//   bthosts: time/random/signature，POST 表单
	//   nokvm:   timeStamp/randomStr/signature，URL query
	TimeParam      string `json:"time_param,omitempty"`
	RandomParam    string `json:"random_param,omitempty"`
	SignatureParam string `json:"signature_param,omitempty"`
	Placement      string `json:"placement,omitempty"` // form | query
	// md5concat：query 里 a=动作名（ActionParam）、r=随机数（RandomParam）、
	// s=签名（SigParam），外加固定 query（如 json=1）。
	ActionParam string            `json:"action_param,omitempty"`
	SigParam    string            `json:"sig_param,omitempty"`
	ExtraQuery  map[string]string `json:"extra_query,omitempty"`
}

// Success 描述"上游说成功"的判据：响应 JSON 里 Field 的值等于 Equals。
// Equals 存字符串，运行时对数字做宽容比较（"1" 等于 1、1.0）。
type Success struct {
	Field        string `json:"field"`
	Equals       string `json:"equals"`
	MessageField string `json:"message_field,omitempty"` // 失败时错误文案字段（msg/message）
}

// Action 是单个生命周期动作的调用方式。Body/Query 的值支持 {{var}} 模板：
// {{instance_id}}、{{domain}}、{{username}}、{{password}}、{{uid}}、{{email}}、
// {{expire_date}}、{{opt_<配置项key>}}、{{cf_<自定义字段key>}}。
type Action struct {
	Method          string            `json:"method"`
	Path            string            `json:"path,omitempty"` // md5concat 时是 r 参数的动作名
	Body            map[string]string `json:"body,omitempty"`
	Query           map[string]string `json:"query,omitempty"`
	InstanceIDPath  string            `json:"instance_id_path,omitempty"`  // create 响应里开通号的 JSON 路径（data.site.id）
	InstanceIDParam string            `json:"instance_id_param,omitempty"` // 后续动作携带开通号的参数名（默认 id）
	StatusField     string            `json:"status_field,omitempty"`      // status 动作：原始状态字段
	StatusMap       map[string]string `json:"status_map,omitempty"`        // 原始状态 → on/off/process/suspend/unknown
}

// ConfigOption 是模块声明的产品配置项（_ConfigOptions），导入后用于把
// ShitIDC 商品配置项映射回上游参数名（见 config_options.provider_key）。
type ConfigOption struct {
	Type        string     `json:"type"` // text/password/yesno/radio/dropdown/textarea
	Name        string     `json:"name"`
	Key         string     `json:"key,omitempty"` // $params['configoptions'][key]
	Description string     `json:"description,omitempty"`
	Placeholder string     `json:"placeholder,omitempty"`
	Default     string     `json:"default,omitempty"`
	Options     []OptValue `json:"options,omitempty"` // radio/dropdown 候选
}

// OptValue 是配置项候选项。
type OptValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Normalize 补齐默认值并做基础校验。
func (s *Spec) Normalize() {
	if s.SpecVersion == 0 {
		s.SpecVersion = SpecVersion
	}
	if s.Source == "" {
		s.Source = SourceZJMF
	}
	if s.Auth.Scheme == "" {
		s.Auth.Scheme = SchemeMD5SortUpper
	}
	if s.Auth.TokenSource == "" {
		s.Auth.TokenSource = TokenAccessHash
	}
	if s.Auth.TimeParam == "" {
		s.Auth.TimeParam = "time"
	}
	if s.Auth.RandomParam == "" {
		s.Auth.RandomParam = "random"
	}
	if s.Auth.SignatureParam == "" {
		s.Auth.SignatureParam = "signature"
	}
	if s.Auth.Placement == "" {
		s.Auth.Placement = PlacementForm
	}
	if s.Auth.ActionParam == "" {
		if s.Auth.Scheme == SchemeMD5Concat {
			s.Auth.ActionParam = "a" // wlkanglepro：a=动作名
		} else {
			s.Auth.ActionParam = "r"
		}
	}
	if s.Auth.SigParam == "" {
		s.Auth.SigParam = "s"
	}
	if s.Auth.RandomParam == "" {
		if s.Auth.Scheme == SchemeMD5Concat {
			s.Auth.RandomParam = "r" // wlkanglepro：r=随机数
		} else {
			s.Auth.RandomParam = "random"
		}
	}
	if s.Success.Field == "" {
		s.Success.Field = "code"
		s.Success.Equals = "0"
	}
	if s.Success.MessageField == "" {
		s.Success.MessageField = "msg"
	}
	if s.Actions == nil {
		s.Actions = map[string]Action{}
	}
	for name, act := range s.Actions {
		if act.InstanceIDParam == "" {
			act.InstanceIDParam = "id"
		}
		if act.Method == "" {
			act.Method = "POST"
		}
		s.Actions[name] = act
	}
}

// Validate 检查规格可执行的最小条件。
func (s *Spec) Validate() error {
	if s.SpecVersion > SpecVersion {
		return ErrSpecTooNew
	}
	if s.Slug == "" {
		return ErrSpecInvalid("缺少模块标识 slug")
	}
	switch s.Auth.Scheme {
	case SchemeMD5SortUpper, SchemeMD5Concat:
	case SchemeUnsupported:
		if len(s.Actions) > 0 {
			return ErrSpecInvalid("unsupported 签名方案不允许有动作")
		}
	default:
		return ErrSpecInvalid("未知签名方案 " + s.Auth.Scheme)
	}
	return nil
}

// SpecTooNew 表示导入的规格由更新版本的转换器生成。
var ErrSpecTooNew = ErrSpecInvalid("规格版本过新，请升级 ShitIDC 后再导入")

// ErrSpecInvalid 是规格内容非法。
type ErrSpecInvalid string

func (e ErrSpecInvalid) Error() string { return string(e) }
