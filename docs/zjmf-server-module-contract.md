# 魔方财务 ZJMF 3.7.6 —— 服务器插件（server module）契约 / 移植手册

> 证据边界（先读这一段）
> - 明文可读：public/plugins/servers/{bthosts,nokvm,proxmoxve,wlkanglepro}/*、public/plugins/{sms,certification,mail,gateway,oauth}/*（部分）、public/admin/js/*.js、public/admin/lang/zh.js、public/install/thinkcmf.sql（完整建表语句）、public/upgrade/*.sql、vendor/thinkcmf/cmf/src/common.php。
> - 加密不可读：app/**（ionCube）。zjmf-main/zjmfmangerbetaV3.7.6.zip 里的 app/** **同样是 ionCube 加密**（已验证：解压后每文件首行含 "ionCube Loader"）。
> - 因此"核心如何调度插件函数"的代码**没有明文证据**。本手册凡属推断均显式标注【推断】；找不到证据的一律写"未找到明文证据"。
> - 提取的中间产物在 %TEMP%\zjmf_pt（scratch，不属于交付物）。

---

## 1. 文件布局

### 1.1 经典 server 模块（本手册目标）

四个随安装包发布的模块的实际布局（Get-ChildItem -Recurse 实测）：

| 模块 | 目录内容 |
|---|---|
| bthosts | bthosts.php (27728B)、version.txt (5B)、templates/information.html、templates/status.html |
| wlkanglepro | wlkanglepro.php、version.txt (5B)、templates/information.html、templates/tips.html |
| nokvm | nokvm.php、templates/{backups,cd_rom,nat_acl,nat_web,security_group,snapshot}.html（**无 version.txt**） |
| proxmoxve | proxmoxve.php、ProxmoxApiException.pve、ProxmoxMethodsTrait.pve、ProxmoxClient.pve、ProxmoxNode.pve、ProxmoxVM.pve、templates/gopanel.htm（**无 version.txt**） |

结论（有证据的）：
- 目录名 = 模块标识。nokvm.php:1539 ->where('d.type', 'nokvm')，而 nokvm.php:1531 ->leftJoin('server_groups d', 'c.gid=d.id')；建表注释 shd_server_groups.type = "服务器模块类型"。即 **模块标识 == 目录名 == server_groups.type**。
- 主文件必须叫 <标识>.php，且用全局函数 <标识>_Xxx() 导出（无命名空间、无类）。
- 允许 require 同目录额外文件，扩展名任意：proxmoxve.php:2-6 require "ProxmoxApiException.pve"; require "ProxmoxMethodsTrait.pve"; ...，ProxmoxClient.pve 内 namespace ProxmoxApi;。
- version.txt 内容 = **裸版本号，无换行**。Format-Hex 实测 bthosts/version.txt = 字节 31 2E 37 2E 31 = "1.7.1"；wlkanglepro/version.txt = "1.0.3"。两者都与该模块 _MetaData()['APIVersion'] 完全相同（bthosts.php:8 'APIVersion' => '1.7.1'；wlkanglepro.php:5 'APIVersion' => '1.0.3'）。
  **谁读 version.txt：在全部可读文件（含 admin JS、install/upgrade PHP）里搜索 version.txt 零命中 → 未找到明文证据**（应由加密核心的插件/升级检查读取）。nokvm、proxmoxve 没有该文件仍随包发布。
- templates/ 是 **给核心的 ThinkPHP 5 模板引擎渲染、输出到"会员中心 → 产品详情"分页** 用的，由 _ClientAreaOutput() 返回相对路径引用（见 2.10）。模板内是 ThinkPHP 语法，实测证据：
  - bthosts/templates/information.html:253  {if $info.domain_max == 0}不限制{else/}{$info.domain_max} 个{/if}
  - bthosts/templates/status.html:233  {$info.status|raw}
  - nokvm/templates/snapshot.html:207  {foreach $list as $key=>$vo } ... :216 {/foreach}
  - nokvm/templates/backups.html:205  url:"{$MODULE_CUSTOM_API}"；:324  xhr.setRequestHeader("Authorization","JWT {$Think.get.jwt}")
  - bthosts/templates/information.html:245-247  {$params.username} / {$params.password} / {$params.customfields.host_id}
  - {$MODULE_CUSTOM_API} 在模板里被使用但**从未在任何 PHP 里被赋值** → 由（加密）核心注入；其拼装规则未找到明文证据。

### 1.2 bthostx（V10 sub_server 格式）与经典格式的差异

ZJMF-CBAP-plugins-main/plugins/sub_server/bthostx.zip 内容（ZipFile 枚举实测）：

~~~
bthostx/bthostx.php                       38400B
bthostx/model/IdcsmartHostModel.php         212B
bthostx/model/IdcsmartModel.php             219B
bthostx/templates/bthosts.php             25912B
bthostx/templates/error.html                294B
bthostx/templates/information.html          241B
bthostx/templates/information - 副本.html  6054B
~~~

| 维度 | 经典格式（public/plugins/servers/<id>/） | bthostx（V10 sub_server） |
|---|---|---|
| 装载位置 | public/plugins/servers/<id>/<id>.php | 模型命名空间写死为 server\idcsmart_common\module\bthostx\model（IdcsmartHostModel.php:2），即安装到 **idcsmart_common 模块的 module/bthostx 子目录** |
| 额外文件 | 任意 .pve 等（proxmoxve） | 固定 model/、templates/ |
| 业务主机号 | $params['customfields']['vserverid']（nokvm.php:1727） | $params["vserverid"] 直接给（bthostx.php:82 return (int) ($params["vserverid"] ?? 0);） |
| 密码加密 | cmf_encrypt()（bthosts.php:239、nokvm.php:970、proxmoxve.php:50、wlkanglepro.php:64） | password_encrypt()（bthostx.php:325） |
| 结果写库 | think\Db::name('host')->update([...])（host 表） | server\idcsmart_common\model\IdcsmartCommonServerHostLinkModel（bthostx.php:322-327）+ app\common\model\HostModel::update(["status"=>"Active"])（:320-321） |
| 随机串 | randStr(8)（bthosts/nokvm/proxmoxve） | rand_str(8)（bthostx.php:162） |
| 到期时间 | $params['nextduedate']（bthosts.php:217） | 读自己的 Host 模型 due_time（bthostx.php:708-709） |
| 授权占位 | <mod>_idcsmartauthorizes() | bthostx_idcsmartauthorizes()（同） |

**bthostx 用到的表/列在本次安装里不存在**：IdcsmartHostModel 声明 protected $name="host"; protected $schema=["id"=>"string","due_time"=>"string"];，但 public/install/thinkcmf.sql 的 shd_host 只有 nextduedate；全部 public/**/*.sql 搜索 due_time、host_link、vserverid **零命中** → idcsmart_common 及其表结构**未找到明文证据**（由应用商店单独安装）。

