# 魔方插件导入：把 ZJMF server 插件变成 ShitIDC 供应商

魔方财务（ZJMF）的插件是 PHP，ShitIDC 是 Go，**不可能直接运行对方的 PHP 插件**。
但经典 server 模块的结构高度公式化（证据见
[zjmf-server-module-contract.md](zjmf-server-module-contract.md)）：每个生命周期
函数就是「组装签名 → POST/GET 一个上游路径 → 按业务码判成功」，签名只有三种
已知形态。因此导入采用**静态转换**：

```
魔方插件 zip（PHP 源码）
        │  internal/zjmfimport —— 静态解析 _MetaData/_ConfigOptions/_CreateSign/
        │                         _TestLink/_CreateAccount/… 抽取事实
        ▼
ProviderSpec JSON（声明式上游规格）
        │  internal/provider/custom —— 按规格发 HTTP（签名/成功判据/模板变量）
        ▼
providers 表里的一条 custom 供应商（与内置供应商同一套：测试连接/绑定商品/worker 开通）
```

**对用户来说最简单的方法**：后台把插件 zip 传上去，检查识别结果，填上游地址
和密钥，完成。也可以先离线转换看一眼：

```bash
go build -o zjmfimport ./cmd/zjmfimport
./zjmfimport -zip bthosts.zip          # 或 -dir 已解包目录
./zjmfimport -zip bthosts.zip -o spec.json
```

## 1. 转换器能识别什么

以随魔方发布的四个模块实测（`_zjmf_ref` 里的真实源码，仅冒烟验证、不入库）：

| 模块 | 签名 | 成功判据 | 动作路径 | 配置项 |
|---|---|---|---|---|
| bthosts | md5 排序大写（表单） | `code==1` | 全部 10 个动作 | 全部 |
| nokvm | md5 排序大写（query，token=服务器密码） | `code==0`，message 字段 | 主要动作 | 全部 |
| wlkanglepro | `s=md5(a+token+r)`（kangle 系） | `result==200` | 动作名（a=） | 全部 |
| proxmoxve | PVE ticket —— **明确报"请用内置 proxmox 供应商"** | — | 不生成 | 保留参考 |

识别内容：

- **元数据**：DisplayName / APIVersion / HelpDoc（来自 `_MetaData`）；
- **签名方案**：`_CreateSign` 的三种已知形态 + token 来源（accesshash 或
  服务器密码）+ 签名落点（表单 / query）；
- **生命周期动作**：`_TestLink` `_CreateAccount` `_SuspendAccount`
  `_UnsuspendAccount` `_TerminateAccount` `_Renew` `_ChangePackage` `_Sync`
  `_Status` `_CrackPassword` 的请求方法、路径（或 kangle 动作名）、
  固定 query、成功判据、开通号在响应里的位置（如 `data.site.id`）；
- **请求体映射**：赋值表达式里的 `$params['configoptions'][X]` → `{{opt_X}}`、
  `$params['domain']` → `{{domain}}`、`$params['uid']` → `{{uid}}`、
  `$params['user_info']['email']` → `{{email}}`、`date('Y-m-d', nextduedate)`
  → `{{expire_date}}`、`$params['customfields'][X]` → `{{instance_id}}` 等；
- **产品配置项**：`_ConfigOptions` 的 text/password/yesno/radio/dropdown，
  options 兼容扁平数组与 `'1' => '1_允许'` 两种写法。

**识别不了的不编造**：路径/参数缺失时该动作保留为空并在 `warnings` 里说明，
导入后可在「供应商 → 查看规格」里直接编辑 JSON 补上。

## 2. 规格示例（bthosts 转换产物节选）