另：plugins/server/*.zip（BtVirtualHost.zip、DirectAdmin.zip …）是 **V10 "server 插件"** 的另一种包格式（bt_virtual_host/{controller/admin,controller/home,logic,model,template,lang}，自带后台/前台模板与 JS），与 public/plugins/servers/ 经典模块完全不是一回事。会员中心 V10 前端调用：public/themes/cart/default/v10/api/common_product.js 里的 /v10/host/product/<id>/idcsmart_common/configoption、/idcsmart_common/host。

---

## 2. 函数契约全表

命名：全局函数 <模块标识>_<方法名>。下表"成功/失败返回值"列全部是**源码原文摘录**（含文件:行号）。

### 2.1 元数据 / 配置

| 函数 | 签名 | 成功返回 | 失败返回 |
|---|---|---|---|
| _MetaData | () | ['DisplayName'=>..,'APIVersion'=>..,'HelpDoc'=>..]（nokvm 另加 'version'） | — |
| _ConfigOptions | () | 二维数组，见第 6 节 | — |
| _idcsmartauthorizes | () | 空函数体，返回 null | — |

bthosts.php:6-9
~~~php
function bthosts_MetaData()
{
    return ['DisplayName' => 'btHost对接模块', 'APIVersion' => '1.7.1', 'HelpDoc' => 'http://blog.hengsuyun.com/index.php/archives/15/'];
}
~~~
nokvm.php:10-12（多一个 version）
~~~php
function nokvm_MetaData(){
	return ['DisplayName'=>'NOKVM', 'APIVersion'=>'1.1', 'HelpDoc'=>'https://www.idcsmart.com/wiki_list/339.html#3.3','version'=>'1.0.0'];
}
~~~
proxmoxve.php:7-9 / bthosts.php:3-5 / wlkanglepro.php:7-9 / bthostx.php:821-823
~~~php
function proxmoxve_idcsmartauthorizes()
{
}
~~~
nokvm 里它是**被注释掉的**，且少一个 s：nokvm.php:7  #function nokvm_idcsmartauthorize(){}。
同类占位在别的插件类型里叫**单数**形式：public/plugins/sms/huaweicloud/HuaweicloudPlugin.php:7  public function huaweicloudidcsmartauthorize(){}；public/plugins/certification/wechat/WechatPlugin.php:7 同。
**谁调用它、要求什么返回值：未找到明文证据**（所有实现都是空函数体）。

_MetaData 被后台消费的证据（只有 APIVersion / HelpDoc 被用）：public/admin/js/EditProduct~ca2dc83e.74497fec.js 内
~~~js
r.moduleMeta={APIVersion:n.module_meta?n.module_meta.APIVersion:"",HelpDoc:n.module_meta?n.module_meta.HelpDoc:""};
~~~
public/admin/js/EditProduct~3b812b8f.b6d6eee3.js 渲染成"帮助文档"链接：e.moduleMeta.HelpDoc?a("el-link",{attrs:{type:"primary",href:e.moduleMeta.HelpDoc,...}})。

### 2.2 _TestLink

| 模块 | 返回 |
|---|---|
| bthosts | ['status'=>200,'data'=>['server_status'=>1]] / ['status'=>200,'data'=>['server_status'=>0,'msg'=>$res['msg']]] |
| nokvm | 同上（msg 取 $res['message']） |
| wlkanglepro | 同上（msg 固定 '请检查安全码'） |
| proxmoxve | **裸字符串** "ok" / "bad" |
| bthostx | 同 bthosts |

bthosts.php:132-140
~~~php
    if ($res['code'] == 1) {
        $result['status'] = 200;
        $result['data']['server_status'] = 1;
    } else {
        $result['status'] = 200;
        $result['data']['server_status'] = 0;
        $result['data']['msg'] = $res['msg'];
    }
    return $result;
~~~
proxmoxve.php:23-26
~~~php
    if (6 < $return["data"]["version"]) {
        return "ok";
    }
    return "bad";
~~~
**后台把它当 HTTP body 直接读**（public/admin/js/ServerSettings~31ecd969.e0d56bfa.js）：
~~~js
Object(o["a"])(t.id);            // GET server_test_link/<serverid>
...
200!==r.data.status?t.msg=r.msg:(t.link_status=r.data.data.server_status,
  0===r.data.data.server_status?t.msg=r.data.data.msg:t.msg=e.$lang.connection_succeed)
~~~
即 _TestLink() 的返回值就是 server_test_link/<id> 的响应体：**必须返回 ['status'=>200,'data'=>['server_status'=>0|1,'msg'=>...]] 才能被后台识别**（proxmoxve 的裸字符串与该 UI 不兼容——原文如此，不做额外解释）。

### 2.3 _CreateAccount

| 模块 | 是否读 customfields 判重 | 成功 | 失败 |
|---|---|---|---|
| bthosts | (int)$params['customfields']['host_id'] | return 'success';（:243） | 纯字符串 '已开通,不能重复开通'（:146）或 ['status'=>'error','msg'=>$res['msg']]（:245） |
| nokvm | $params['customfields']['vserverid'] | return 'ok';（:977） | ['status'=>'error','msg'=>$res['message'] ?: '开通失败']（:979） |
| proxmoxve | 无 | return "ok";（:53） | 无（异常会 500） |
| wlkanglepro | 无 | return 'success';（:67） | ['status'=>'error','msg'=>'主机名重复']（:70）/ ['status'=>'error','msg'=>'未知错误'] |
| bthostx | $params["vserverid"] | ["status"=>"success","msg"=>$res["msg"]]（:328） | ["status"=>"error","msg"=>$res["msg"]]（:179、:330） |

bthosts.php:144-147
~~~php
    $hostid = bthosts_gethostid($params);
    if (!empty($hostid)) {
        return '已开通,不能重复开通';
    }
~~~
bthosts.php:241-245
~~~php
        $update['bwlimit'] = (int) $datas['pack[flow_max]'];
        think\Db::name('host')->where('id', $params['hostid'])->update($update);
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg']];
~~~
nokvm.php:975-980
~~~php
  		$update['bwlimit'] = (int)$post_data['flow_limit'];
		Db::name('host')->where('id', $params['hostid'])->update($update);
		return 'ok';
	}else{
		return ['status'=>'error', 'msg'=>$res['message'] ?: '开通失败'];
	}
~~~
【推断】'success' / 'ok' 都出现在随包发布的模块里，说明核心对"字符串返回值"做的是"非错误"判断；两字符串都能用，但推荐 'success'（bthosts、wlkanglepro 用）。核心判定代码本身无明文证据。

### 2.4 _SuspendAccount / _UnsuspendAccount / _TerminateAccount

| 函数 | bthosts | nokvm | proxmoxve | wlkanglepro | bthostx |
|---|---|---|---|---|---|
| Suspend | 'success' (:363) / ['status'=>'error','msg'=>..] (:365) | ['status'=>'success','msg'=>$res['message']] (:994) | **未实现** | 'success' (:100) / ['status'=>'error'] (:102) | ["status"=>"success","msg"=>"主机暂停成功"] (:457) |
| Unsuspend | 'success' (:383) | 同 (:1011) | **未实现** | 'success' (:113) | ["status"=>"success","msg"=>"主机解除暂停成功"] (:477) |
| Terminate | 'success' (:403) | ['status'=>'success','msg'=>..] (:1036) + 删 customfieldsvalues | return "ok"; (:103) 且**把 domainstatus 写成 'Terminated'**（:101） | 'success' (:126) | ["status"=>"success","msg"=>"删除成功"] (:497) |

bthosts.php:362-365
~~~php
    $res = json_decode(bthosts_post($url, $datas), true);
    if ($res['code'] == 1) {
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg']];
~~~
nokvm.php:993-997
~~~php
	if(isset($res['code']) && $res['code'] == 0){
		return ['status'=>'success', 'msg'=>$res['message']];
	}else{
		return ['status'=>'error', 'msg'=>$res['message'] ?: '暂停失败'];
	}
~~~
nokvm.php:1027-1039（删除成功顺手清自定义字段）
~~~php
	if(isset($res['code']) && $res['code'] == 0){
		// 删除成功后
		$customid = Db::name('customfields')
					->where('type', 'product')
					->where('relid', $params['productid'])
					->where('fieldname', 'vserverid')
					->value('id');
		Db::name('customfieldsvalues')->where('fieldid', $customid)->where('relid', $params['hostid'])->delete();
		return ['status'=>'success', 'msg'=>$res['message']];
	}else{
		return ['status'=>'error', 'msg'=>$res['message'] ?: '删除失败'];
	}
~~~
> 注意 proxmoxve.php:101-102  $update["domainstatus"] = "Terminated"; —— 而 shd_host.domainstatus 是 enum('Pending','Active','Suspended','Cancelled','Fraud','Completed','Deleted')，**不含 Terminated**。这是原模块的坑，新模块不要照抄。

### 2.5 _Renew

| 模块 | 有？ | 返回 |
|---|---|---|
| bthosts | 有(:527) | ['status'=>'success','msg'=>'续费成功']；失败也返回 success：['status' => 'success', 'msg' => '续费失败：' . $res['msg']]（:548） |
| bthostx | 有(:687) | ["status"=>"success","msg"=>"主机续费成功"]（:692，内部串行调 recovery+unsuspend+endtime） |
| wlkanglepro | 有(:130) | ['status'=>'success','msg'=>'续费成功'] / ['status'=>'error','msg'=>'续费失败'] |
| nokvm | **无** | — |
| proxmoxve | **无** | — |

bthosts.php:542-548
~~~php
    $datas['endtime'] = date('Y-m-d', $params['nextduedate']);
    $url = bthosts_geturl($params, '/api/vhost/host_endtime');
    $res = json_decode(bthosts_post($url, $datas), true);
    if ($res['code'] == 1) {
        return ['status' => 'success', 'msg' => '续费成功'];
    }
    return ['status' => 'success', 'msg' => '续费失败：' . $res['msg']];
~~~
**"没有 _Renew 会怎样"：核心侧行为未找到明文证据。** 可证的只有：nokvm 与 proxmoxve 两个随安装包发布的模块都没有 _Renew，所以它**不是"模块被加载/被绑定"的必要条件**。建议实现（3/5 模块实现），且实现里应把上游到期时间同步为 date('Y-m-d', $params['nextduedate'])。

### 2.6 _ChangePackage（升降级）

| 模块 | 成功 | 失败 | 关键入参 |
|---|---|---|---|
| bthosts | ['status'=>'success','msg'=>$res['msg'] ?: '修改配置成功'] (:339-340) | ['status'=>'error','msg'=>$res['msg'] ?: '修改配置失败'] (:342-343) | $params['configoptions_upgrade'][<key>] 判断哪项变了 |
| nokvm | ['status'=>'success','msg'=>$res['message'] ?: '修改配置成功'] (:1390-1391) | ['status'=>'error',...] (:1393-1394) | 同上；旧开通号可退化取 $params['old_configoptions']['customfields']['vserverid'] (:1306) |
| wlkanglepro | **污染式**：直接把上游响应数组加上 $result['status']='success' 后 return (:82-89) | 同 | — |
| bthostx | ["status"=>"success","msg"=>$res["msg"]] (:437) | ["status"=>"error","msg"=>$res["msg"]] (:439) | $params["configoptions_upgrade"]["bt_site"] 等 |

bthosts.php:307-320
~~~php
        if (isset($params['configoptions_upgrade']['site_max'])) {
            $post_data['site_max'] = $params['configoptions']['site_max'];
        }
        if (isset($params['configoptions_upgrade']['sql_max'])) {
            $post_data['sql_max'] = $params['configoptions']['sql_max'];
        }
        if (isset($params['configoptions_upgrade']['domain_num'])) {
            $post_data['domain_max'] = $params['configoptions']['domain_num'];
        }
~~~
nokvm.php:1389-1395
~~~php
	if(isset($res['code']) && $res['code'] == 0){ # 配置改变才变
		$result['status'] = 'success';
		$result['msg'] = $res['message'] ?: '修改配置成功';
	}else{
		$result['status'] = 'error';
		$result['msg'] = $res['message'] ?: '修改配置失败';
	}
~~~

### 2.7 _CrackPassword

签名 ($params, $new_pass)（两参数，第二个是**核心传入的新明文密码**）。

bthosts.php:502-505
~~~php
    if (isset($res['code']) && $res['code'] == 1) {
        return ['status' => 'success', 'msg' => '密码重置成功'];
    }
    return ['status' => 'error', 'msg' => $res['msg'] ?: '密码重置失败'];
~~~
nokvm.php:1243-1247 结构相同（msg 取 $res['message']，失败文案写的是 '同步失败'，原文如此）。
wlkanglepro.php:177-180 相同。bthostx.php:662-665 相同（额外先调 bthostx_endtime($params)）。

### 2.8 _Sync

只有 bthosts 与 nokvm 实现（wlkanglepro/proxmoxve 无）。

bthosts.php:426-433
~~~php
    if ($res['code'] == 1 && $result['code'] == 1) {
        $update['domain'] = $result['data']['username'];
        $update['username'] = $result['data']['username'];
        $update['password'] = cmf_encrypt($result['data']['password']);
        think\Db::name('host')->where('id', $params['hostid'])->update($update);
        return ['status' => 'success', 'msg' => $res['msg']];
    }
    return ['status' => 'error', 'msg' => $res['msg'] ?: $result['msg'] ?: '同步失败'];
~~~
nokvm.php:1294-1298
~~~php
		Db::name('host')->where('id', $params['hostid'])->update($update);
		return ['status'=>'success', 'msg'=>$res['message']];
	}else{
		return ['status'=>'error', 'msg'=>$res['message'] ?: '同步失败'];
	}
~~~

### 2.9 _Status

成功：['status'=>'success','data'=>['status'=>'on|off|process|suspend|waiting|unknown','des'=>'中文描述']]

bthosts.php:450-479
~~~php
    if ($res['code'] == 1) {
        $result['status'] = 'success';
        if ($res['data']['loca'] == 'normal') {
            $result['data']['status'] = 'on';
            $result['data']['des'] = '运行中';
        } else {
            if ($res['data']['loca'] == 'locked') {
                $result['data']['status'] = 'off';
                $result['data']['des'] = '暂停';
...
        return $result;
    }
    $result['data']['status'] = 'unknown';
    $result['data']['des'] = '未知';
    return $result;
~~~
nokvm.php:1460-1480：in_array($res['data']['code'],[1,12]) → 'on'；[2,3] → 'off'；[4,5,8,11] → 'process'；13|14 → 'suspend'；否则 'unknown'；失败 return ['status'=>'error','msg'=>$res['message'] ?: '获取失败'];。
wlkanglepro.php:155-167：成功 $result['status']='success' + on/off；失败（连不上）仍然返回 $result['data']['status']='unknown' **不带 status 键**。
bthostx.php:606-642 是**异形**：$result = $res["data"]; 然后 $result["status"] 被覆盖成 HTML 按钮串、$result["data"]["status"] 放 normal/locked/expired/excess/off/unknown。不要照抄。

### 2.10 _ClientArea / _ClientAreaOutput

_ClientArea($params) 返回 ['<key>'=>['name'=>'<分页标题>']]：

bthosts.php:590-593
~~~php
function bthosts_ClientArea($params)
{
    return ['index' => ['name' => '主机信息'], 'status' => ['name' => '使用情况']];
}
~~~
proxmoxve.php:76-79  return ["goPanel" => ["name" => "控制面板信息"]];
nokvm.php:271-322 按 $params['configoptions']['nat'] 等条件动态增删 snapshot/security_group/backups/cd_rom/nat_acl/nat_web 分页（unset($panel['nat_acl']) 等）。

_ClientAreaOutput($params, $key) 返回 ['template'=>'templates/xxx.html','vars'=>[...]]：

bthosts.php:594-607
~~~php
function bthosts_ClientAreaOutput($params, $key)
{
    $hostid = bthosts_gethostid($params);
    if (empty($hostid)) {
        return '';
    }
    $info = bthosts_hostinfo($params, $hostid);
    if ($key == 'index') {
        return ['template' => 'templates/information.html', 'vars' => ['params' => $params, 'info' => $info]];
    }
    if ($key == 'status') {
        return ['template' => 'templates/status.html', 'vars' => ['params' => $params, 'info' => $info]];
    }
}
~~~
nokvm.php:339-344 用 'vars'=>['list'=>$res['data']]，模板侧 {foreach $list as $key=>$vo }。
proxmoxve.php:80-86  return ["template" => "templates/gopanel.htm", "vars" => ["url" => $url, "username" => $params["username"], "password" => $params["password"]]];
bthostx.php:741-806 试图返回**原始 HTML 字符串**，但 switch 写错（case "index" 只赋值 $output 后 break，函数末尾没有 return）→ 实际返回 null。这是原作者 bug，**不要照抄**；证据表明约定是 template + vars。
分页 key 与 _ClientArea 的 key 一一对应；_ClientArea 没声明的 key（如 wlkanglepro 的 tips）在 _ClientAreaOutput 里也有分支（wlkanglepro.php:192-194）。

### 2.11 _AllowFunction / _AdminButton / _AdminButtonHide / _ClientButton

bthosts.php:608-621
~~~php
function bthosts_AllowFunction()
{
    return ['client' => ['Sync'], 'admin' => ['Resource']];
}
function bthosts_AdminButton($params)
{
    $button = ['Resource' => '资源稽核', 'Stop' => '主机停用', 'UnsuspendAccount' => '主机开启', 'TerminateAccount' => '放入回收站', 'Recovery' => '从回收站恢复', 'UnSpeed' => '解除限速'];
    return $button;
}
function bthosts_ClientButton($params)
{
    $button = ['Sync' => ['place' => 'console', 'name' => '主机同步']];
    return $button;
}
~~~
nokvm.php:428-432（只声明 client，17 个自定义方法）
~~~php
function nokvm_AllowFunction(){
	return [
		'client'=>['CreateSnap','DeleteSnap','RestoreSnap','CreateBackup','DeleteBackup','RestoreBackup','CreateSecurityGroup','DeleteSecurityGroup','ApplySecurityGroup','ShowSecurityGroupAcl','CreateSecurityGroupAcl','DeleteSecurityGroupAcl','MountCdRom','UnmountCdRom','addNatAcl','delNatAcl','addNatWeb','delNatWeb'],
	];
}
~~~
bthostx.php:807-820：AllowFunction 声明 ["client"=>["Sync"],"admin"=>["Resource"]]，**但 bthostx.php 里根本没有 bthostx_Sync() 函数**（对照 2.8）；AdminButtonHide 与 AdminButton 内容完全相同（["SuspendAccount"=>"锁定面板","UnsuspendAccount"=>"主机启用","Stop"=>"主机停用","Recovery"=>"回收站恢复","Resource"=>"资源稽核","Sync"=>"主机同步"]）。

nokvm.php:1717-1723（按钮隐藏，按条件返回要隐藏的方法名数组）
~~~php
function nokvm_AdminButtonHide($params){
	if(!empty(nokvm_GetServerid($params)) && $params['serverid']>0){
		return ['CreateAccount'];
	}else{
		return ['SuspendAccount','UnsuspendAccount','TerminateAccount','On','Off','Reboot','HardOff','HardReboot','Reinstall','CrackPassword','Vnc','Sync'];
	}
}
~~~
被声明的自定义方法在客户端是"POST 该函数名 + $params"进来，函数内部用 input('post.') 取参（nokvm.php:439-440）：
~~~php
	// 通过post接受自定义参数
	$post = input('post.');
~~~
并且返回前调用核心日志助手 active_logs()：nokvm.php:462-464
~~~php
    active_logs($description,$params['uid'],2);
    active_logs($description,$params['uid'],2,2);
    return $result;
~~~
（active_logs() / randStr() 的定义在加密的 app/common.php，**未找到明文证据**；cmf_encrypt() 有明文定义。）

### 2.12 定时任务 / 流量包 / 图表（可选钩子）

~~~php
function nokvm_FiveMinuteCron(){        // nokvm.php:1523  无参数
function nokvm_DailyCron(){             // nokvm.php:1605  无参数
function nokvm_FlowPacketPaid($params){ // nokvm.php:1655  有参数
function nokvm_Chart(){                 // nokvm.php:156   无参数
function nokvm_ChartData($params){      // nokvm.php:181   有参数
~~~
- _FiveMinuteCron() / _DailyCron() **不带 $params**，自己用 Db 联表捞数据；nokvm.php:1525-1542 的 SQL 是本手册最有价值的一段——它把"主机 ↔ 产品 ↔ 服务器 ↔ 服务器组 ↔ 自定义字段"的关联写全了：
~~~php
    $host_data = Db::name('host')
            ->alias('a')
            ->field('a.id,a.domainstatus,a.suspendreason,a.uid,c.ip_address server_ip,
					c.hostname server_host,c.username server_username,c.password server_password,c.accesshash,c.secure,c.port,f.value vserverid')
            ->leftJoin('products b', 'a.productid=b.id')
            ->leftJoin('servers c', 'a.serverid=c.id')
            ->leftJoin('server_groups d', 'c.gid=d.id')
            ->leftJoin('customfields e', 'a.productid=e.relid AND e.type="product" AND e.fieldname="vserverid"')
            ->leftJoin('customfieldsvalues f', 'e.id=f.fieldid and a.id=f.relid')
            ->whereIn('a.domainstatus', 'Active,Suspended')
            ->where('a.nextduedate=0 OR a.nextduedate>'.$time)
            ->where('a.serverid', '>', 0)
            ->whereIn('b.api_type', ['','normal'])
            ->where('d.system_type', 'normal')
            ->where('d.type', 'nokvm')
            ->where('f.value', '>', 0)
            ->select()->toArray();
    $host = new \app\common\logic\Host();
~~~
  并在 :1545 手动解密服务器密码：$v['server_password'] = aesPasswordDecode($v['server_password']);
  超额暂停/恢复调用核心逻辑类：$host->suspend($v['id'], 'flow', '用量超额')（:1558）、$host->unsuspend($v['id'], 0, '', true)（:1580），返回 ['status'=>200,...]；任务队列用 app\common\logic\RunMap + app\common\model\HostModel::isZjmfApi() + $logic_run_map->saveMap($data_i,1,400,2)（:1560-1574）。
- _Chart() 返回 ['cpu'=>['title'=>'CPU'], 'disk'=>['title'=>'磁盘IO','select'=>[['name'=>'系统盘','value'=>'vda'],...]], 'flow'=>['title'=>'流量图']]（nokvm.php:156-178）；_ChartData($params) 从 $params['chart']['start']（毫秒）/['type']/['select'] 取值，成功返回 ['status'=>'success','data'=>['unit'=>'%','chart_type'=>'line','list'=>[[['time'=>..,'value'=>..]]],'label'=>['CPU使用率(%)']]]（:196-264），失败 ['status'=>'error','msg'=>'数据获取失败']（:184,266）。
- _FlowPacketPaid($params)：流量包付款后回调，读 $params['configoptions']['flow_limit']、$params['suspendreason']、$params['domainstatus']，失败 return false;（:1658）。

### 2.13 _downloadResource（模块自身升级包）

只有 nokvm 实现。nokvm.php:1834-1848
~~~php
function nokvm_downloadResource()
{
    $metaData = nokvm_MetaData();

    $result = [
        'status' => 200,
        'msg'	 => '请求成功',
        'data'	 => [
            'module' => 'nokvm',
            'url' => request()->domain() . '/plugins/servers/nokvm/data/abc.zip' , // 下载路径
            'version' => $metaData['version'] ?? '1.0.0',
        ]
    ];
    return $result;
}
~~~
（注意它引用了 request()（ThinkPHP 助手）与 _MetaData()['version']。）

### 2.14 必须 / 可选 汇总（按"随包发布模块是否实现"统计）

| 函数 | bthosts | nokvm | proxmoxve | wlkanglepro | bthostx(V10) | 结论 |
|---|---|---|---|---|---|---|
| _MetaData | Y | Y | Y | Y | Y | 必备 |
| _ConfigOptions | Y | Y | Y | Y | Y | 必备 |
| _TestLink | Y | Y | Y | Y | Y | 必备（后台"状态"列依赖） |
| _CreateAccount | Y | Y | Y | Y | Y | 必备 |
| _SuspendAccount | Y | Y | N | Y | Y | 强烈建议 |
| _UnsuspendAccount | Y | Y | N | Y | Y | 强烈建议 |
| _TerminateAccount | Y | Y | Y | Y | Y | 必备 |
| _Renew | Y | N | N | Y | Y | 可选 |
| _ChangePackage | Y | Y | N | Y | Y | 可选 |
| _Sync | Y | Y | N | N | N | 可选 |
| _Status | Y | Y | Y | Y | Y | 建议 |
| _CrackPassword | Y | Y | N | Y | Y | 可选 |
| _ClientArea / _ClientAreaOutput | Y | Y | Y | Y | Y | 必备（会员中心产品页） |
| _AllowFunction | Y | Y | N | N | Y | 可选 |
| _AdminButton | Y | N | N | N | Y | 可选 |
| _AdminButtonHide | N | Y | N | N | Y | 可选 |
| _ClientButton | Y | N | N | N | N | 可选 |
| _FiveMinuteCron / _DailyCron | N | Y | N | N | N | 可选 |
| _Chart / _ChartData | N | Y | N | N | N | 可选 |
| _FlowPacketPaid | N | Y | N | N | N | 可选 |
| _idcsmartauthorizes | Y | N(注释) | Y | Y | Y | 空实现，建议保留 |
| _downloadResource | N | Y | N | N | N | 可选 |

---

## 3. $params 完整键名清单

以下为对 5 个模块源码做正则全量提取（$params['x'] 与 $params["x"] 两种引号）的结果，共 **26 个顶层键**，每个键给出出处与用法原文。

### 3.1 服务器（servers）相关 —— 核心已从 shd_servers 行组装并**解密**

| 键 | 出处（首次） | 原文片段 | 对应 DB 列（nokvm.php:1527-1528 自查 SQL 直接证明） |
|---|---|---|---|
| server_ip | bthosts.php:32 | $url .= $params['server_ip'] ?: $params['server_host']; | c.ip_address server_ip |
| server_host | bthosts.php:32 | 同上 | c.hostname server_host |
| server_username | proxmoxve.php:20 | new ProxmoxApi\ProxmoxClient($params["server_ip"], $params["server_username"], $params["server_password"], "pam") | c.username server_username |
| server_password | nokvm.php:140 | $sign = nokvm_CreateSign($params['server_password']); | c.password server_password（**已解密**：nokvm.php:1545 手工取库时必须自己 aesPasswordDecode()） |
| accesshash | bthosts.php:91 | $data['token'] = $params['accesshash']; | c.accesshash |
| secure | bthosts.php:27 | if ($params['secure']) { $url = 'https://'; } | c.secure |
| port | bthosts.php:33 | if (!empty($params['port'])) { $url .= ':' . $params['port']; } | c.port |
| serverid | nokvm.php:1718 | if(!empty(nokvm_GetServerid($params)) && $params['serverid']>0){ | host.serverid |

### 3.2 主机（host）相关

| 键 | 出处 | 原文片段 |
|---|---|---|
| hostid | bthosts.php:228 | ->where('relid', $params['hostid'])（62 处使用） |
| productid | bthosts.php:223 | ->where('relid', $params['productid']) |
| uid | nokvm.php:462 | active_logs($description,$params['uid'],2); |
| domain | bthosts.php:159 | $infos['username'] = $params['domain']; |
| username | proxmoxve.php:35 | $params["username"] = $hostusername;（**可写**：模块内赋值后自己再落库） |
| password | bthosts.php:148 | if (empty($params['password'])) { $sys_pwd = randStr(8); } else { $sys_pwd = $params['password']; }（**明文**，见 wlkanglepro/templates/information.html:250 的 {$params.password}） |
| nextduedate | bthosts.php:217 | $datas['endtime'] = date('Y-m-d', $params['nextduedate']);（int 时间戳） |
| domainstatus | nokvm.php:1683 | if($params['domainstatus'] == 'Suspended' && ...) |
| suspendreason | nokvm.php:1681 | $suspendreason = explode('-', $params['suspendreason'])[0]; |
| reinstall_os | nokvm.php:1196 | if(empty($params['reinstall_os'])){ |
| reinstall_os_name | nokvm.php:1218 | if(stripos($params['reinstall_os_name'], 'win') !== false){ |

### 3.3 产品配置项（核心已组装）

| 键 | 出处 | 原文片段 |
|---|---|---|
| configoptions | bthosts.php:172 / nokvm.php:272 / proxmoxve.php:39 / wlkanglepro.php:48 / bthostx.php:187 | if ($params['configoptions']['type'] == 1) { ；if($params['configoptions']['nat']==1){ ；if ($params["configoptions"]["privileged"] == "是") { |
| configoptions_upgrade | bthosts.php:307 / nokvm.php:1313 / bthostx.php:391 | if (isset($params['configoptions_upgrade']['site_max'])) {（用于 _ChangePackage 判断"哪一项变了"） |
| old_configoptions | nokvm.php:1306 | $vserverid = intval($params['old_configoptions']['customfields']['vserverid']);（升级前快照；**含 customfields 子键**） |

$params['configoptions'] 的全部子键（按模块 _ConfigOptions() 的 key 命名，实测 88 个）：

- nokvm：Location, nodes_id, Memory, net_out, flow_limit, Backups, nat_acl_limit, 'Extra IP Address', os, CPU, 'Disk Space', net_in, Snapshot, cpu_mode, nat, nat_web_limit（与 nokvm.php:14-136 的 key 一一对应；第 9 个选项 key=>'' 因此取不到，代码改用第 10 个 'os'）
- bthosts：type, plans_id, sort_id, port, domain_num, web_back_num, sql_back_num, domainpools_id, ippools_id, ip_num, phpver, perserver, limit_rate, site_max, sql_max, flow_max, sub_bind
- proxmoxve：panel, node, cores, memory, swap, privileged, ostemplate, disksize, storage, bridge, speed, ip
- wlkanglepro：type, product_id, cdn, subdir, web_quota, db_quota, max_subdir, subdir_flag, flow_limit, domain, speed_limit, max_connect, access, log_file, log_handle, ssi, htaccess, port
- bthostx：way, parameter1..parameter20, bt_site, bt_sql, bt_domain, bt_flow, bt_webback, bt_sqlback, bt_ipnum, bt_perserver, bt_limit（后 9 个 **不在** _ConfigOptions() 里，仅出现在 configoptions / configoptions_upgrade 判断中）

$params['configoptions_upgrade'] 的子键集合与上面不同（实测：site_max,sql_max,domain_num,web_back_num,flow_max,sql_back_num,sub_bind / CPU,Memory,'Disk Space','Network Speed',net_out,net_in,flow_limit,'Extra IP Address',Snapshot,Backups / bt_* 与 parameter2,parameter6），语义 = "本次变更涉及的配置项集合"，只用于 isset() 判断。

### 3.4 自定义字段 / 用户信息 / 图表

| 键 | 出处 | 原文片段 |
|---|---|---|
| customfields | bthosts.php:85 | return (int) $params['customfields']['host_id']; |
| | nokvm.php:1727 | return (int)$params['customfields']['vserverid']; |
| user_info | nokvm.php:879 | $post_data['username'] = ($params['user_info']['phonenumber'] ?: $params['user_info']['email']) ?: $params['user_info']['username']; |
| chart | nokvm.php:194/198/233 | $start = $params['chart']['start']/1000; ；if($params['chart']['type'] == 'cpu') ；$v['Disk'][$params['chart']['select']] |

$params['customfields'] 是核心按 **fieldname** 组装好的便捷数组，值来自 customfieldsvalues；nokvm.php:1728-1735 把等价的查库写法注释在旁边，可直接当"核心做了什么"的证据：
~~~php
function nokvm_GetServerid($params){
	return (int)$params['customfields']['vserverid'];
	// return Db::name('customfields')
	// 		->alias('a')
	// 		->leftJoin('customfieldsvalues b', 'a.id=b.fieldid')
	// 		->where('a.type', 'product')
	// 		->where('a.relid', $params['productid'])
	// 		->where('a.fieldname', 'vserverid')
	// 		->where('b.relid', $params['hostid'])
	// 		->value('value');
}
~~~

### 3.5 "已组装/已解密" vs "必须自己查库"

**核心在调用插件前已给好（证据见上表最后一列）：**
- host 行：hostid, uid, productid, domain, username, password(明文), nextduedate, domainstatus, suspendreason
- products 行：productid，以及由 config_option1..24 与模块 _ConfigOptions() 位置对齐后按 key 命名的 configoptions
- servers 行：serverid, server_ip, server_host, server_username, server_password(已解密), accesshash, secure, port
- customfields（按 fieldname）、user_info（username/email/phonenumber）、chart（_ChartData 专用）

**模块必须自己查库的：**
- customfieldsvalues 的**写入**（bthosts.php:223-234、nokvm.php:909-940）
- 自定义字段的**建列**：customfields 表 type='product', relid=productid, fieldname=..., adminonly=1
- 服务器密码**在定时任务里**（nokvm.php:1545 → aesPasswordDecode()）
- 产品配置项/自定义字段的原始行（nokvm.php:952-959 查 host_config_options 联 product_config_options / product_config_options_sub 拿操作系统名）

### 3.6 用户提到的但"未找到读取证据"的键

- $params['bwlimit']、$params['bwusage']、$params['dedicatedip']、$params['assignedips']、$params['os'] —— **这些是模块写回 host 表的字段（见第 4 节），没有任何模块把它们从 $params 里读出来**。它们是否也被核心塞进 $params：未找到证据。

---

## 4. 写库约定

### 4.1 主表 shd_host（建表原文，public/install/thinkcmf.sql）

（为在纯文本手册中排版，字段名引号已去掉，类型与注释与原文一致）
~~~sql
domainstatus enum('Pending','Active','Suspended','Cancelled','Fraud','Completed','Deleted') NOT NULL DEFAULT 'Pending' COMMENT '状态',
username varchar(255) NOT NULL DEFAULT '' COMMENT '用户名',
password varchar(255) NOT NULL DEFAULT '' COMMENT '密码',
dedicatedip text NOT NULL COMMENT '独立ip地址',
assignedips text NOT NULL COMMENT '分配的ip地址',
diskusage int(10) NOT NULL DEFAULT '0',
disklimit int(10) NOT NULL DEFAULT '0',
bwusage decimal(10,2) NOT NULL DEFAULT '0.00',
bwlimit int(10) NOT NULL DEFAULT '0',
os varchar(255) NOT NULL DEFAULT '' COMMENT '显示的操作系统',
suspendreason text NOT NULL COMMENT '暂停原因',
nextduedate int(10) NOT NULL DEFAULT '0' COMMENT '到期时间',
~~~

**开通成功写回（bthosts 原文，bthosts.php:235-242）：**
~~~php
        $mainip = $params['server_ip'];
        $update['dedicatedip'] = $mainip;
        $update['domainstatus'] = 'Active';
        $update['username'] = $arr['data']['username'];
        $update['password'] = cmf_encrypt($sys_pwd);
        $update['domain'] = $arr['data']['username'];
        $update['bwlimit'] = (int) $datas['pack[flow_max]'];
        think\Db::name('host')->where('id', $params['hostid'])->update($update);
~~~

**nokvm 原文（nokvm.php:965-976，多了 assignedips / os）：**
~~~php
		$update['dedicatedip'] = $mainip;
		$update['assignedips'] = implode(',', $ip);
		// 存入服务器密码
		$update['domainstatus'] = 'Active';
		$update['username'] = $username;
		$update['password'] = cmf_encrypt($sys_pwd);
		$update['domain'] = $res['data']['name'];
		if(empty($os_info)){
			$update['os'] = $post_data['templates_id'];
		}
  		$update['bwlimit'] = (int)$post_data['flow_limit'];
		Db::name('host')->where('id', $params['hostid'])->update($update);
~~~

**同步时写用量（nokvm.php:1271-1294）：**
~~~php
		$update['dedicatedip'] = $mainip;
		$update['assignedips'] = implode(',', $ip);
		$update['password'] = cmf_encrypt($res['data']['sys_pwd']);
		$update['domain'] = $res['data']['name'];
		if(is_numeric($res['data']['flow']['code']) && $res['data']['flow']['code'] == 0){
  			$update['bwusage'] = round(($res['data']['flow']['data']['in'] + $res['data']['flow']['data']['out'])/1024/1024/1024, 2);
  			if(is_numeric($res['data']['flow_limit'])){
  				$update['bwlimit'] = (int)$res['data']['flow_limit'];
  			}
  		}
~~~

### 4.2 password 用哪个加密函数

- **经典模块一律用 cmf_encrypt()**：bthosts.php:239,429、nokvm.php:970,1273、proxmoxve.php:50、wlkanglepro.php:64（全量 grep 命中仅这些 + 定义处）。
- **明文定义存在**：vendor/thinkcmf/cmf/src/common.php:2282
~~~php
function cmf_encrypt($string)
{
    //$applicationConfig = DI::make("config");
    $cc_encryption_hash = config('crypt.cc_encryption_hash');
    $key = md5(md5($cc_encryption_hash)) . md5($cc_encryption_hash);
    $hash_key = _hash($key);
    $hash_length = strlen($hash_key);
    $iv = _generate_iv();
    ...
    return base64_encode($out);
}
~~~
  依赖 app/config/crypt.php 的 cc_encryption_hash（**该文件是 ionCube 加密的，密钥值不可读**——但不需要，直接调用即可）。
- aesPasswordDecode()（解密 servers.password）与 password_encrypt()（V10 bthostx 用）、randStr()、active_logs() 的定义：在 public/、vendor/ 全量 grep **零命中** → 都在加密的 app/common.php，**未找到明文证据**。
- 对应的明文证据：nokvm.php:1545  $v['server_password'] = aesPasswordDecode($v['server_password']);（证明库里是 AES 存、核心解密后再给插件）。

### 4.3 customfields / customfieldsvalues（nokvm 原文全段）

建表：shd_customfields(type, relid, fieldname, fieldtype, description, fieldoptions, regexpr, adminonly, required, showorder, showinvoice, sortorder, create_time, update_time)；shd_customfieldsvalues(fieldid, relid, value, create_time, update_time)，relid 注释为 "hostid或者客户ID或者工单id"。

nokvm.php:907-940
~~~php
	if(isset($res['code']) && $res['code'] == 0){
		// 存入产品自定义字段
		$customid = Db::name('customfields')
					->where('type', 'product')
					->where('relid', $params['productid'])
					->where('fieldname', 'vserverid')
					->value('id');
		if(empty($customid)){
			// 添加自定义字段
			$customfields = [
				'type'=>'product',
				'relid'=>$params['productid'],
				'fieldname'=>'vserverid',
				'fieldtype'=>'text',
				'adminonly'=>1,
				'create_time'=>time()
			];
			$customid = Db::name('customfields')->insertGetId($customfields);
		}
		$exist = Db::name('customfieldsvalues')
				->where('fieldid', $customid)
				->where('relid', $params['hostid'])
				->find();
		if(empty($exist)){
			$data = [
				'fieldid'=>$customid,
				'relid'=>$params['hostid'],
				'value'=>$res['data']['id'],
				'create_time'=>time()
			];
			Db::name('customfieldsvalues')->insert($data);
		}else{
			Db::name('customfieldsvalues')->where('id', $exist['id'])->update(['value'=>$res['data']['id']]);
		}
~~~
bthosts 完全同构，只是字段名是 host_id、值取 $res['data']['site']['id']（bthosts.php:223-234）。
nokvm 的 _ChangePackage 里又重复了一遍同样的 upsert（nokvm.php:1397-1431），值用 $res['data']['id'] ?? $vserverid。

### 4.4 V10 关联表（bthostx，仅作对照）

bthostx/model/IdcsmartHostModel.php
~~~php
namespace server\idcsmart_common\module\bthostx\model;

class IdcsmartHostModel extends \think\Model
{
    protected $name = "host";
    protected $schema = ["id" => "string", "due_time" => "string"];
}
~~~
bthostx/model/IdcsmartModel.php
~~~php
namespace server\idcsmart_common\module\bthostx\model;

class IdcsmartModel extends \think\Model
{
    protected $name = "configuration";
    protected $schema = ["setting" => "string", "value" => "string"];
}
~~~
写回（bthostx.php:320-327）：
~~~php
        $HostModel = new app\common\model\HostModel();
        $HostModel->where("id", $params["hostid"])->update(["status" => "Active"]);
        $IdcsmartCommonServerHostLinkModel = new server\idcsmart_common\model\IdcsmartCommonServerHostLinkModel();
        $update["dedicatedip"] = $server_ip;
        $update["username"] = $arr["data"]["username"];
        $update["password"] = password_encrypt($sys_pwd);
        $update["vserverid"] = $res["data"]["site"]["id"];
        $IdcsmartCommonServerHostLinkModel->where("host_id", $params["hostid"])->update($update);
~~~
反查（bthostx.php:707-708、:747-748）：
~~~php
    $host_id = server\idcsmart_common\model\IdcsmartCommonServerHostLinkModel::where("vserverid", $hostid)->value("host_id");
    ...
    $username = $hostModel->where("vserverid", $hostid)->value("username");
~~~
→ 关联表列：host_id, vserverid, username, password, dedicatedip（其余列未找到明文证据；表名/SQL 未在安装包中出现）。

---

## 5. 签名算法与请求

### 5.1 bthosts / bthostx（同一套：md5(sort(time,random,token)) 转大写）

bthosts.php:14-23（bthostx.php:11-20 完全相同，只是双引号）
~~~php
function bthosts_CreateSign($time, $random, $token)
{
    $data['time'] = $time;
    $data['random'] = $random;
    $data['token'] = $token;
    sort($data, SORT_STRING);
    $str = implode($data);
    $signature = md5($str);
    return strtoupper($signature);
}
~~~
调用点（bthosts.php:89-97）——注意 **token 不参与发送，只参与签名**：
~~~php
    $data['time'] = time();
    $data['random'] = mt_rand();
    $data['token'] = $params['accesshash'];
    $datas = $data;
    unset($datas['token']);
    $datas['signature'] = bthosts_createsign($data['time'], $data['random'], $data['token']);
    $datas['id'] = $hostid;
~~~
- _TestLink 走 **GET**，签名参数放 query（bthosts.php:130-131）：$url = bthosts_geturl($params, '/api/vhost/index', $datas); $res = json_decode(bthosts_get($url), true);
- 其它全走 **POST form-urlencoded**（bthosts.php:59-82）：$o .= $k . '=' . urlencode($v) . '&'; → CURLOPT_POSTFIELDS，CURLOPT_POST=1，header 只有 User-Agent: Mozilla/5.0 ... Chrome/91.0.4472.124 Safari/537.36，CURLOPT_SSL_VERIFYPEER=false（只 GET 里设了）。
- URL 拼装（bthosts.php:24-45）：scheme = $params['secure'] ? 'https://' : 'http://'；host = $params['server_ip'] ?: $params['server_host']；if(!empty($params['port'])) host .= ':'.$params['port']；再拼 path 与 query。
- 错误处理：只判业务码 if ($res['code'] == 1) {...} else { return ['status'=>'error','msg'=>$res['msg']]; }；**没有** curl 错误判断。

### 5.2 nokvm（md5(sort(timeStamp,randomStr,token)) 转大写，返回 time/random/signature 三元组）

nokvm.php:1739-1751
~~~php
function nokvm_CreateSign($token = ''){
	$data['timeStamp'] = time();
	$data['randomStr'] = randStr(6); 
	$data['token'] = $token;
	$res['time'] = $data['timeStamp'];
	$res['random'] = $data['randomStr'];
	sort($data, SORT_STRING);
	$str = implode($data);
	$signature = md5($str);
	$signature = strtoupper($signature);
	$res['signature'] = $signature;
	return $res;
}
~~~
**token = $params['server_password']**（不是 accesshash！nokvm.php:140  $sign = nokvm_CreateSign($params['server_password']);），且签名随 URL query 发送（nokvm.php:1753-1773 nokvm_GetUrl）。
请求统一走 nokvm_Curl($url, $data, $timeout, $request, $header)（nokvm.php:1775-1831）：
- GET：CURLOPT_HTTPGET=1，data 拼到 URL；
- POST：CURLOPT_POST=1 + CURLOPT_POSTFIELDS=http_build_query($data)（表单）；
- PUT / DELETE：CURLOPT_CUSTOMREQUEST + http_build_query body；
- 统一 CURLOPT_TIMEOUT=$timeout、CURLOPT_USERAGENT='WHMCS'、CURLOPT_FOLLOWLOCATION=1、CURLOPT_SSL_VERIFYPEER=false、CURLOPT_SSL_VERIFYHOST=false；
- 错误处理（唯一有 curl 错误分支的模块，:1823-1830）：
~~~php
    $res = curl_exec($curl);
    $error = curl_error($curl);
    if(!empty($error)){
    	return ['status'=>500, 'message'=>'CURL ERROR:'.$error];
    }
    $info = curl_getinfo($curl);
    curl_close($curl);
    return json_decode($res, true);
~~~
  业务码：if(isset($res['code']) && $res['code'] == 0) 视为成功（code==0，与 bthosts 的 code==1 相反！）。

### 5.3 wlkanglepro（md5(a . accesshash . r) + GET query）

~~~php
function wlkanglepro_CreateSign($a, $skey, $r)   // wlkanglepro.php:14-17
{
    return md5($a . $skey . $r);
}
function wlkanglepro_GetUrl($params, $info, $skey, $r)   // :18-25
{
    $url = '';
    foreach ($info as $k => $v) { $url .= $k . '=' . $v . '&'; }
    return 'http://' . $params['server_ip'] . ':' . $params['port'] . '/api/index.php?' . $url . 'r=' . $r . '&s=' . $skey . '&json=1';
}
~~~
请求方式：json_decode(file_get_contents($url), true)（wlkanglepro.php:33,59,81,98,...），业务码 $res['result'] == 200。**无 curl、无 header、无错误处理**。

### 5.4 proxmoxve（无自定义签名，走 PVE ticket + CSRF）

proxmoxve.php:20：new ProxmoxApi\ProxmoxClient($params["server_ip"].":".$params["port"], $params["server_username"], $params["server_password"], "pam")；
ProxmoxClient.pve:109,113：
~~~php
        curl_setopt($curl, CURLOPT_COOKIE, "PVEAuthCookie={$this->ticket}");
        ...
        curl_setopt($curl, CURLOPT_HTTPHEADER, [ "CSRFPreventionToken: {$this->CSRFPreventionToken}" ]);
~~~

### 5.5 给 ShitIDC(Go) 的落地建议（基于以上证据的工程选择）

bthosts 那套最省事：上游只需实现 GET /api/vhost/index?time&random&signature 与若干 POST <path> 表单接口，验签 = strtoupper(md5(implode(sort([time, random, accesshash]))))。见第 7 节骨架。

---

## 6. ConfigOptions 声明与后台绑定

### 6.1 两个真实 _ConfigOptions() 原文

proxmoxve.php:14-17（**唯一使用 placeholder 的模块**，最完整）
~~~php
function proxmoxve_ConfigOptions()
{
    return [["type" => "text", "name" => "ProxmoxVE面板地址(面向用户)", "placeholder" => "https://pve.example.com:8006/", "description" => "ProxmoxVE面板地址，面向用户，必须以https://开头，结尾必须以/结尾", "key" => "panel"], ["type" => "text", "name" => "开通节点名称", "placeholder" => "pve", "description" => "PVE面板内显示的节点名称", "key" => "node"], ["type" => "text", "name" => "CPU数量", "placeholder" => "请输入CPU数量", "description" => "CPU数量，单位为[个]，必须为整数", "key" => "cores"], ... ["type" => "text", "name" => "IP地址范围", "placeholder" => "请输入IP地址范围", "description" => "IP地址范围，最后一位不写，如：[172.16.1.]", "key" => "ip"]];
}
~~~
bthosts.php:10-13（dropdown 的两种 options 写法）
~~~php
function bthosts_ConfigOptions()
{
    return [['type' => 'dropdown', 'name' => '开通方式', 'description' => '', 'options' => ['自定义配置', '套餐开通', '弹性开通'], 'key' => 'type'], ['type' => 'text', 'name' => '套餐ID', 'description' => '弹性和自定义开通请留空', 'key' => 'plans_id'], ['type' => 'text', 'name' => '分类ID', 'description' => '默认为1', 'default' => '1', 'key' => 'sort_id'], ... ['type' => 'dropdown', 'name' => '绑定子目录', 'description' => '此为高危操作，不建议开启', 'options' => ['不允许', '允许'], 'key' => 'sub_bind']];
}
~~~
第三份（yesno）：nokvm.php:14-136，例如
~~~php
		[
			'type'=>'yesno', 
			'name'=>'NAT产品', 
			'description'=>'勾选则表示为NAT产品', 
			'default'=>'1',
			'key'=>'nat',
		],
~~~
第四份（"值_标签" 写法）：wlkanglepro.php:12  ['type' => 'dropdown', 'name' => '绑定子目录', 'options' => ['1' => '1_允许', '0' => '0_不允许'], 'key' => 'subdir_flag']。

### 6.2 每个 key 的作用（后台渲染代码为证据）

public/admin/js/EditProduct~3b812b8f.b6d6eee3.js（模块配置项渲染块，紧跟 e.modulesList.length&&"normal"===e.editProductFormData.api_type 之后）：
~~~js
a("el-form-item",{attrs:{label:t.name}}, ... 
  "text"===t.type ? a("el-input",{attrs:{size:"small",placeholder:t.placeholder,...}}) :
  "password"===t.type ? a("el-input",{attrs:{...,"show-password":""}}) :
  "yesno"===t.type ? a("el-checkbox",{attrs:{"true-label":"1","false-label":"0",...}},[e._v(e._s(t.description))]) :
  "radio"===t.type ? a("el-radio-group",...,e._l(t.options,(t)=>a("el-radio",{key:t.value,attrs:{label:t.value}},[e._v(e._s(t.name))]))) :
  "dropdown"===t.type ? a("el-select",...,e._l(t.options,(e)=>a("el-option",{key:e.value,attrs:{label:e.name,value:e.value}}))) :
  a("el-input",{attrs:{size:"small",type:"textarea",rows:t.rows,placeholder:t.placeholder,...}})
~~~

| key | 作用 | 证据 |
|---|---|---|
| type | 控件类型，识别 text / password / yesno / radio / dropdown，**其它值一律渲染成 textarea**（用 rows） | 上面 JS |
| name | 表单项 label（label:t.name） | 同上；public/admin/lang/zh.js: server_module: '服务器模块' |
| description | 控件右侧说明；type=yesno 时作为 checkbox 的文本 | 同上 |
| placeholder | 输入框占位符（proxmoxve 用得最多） | 同上 |
| options | radio / dropdown 的候选；**后台期望 [{name,value}] 形态** | 同上里的 e.name / e.value |
| default | 默认值，双向绑定到 t.default（保存时写进 config_optionN） | 同上 + packageconfigoption 逻辑 |
| rows | textarea 行数（**没有模块用过**） | 同上 |
| key | **不在 UI 出现**；决定 $params['configoptions'][key] 的名字 | 对比 bthosts_ConfigOptions 的 key 与 bthosts.php:172-193 的读取名；nokvm 中 key=>'' 的那一项在代码里确实取不到（改读第 10 项 os） |

> **options 形态冲突（重要）**：模块侧写的是**扁平数组**（['自定义配置','套餐开通','弹性开通']），后台读的是 {name,value}。把扁平数组转成 {name,value} 的代码在加密核心/接口层，**未找到明文证据**。bthosts 的取值方式是数字下标（type==0/1/2），与"扁平数组 → value=下标"一致。骨架里按 bthosts 的写法（数字比较 + ?? 兜底）最稳。

### 6.3 运行时映射：config_optionN ↔ modulesList[N-1] ↔ $params['configoptions'][key]

后台读模块配置项：GET provision/<server_group_id> → {status:200, data:[...即 _ConfigOptions() 的结果...], module_meta:{APIVersion,HelpDoc}}。
证据：public/admin/js/EditProduct~ca2dc83e.74497fec.js
~~~js
getModule:function(e,t){ ... Object(o["u"])(e); ... r.modulesList=n.data, t||r.moduleData(r.product,r.modulesList),
  r.moduleMeta={APIVersion:n.module_meta?n.module_meta.APIVersion:"",HelpDoc:n.module_meta?n.module_meta.HelpDoc:""};
~~~
其中 o = chunk f421（同一份 admin 产物里唯一存在：public/admin/js/AddProductGroup~f71cff67.47e958b3.js 偏移 8513 起），该 chunk 内
~~~js
a.d(t,"u",(function(){return g}));  ...  function g(e){return Object(a["a"])({url:"provision/".concat(e)})}
a.d(t,"y",(function(){return $}));  ...  function $(e){return Object(a["a"])({url:"product/get_upstream_price",params:e})}
~~~
（旁证：同一组件里 getUpstreamPrice() 调 Object(o["y"])({pid:...})，正是 product/get_upstream_price，说明 f421 就是该组件依赖的 API 模块。）
调用点：e.editProductFormData.server_group&&e.getModule(e.editProductFormData.server_group) 与 groupChange(e){ ... this.getModule(e,"handle") ... } —— **入参是产品选择的"接口组"id（products.server_group）**。

位置映射（EditProduct~ca2dc83e.74497fec.js）：
~~~js
moduleData:function(e,t){ ... var c=function(c){ if(c.startsWith("config_option")&&!c.endsWith("_upgrade")&&e[c]){ var a=c.replace("config_option","");
    (t||[]).forEach((function(t,n){ n===a-1&&(t.default=e[c], r.$forceUpdate()) })) } }; for(var a in e)c(a) }
...
this.editProductFormData.packageconfigoption=[];
(this.modulesList||[]).forEach((function(t,r){ e.editProductFormData.packageconfigoption[r+1]=t.default }));
~~~
即：**产品列 config_option1..24 按序号与 _ConfigOptions() 的数组下标一一对应**；运行时核心把 config_optionN 与 _ConfigOptions()[N-1]['key'] 组合成 $params['configoptions'][key]（此结论由"模块读的 key 名恰好等于自己声明的 key"这一 5 模块一致性支撑；组装代码本身在加密核心，**未找到明文**）。另有 config_optionN_upgrade（由 !c.endsWith("_upgrade") 的排除写法推断它存在）。

### 6.4 产品/服务器管理界面里的绑定字段（后台 JS + 建表证据）

| 层 | 字段 | 说明 | 证据 |
|---|---|---|---|
| 产品 | products.server_type varchar(100) | 服务器模块类型（模块标识字符串） | 建表注释 "服务器模块类型" |
| 产品 | products.server_group int(11) | 服务器组ID | 建表注释 + JS editProductFormData.server_group |
| 产品 | products.api_type | 开通方式：''/normal=本地模块，另有 zjmf_api / manual(供应商) / whmcs | nokvm.php:1537 ->whereIn('b.api_type', ['','normal'])；EditProduct 仅 api_type==='normal' 才渲染模块配置项 |
| 产品 | products.config_option1..24 varchar(500) | 模块配置项的值（按序号） | 建表原文第 21-44 行 |
| 产品 | products.auto_setup | 自动开通：空=手动，payment / order | 建表注释；JS auto_setupOptions |
| 接口组 | server_groups.type varchar(255) | **服务器模块类型 = 模块标识** | 建表注释 + nokvm.php:1539 ->where('d.type','nokvm')（d=server_groups） |
| 接口组 | server_groups.system_type enum('normal','dcim') | 组类型，普通模块必须 normal | 建表 + nokvm.php:1538 |
| 接口组 | server_groups.capacity / mode | 最大接口容量 / 分配方式 | public/upgrade/2.6.3.sql:10-11 |
| 接口 | servers.gid | 接口所属组 | 建表 gid int(11) COMMENT '服务器组ID' |
| 接口 | servers.ip_address / hostname / username / password / accesshash / secure / port / max_accounts / disabled / noc / assigned_ips / status_address | 后台"接口"表单字段，**与 $params 键一一对应** | 建表 + AddInterface~31ecd969.0ab457c5.js 的 formData |
| 接口 | servers.server_type enum('normal','dcim') | 注意：**与产品的 server_type 同名不同义** | 建表原文 |
| 关联 | server_groups_rel(group_id, server_id) | 组 ↔ 接口 多对多 | 建表原文 |

后台接口层证据（AddInterface~31ecd969.0ab457c5.js）：
~~~js
formData:{name:"",ip_address:"",type:"",hostname:"",gid:"",username:"",password:"",port:"",secure:"",disabled:"",accesshash:"",max_accounts:""}
changeType:function(e,t){ ... Object(o["l"])({modules:e}).then(e=>{ a.gidOptions=e.data.data.groups; ...}) }   // POST get_modules_group {modules: 模块标识}
addServerInit:function(){ Object(o["c"])().then(t=>{ e.interfaceType=t.data.data.modules }) }                 // GET  servers_add → 模块下拉 [{name,value}]
getData: ... e.formData.type=t.data.server.type, ... e.changeType(t.data.server.type, t.data.server.gid)     // GET  edit_servers/<id>
submit: Object(o["d"])(e.formData)  // POST servers_add_post（新增）/ Object(o["h"])(...) // POST edit_servers_post（编辑）
~~~
AddGroup~31ecd969.3fb4467e.js：组里每个接口带 type，右侧勾选时强制"同组接口必须同 type"（rightChange / leftCheckChange 里 e.type!==r.type&&(e.disabled=!0)）。

列表/管理接口名（AddServer~31ecd969.7b47ce2d.js 的 api 模块 bdf0）：servers_list、groups_list、create_groups、create_groups_post、edit_server_groups、edit_server_groups_post、delete_servers/<id>、delete_server_groups/<id>、server_test_link/<id>、get_modules_group。

---

## 7. 最小可用骨架

放置：public/plugins/servers/shitidc/{shitidc.php, version.txt, templates/information.html}，
模块标识 shitidc（= 目录名 = server_groups.type）。
骨架文件已同时落盘：zjmf-servers-shitidc/shitidc.php、zjmf-servers-shitidc/version.txt、zjmf-servers-shitidc/templates/information.html。

~~~php
<?php

/* ============================================================
 * ShitIDC server module  —— 魔方财务 ZJMF 3.7.x 经典服务器模块
 * 安装路径: public/plugins/servers/shitidc/shitidc.php
 * 模块标识: shitidc  （= 目录名 = server_groups.type）
 * 上游: ShitIDC (Go)
 * ============================================================ */

/* 授权占位（bthosts/proxmoxve/wlkanglepro/bthostx 均为空实现） */
function shitidc_idcsmartauthorizes()
{
}

/* 元数据：后台"服务器模块"下拉、"帮助文档"链接 */
function shitidc_MetaData()
{
    return [
        'DisplayName' => 'ShitIDC',
        'APIVersion'  => '1.0.0',
        'HelpDoc'     => 'https://your-shitidc.example.com/docs/zjmf',
        'version'     => '1.0.0',
    ];
}

/* 模块配置项：key 决定 $params['configoptions'][key] 的名字
   产品列 config_option1..24 按数组下标一一对应 */
function shitidc_ConfigOptions()
{
    return [
        ['type' => 'text',     'name' => 'ShitIDC 套餐ID', 'description' => '上游套餐/产品编号', 'default' => '1',  'placeholder' => '1',  'key' => 'plan_id'],
        ['type' => 'text',     'name' => '区域ID',         'description' => '留空由上游自动分配', 'placeholder' => '1',  'key' => 'region_id'],
        ['type' => 'dropdown', 'name' => '开通方式',       'description' => '', 'options' => ['自定义配置', '套餐开通'], 'default' => '0', 'key' => 'mode'],
        ['type' => 'text',     'name' => '月流量(GB)',     'description' => '0 为不限制', 'default' => '0', 'key' => 'flow_limit'],
        ['type' => 'yesno',    'name' => 'NAT 产品',       'description' => '勾选表示 NAT 产品', 'default' => '0', 'key' => 'nat'],
    ];
}