```json
{
  "spec_version": 1,
  "slug": "bthosts",
  "display_name": "btHost对接模块",
  "source": "zjmf",
  "source_version": "1.7.1",
  "auth": {
    "scheme": "md5sort_upper",
    "token_source": "accesshash",
    "time_param": "time", "random_param": "random", "signature_param": "signature",
    "placement": "form"
  },
  "success_when": { "field": "code", "equals": "1", "message_field": "msg" },
  "actions": {
    "test":      { "method": "GET",  "path": "/api/vhost/index" },
    "create":    { "method": "POST", "path": "/api/vhost/user_create",
                   "body": { "plans_id": "{{opt_plans_id}}", "pack[flow_max]": "{{opt_flow_max}}" },
                   "instance_id_path": "data.site.id" },
    "suspend":   { "method": "POST", "path": "/api/vhost/host_locked" },
    "renew":     { "method": "POST", "path": "/api/vhost/host_endtime",
                   "body": { "endtime": "{{expire_date}}" } }
  },
  "config_options": [ { "type": "dropdown", "name": "开通方式", "key": "type", "options": [...] } ],
  "warnings": []
}
```

## 3. 导入之后怎么用

1. **管理后台 → 供应商 → 导入魔方插件**：选 zip → 转换预览（含警告清单）
   → 填上游接口地址与密钥（即魔方插件里 `$params['server_ip']/secure/port`
   组成的地址与 accesshash）→ 导入。
2. 生成一条 **custom 供应商**，用「测试连接」验证（会真实按规格发一次签名请求）。
3. 商品绑定该供应商（和绑定内置 proxmox/魔方供应商完全一样）。
4. **配置项映射（关键）**：在商品配置项里给每个配置项填 `provider_key` =
   插件原来的 key（如 `site_max`、`flow_max`）。开通时系统把买家选择的值按
   `{{opt_<key>}}` 传给上游；不填 provider_key 则用配置项名称作为键。
5. 生命周期由 worker 自动执行：开通 / 暂停 / 解除 / 删除 / 续费（带新到期
   时间，对应魔方 `_Renew` 的 `nextduedate` 约定）/ 升降级。

## 4. 模板变量速查（规格 body/query 里可用）

| 变量 | 含义 |
|---|---|
| `{{instance_id}}` | 上游开通号（服务上的 provider_ref） |
| `{{domain}}` / `{{username}}` / `{{password}}` / `{{email}}` | 开通身份信息（下单上下文提供） |
| `{{uid}}` | ShitIDC 用户 ID |
| `{{expire_date}}` / `{{expire_ts}}` | 续费后的到期时间（日期 / Unix 秒） |
| `{{opt_<key>}}` | 商品配置项取值（provider_key 或名称作键；支持含空格的 key，如 `{{opt_Disk Space}}`） |
| `{{cf_<key>}}` | 商品自定义字段取值 |

## 5. 边界与限制（诚实清单）

- **只支持经典 server 模块**（`public/plugins/servers/<标识>/<标识>.php`）。
  gateway/sms/oauth/certification 等 PHP 插件、V10 `idcsmart_common` 包格式的
  server 插件不在转换范围（转换器会明确报"没有识别到任何生命周期函数"）。
- **ionCube 加密的模块无法解析**（本来也读不到源码）。
- 静态分析看不出的逻辑（条件分支选路径、按 `$params` 动态拼参数等）不会硬猜，
  以警告形式列出，需要人工在规格编辑器里补一条通用路径。
- 转换器覆盖的是魔方"插件调用上游"的完整骨架；上游若需要特殊的开通前置
  （如先建用户再开主机），请把动作路径按实际调用顺序在规格里配置好——
  规格是数据，不是代码，改起来不需要重新编译。
- 签名只实现两种已证实方案；遇到未知签名会按 bthosts 约定处理并给出警告，
  上线前务必用「测试连接」验证。

## 6. 相关注册表

- 转换器：`internal/zjmfimport`（解析与规格模型，含三种签名/配置项形态的单测）
- 运行时：`internal/provider/custom`（httptest 全流程测试，含签名逐字节比对）
- 导入 API：`POST /api/v1/admin/providers/zjmf-import`（multipart，
  `apply=false` 预览 / `apply=true` 落库，需 `provider.manage` 权限）
- 规格查看/编辑：`GET|PUT /api/v1/admin/providers/:id/spec`
- CLI：`cmd/zjmfimport`