/* ---------------- 内部工具 ---------------- */

/* 主机侧标识：与 bthosts 同构，读写产品自定义字段 host_id */
function shitidc_GetServerid($params)
{
    return (int) ($params['customfields']['host_id'] ?? 0);
}

/* 签名：time + random + accesshash 字典序拼接后 md5 大写（bthosts.php:14-23 原文算法） */
function shitidc_CreateSign($time, $random, $token)
{
    $data = ['time' => $time, 'random' => $random, 'token' => $token];
    sort($data, SORT_STRING);
    return strtoupper(md5(implode($data)));
}

function shitidc_GetUrl($params, $path = '/api/v1/index', $query = [])
{
    $url  = $params['secure'] ? 'https://' : 'http://';
    $url .= $params['server_ip'] ?: $params['server_host'];
    if (!empty($params['port'])) {
        $url .= ':' . $params['port'];
    }
    $url .= $path;
    $q = '';
    foreach ($query as $k => $v) {
        $q .= '&' . $k . '=' . urlencode($v);
    }
    if (!empty($q)) {
        $url .= '?' . ltrim($q, '&');
    }
    return $url;
}

/* 统一请求：POST 表单；返回 ['ok'=>bool,'data'=>array,'msg'=>string] */
function shitidc_Request($params, $path, $post = [], $method = 'POST', $query = [])
{
    $data = ['time' => time(), 'random' => mt_rand(), 'signature' => ''];
    $data['signature'] = shitidc_CreateSign($data['time'], $data['random'], $params['accesshash']);
    $body = array_merge($data, $post);

    $url = shitidc_GetUrl($params, $path, $query);
    $ch  = curl_init();
    curl_setopt($ch, CURLOPT_URL, $url);
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, 1);
    curl_setopt($ch, CURLOPT_HEADER, 0);
    curl_setopt($ch, CURLOPT_TIMEOUT, 30);
    curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, false);
    curl_setopt($ch, CURLOPT_SSL_VERIFYHOST, false);
    curl_setopt($ch, CURLOPT_USERAGENT, 'Mozilla/5.0 (compatible; ZJMF-ShitIDC)');
    if (strtoupper($method) === 'POST') {
        curl_setopt($ch, CURLOPT_POST, 1);
        curl_setopt($ch, CURLOPT_POSTFIELDS, http_build_query($body));
    } else {
        curl_setopt($ch, CURLOPT_CUSTOMREQUEST, strtoupper($method));
        curl_setopt($ch, CURLOPT_POSTFIELDS, http_build_query($body));
    }
    $raw   = curl_exec($ch);
    $error = curl_error($ch);
    curl_close($ch);
    if (!empty($error)) {
        return ['ok' => false, 'msg' => 'CURL ERROR:' . $error, 'data' => []];
    }
    $res = json_decode($raw, true);
    if (!is_array($res)) {
        return ['ok' => false, 'msg' => '上游返回非 JSON: ' . mb_substr((string) $raw, 0, 200), 'data' => []];
    }
    /* 与上游约定 code=0 成功；兼容 code=1 与 status=200 */
    $code = $res['code'] ?? ($res['status'] ?? null);
    $ok   = in_array($code, [0, 1, 200, '0', '1', '200'], true);
    return ['ok' => $ok, 'msg' => $res['msg'] ?? ($res['message'] ?? ''), 'data' => $res['data'] ?? []];
}

/* ---------------- 必须实现 ---------------- */

/* 连接测试：后台"接口"列表状态列直接读本返回值（必须 status=200 + data.server_status） */
function shitidc_TestLink($params)
{
    $res = shitidc_Request($params, '/api/v1/ping', [], 'POST');
    if (!empty($res['ok'])) {
        return ['status' => 200, 'data' => ['server_status' => 1]];
    }
    return ['status' => 200, 'data' => ['server_status' => 0, 'msg' => $res['msg'] ?: '连接失败']];
}

/* 开通 */
function shitidc_CreateAccount($params)
{
    $hostid = shitidc_GetServerid($params);
    if (!empty($hostid)) {
        return '已开通,不能重复开通';
    }
    $sys_pwd = empty($params['password']) ? randStr(8) : $params['password'];

    $post = [
        'username'   => $params['domain'],
        'password'   => $sys_pwd,
        'domain'     => $params['domain'],
        'plan_id'    => $params['configoptions']['plan_id'] ?? '',
        'region_id'  => $params['configoptions']['region_id'] ?? '',
        'mode'       => $params['configoptions']['mode'] ?? 0,
        'flow_limit' => (int) ($params['configoptions']['flow_limit'] ?? 0),
        'nat'        => (int) ($params['configoptions']['nat'] ?? 0),
        'client_id'  => $params['uid'],
        'email'      => $params['user_info']['email'] ?? '',
        'phone'      => $params['user_info']['phonenumber'] ?? '',
        'expire_at'  => date('Y-m-d H:i:s', (int) $params['nextduedate']),
    ];
    $res = shitidc_Request($params, '/api/v1/instances', $post, 'POST');
    if (empty($res['ok'])) {
        return ['status' => 'error', 'msg' => $res['msg'] ?: '开通失败'];
    }
    $instanceId = $res['data']['id'] ?? 0;
    $username   = $res['data']['username'] ?? $params['domain'];
    $sysPwdUp   = $res['data']['password'] ?? $sys_pwd;
    $mainIp     = $res['data']['ip'] ?? $params['server_ip'];

    /* 写产品自定义字段 host_id（nokvm.php:909-940 / bthosts.php:223-234 同构） */
    $customid = think\Db::name('customfields')
        ->where('type', 'product')->where('relid', $params['productid'])
        ->where('fieldname', 'host_id')->value('id');
    if (empty($customid)) {
        $customid = think\Db::name('customfields')->insertGetId([
            'type' => 'product', 'relid' => $params['productid'], 'fieldname' => 'host_id',
            'fieldtype' => 'text', 'adminonly' => 1, 'create_time' => time(),
        ]);
    }
    $exist = think\Db::name('customfieldsvalues')
        ->where('fieldid', $customid)->where('relid', $params['hostid'])->find();
    if (empty($exist)) {
        think\Db::name('customfieldsvalues')->insert([
            'fieldid' => $customid, 'relid' => $params['hostid'],
            'value' => $instanceId, 'create_time' => time(),
        ]);
    } else {
        think\Db::name('customfieldsvalues')->where('id', $exist['id'])->update(['value' => $instanceId]);
    }

    /* 写回主机（domainstatus 只能取 enum 内的值） */
    $update = [
        'domainstatus' => 'Active',
        'domain'       => $params['domain'],
        'username'     => $username,
        'password'     => cmf_encrypt($sysPwdUp),   // 经典模块统一 cmf_encrypt
        'dedicatedip'  => $mainIp,
        'assignedips'  => is_array($res['data']['ips'] ?? null) ? implode(',', $res['data']['ips']) : '',
        'bwlimit'      => (int) ($params['configoptions']['flow_limit'] ?? 0),
    ];
    think\Db::name('host')->where('id', $params['hostid'])->update($update);

    return 'success';   // bthosts.php:243 / wlkanglepro.php:67
}

/* 暂停 */
function shitidc_SuspendAccount($params)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return 'ShitIDC 主机ID错误';
    }
    $res = shitidc_Request($params, '/api/v1/instances/' . $hostid . '/suspend');
    if (!empty($res['ok'])) {
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg'] ?: '暂停失败'];
}

/* 解除暂停 */
function shitidc_UnsuspendAccount($params)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return 'ShitIDC 主机ID错误';
    }
    $res = shitidc_Request($params, '/api/v1/instances/' . $hostid . '/unsuspend');
    if (!empty($res['ok'])) {
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg'] ?: '解除暂停失败'];
}

/* 删除 */
function shitidc_TerminateAccount($params)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return 'ShitIDC 主机ID错误';
    }
    $res = shitidc_Request($params, '/api/v1/instances/' . $hostid, [], 'DELETE');
    if (!empty($res['ok'])) {
        /* 清理自定义字段值（nokvm.php:1029-1034） */
        $customid = think\Db::name('customfields')
            ->where('type', 'product')->where('relid', $params['productid'])
            ->where('fieldname', 'host_id')->value('id');
        think\Db::name('customfieldsvalues')
            ->where('fieldid', $customid)->where('relid', $params['hostid'])->delete();
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg'] ?: '删除失败'];
}

/* 续费：同步上游到期时间（bthosts.php:527-549） */
function shitidc_Renew($params)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return 'ShitIDC 主机ID错误';
    }
    $res = shitidc_Request($params, '/api/v1/instances/' . $hostid . '/renew', [
        'endtime' => date('Y-m-d', (int) $params['nextduedate']),
    ]);
    if (!empty($res['ok'])) {
        return ['status' => 'success', 'msg' => '续费成功'];
    }
    return ['status' => 'error', 'msg' => $res['msg'] ?: '续费失败'];
}

/* ---------------- 建议实现 ---------------- */

/* 同步：把上游实际状态写回 host 表 */
function shitidc_Sync($params)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return 'ShitIDC 主机ID错误';
    }
    $res = shitidc_Request($params, '/api/v1/instances/' . $hostid . '/info', [], 'POST');
    if (empty($res['ok'])) {
        return ['status' => 'error', 'msg' => $res['msg'] ?: '同步失败'];
    }
    $d = $res['data'];
    $update = [
        'domain'       => $d['domain'] ?? $params['domain'],
        'username'     => $d['username'] ?? $params['username'],
        'dedicatedip'  => $d['ip'] ?? $params['server_ip'],
        'domainstatus' => 'Active',
    ];
    if (!empty($d['password'])) {
        $update['password'] = cmf_encrypt($d['password']);
    }
    if (isset($d['flow_used_gb'])) {
        $update['bwusage'] = round((float) $d['flow_used_gb'], 2);
    }
    if (isset($d['flow_limit_gb'])) {
        $update['bwlimit'] = (int) $d['flow_limit_gb'];
    }
    think\Db::name('host')->where('id', $params['hostid'])->update($update);
    return ['status' => 'success', 'msg' => '同步成功'];
}

/* 状态：data.status ∈ on/off/process/suspend/unknown */
function shitidc_Status($params)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return 'ShitIDC 主机ID错误';
    }
    $res = shitidc_Request($params, '/api/v1/instances/' . $hostid . '/status', [], 'POST');
    if (empty($res['ok'])) {
        return ['status' => 'error', 'msg' => $res['msg'] ?: '获取失败'];
    }
    $map = [
        'running'   => ['on', '运行中'],
        'stopped'   => ['off', '已关机'],
        'suspended' => ['suspend', '已暂停'],
        'pending'   => ['process', '创建中'],
    ];
    $s = $res['data']['status'] ?? 'unknown';
    $result['status'] = 'success';
    if (isset($map[$s])) {
        $result['data']['status'] = $map[$s][0];
        $result['data']['des']    = $map[$s][1];
    } else {
        $result['data']['status'] = 'unknown';
        $result['data']['des']    = '未知';
    }
    return $result;
}

/* 改密（核心传第二参数为新明文密码） */
function shitidc_CrackPassword($params, $new_pass)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return 'ShitIDC 主机ID错误';
    }
    $res = shitidc_Request($params, '/api/v1/instances/' . $hostid . '/password', ['password' => $new_pass]);
    if (!empty($res['ok'])) {
        return ['status' => 'success', 'msg' => '密码重置成功'];
    }
    return ['status' => 'error', 'msg' => $res['msg'] ?: '密码重置失败'];
}

/* 升降级：只有变化的项才推给上游 */
function shitidc_ChangePackage($params)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return 'ShitIDC 主机ID错误';
    }
    $post = [];
    if (isset($params['configoptions_upgrade']['plan_id'])) {
        $post['plan_id'] = $params['configoptions']['plan_id'] ?? '';
    }
    if (isset($params['configoptions_upgrade']['flow_limit'])) {
        $post['flow_limit'] = (int) ($params['configoptions']['flow_limit'] ?? 0);
    }
    $result = ['status' => 'error', 'msg' => '修改配置失败'];
    if (!empty($post)) {
        $res = shitidc_Request($params, '/api/v1/instances/' . $hostid, $post, 'PUT');
        if (!empty($res['ok'])) {
            $result = ['status' => 'success', 'msg' => $res['msg'] ?: '修改配置成功'];
        } else {
            $result['msg'] = $res['msg'] ?: '修改配置失败';
        }
    }
    shitidc_Sync($params);
    return $result;
}

/* ---------------- 会员中心 ---------------- */

function shitidc_ClientArea($params)
{
    return ['index' => ['name' => '主机信息']];
}

function shitidc_ClientAreaOutput($params, $key)
{
    $hostid = shitidc_GetServerid($params);
    if (empty($hostid)) {
        return '';
    }
    $res  = shitidc_Request($params, '/api/v1/instances/' . $hostid . '/info', [], 'POST');
    $info = !empty($res['ok']) ? $res['data'] : [];
    if ($key === 'index') {
        return [
            'template' => 'templates/information.html',
            'vars'     => ['params' => $params, 'info' => $info],
        ];
    }
}

/* ---------------- 后台/前台按钮与自定义方法 ---------------- */

function shitidc_AllowFunction()
{
    return ['client' => ['Sync'], 'admin' => []];
}

function shitidc_AdminButton($params)
{
    return ['Sync' => '同步信息', 'UnsuspendAccount' => '解除暂停', 'SuspendAccount' => '暂停'];
}

function shitidc_ClientButton($params)
{
    return ['Sync' => ['place' => 'console', 'name' => '同步信息']];
}
~~~

配套 version.txt（裸版本号，**无换行**，内容与 _MetaData()['APIVersion'] 一致）：
~~~
1.0.0
~~~

配套 templates/information.html（ThinkPHP 语法，变量来自 vars）：
~~~html
<table class="table-main">
  <tr><th>主机编号</th><td>{$params.customfields.host_id}</td></tr>
  <tr><th>状态</th><td>{$info.status}</td></tr>
  <tr><th>IP</th><td>{$params.server_ip}</td></tr>
  <tr><th>账号</th><td>{$params.username}</td></tr>
  <tr><th>密码</th><td>{$params.password}</td></tr>
  <tr><th>月流量</th><td>{if $info.flow_limit_gb == 0}不限制{else/}{$info.flow_limit_gb} GB{/if}</td></tr>
</table>
~~~

**上线清单（按契约逐条自检）**
1. 目录名 / 主文件名 / 所有函数前缀 = shitidc；
2. 后台"接口组"新建时选"服务器模块 = ShitIDC"（写 server_groups.type='shitidc'、system_type='normal'）；
3. 后台"接口"新建：填 IP/主机名/端口/用户名/密码/accesshash/SSL，选上一步的组（servers.gid）；点状态列刷新应显示"连接成功"（走 _TestLink）；
4. 后台"产品"编辑 → 自动开通页签：选该接口组（products.server_group），页面会 GET provision/<组id> 拉出模块配置项并渲染成表单（products.config_option1..N）；api_type 保持"本地接口/normal"；auto_setup 按需；
5. 下单/开通后核对：host.domainstatus='Active'、host.password 是 cmf_encrypt 后的值、customfieldsvalues 里有 host_id；
6. 暂停/恢复/删除/续费各跑一次，确认返回 'success' 或 ['status'=>'success',...]；
7. 会员中心产品详情页应出现"主机信息"分页（_ClientArea / _ClientAreaOutput）。

---

## 8. 未找到明文证据的清单（不要臆测）

1. 核心如何调用模块函数、如何判定成功/失败、字符串返回值（'success' / 'ok'）与数组返回值（['status'=>...]）的解析差异 —— app/** 全部 ionCube。
2. 模块目录的扫描/缓存/启用机制、version.txt 的读取方、插件安装/升级流程。
3. _Renew 缺失时核心的兜底行为（只能证明它不是加载必需）。
4. _ConfigOptions()['options'] 从"扁平数组"到后台 {name,value} 的规范化代码。
5. $params['configoptions'] / configoptions_upgrade 的组装代码（只能由后台 JS 的位置映射 + 5 个模块的 key 一致性旁证）。
6. {$MODULE_CUSTOM_API} 的赋值规则、以及自定义 client 方法（_AllowFunction）的 HTTP 路由形态。
7. randStr() / rand_str() / active_logs() / aesPasswordDecode() / password_encrypt() 的定义（都在加密核心；cmf_encrypt() 例外，有明文）。
8. idcsmart_common（V10）的表结构、IdcsmartCommonServerHostLinkModel 的表名与全部列、安装方式。
9. shd_host 之外的 V10 字段（due_time、host.status）来自哪个升级脚本。
10. 经典主机详情页里渲染模块 _ClientArea 分页的前端代码（public/themes/** 大量为编译/远程资源，本地明文文件中未找到）。
