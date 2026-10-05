package zjmfimport

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 固定样例都是按 docs/zjmf-server-module-contract.md 记录的模块约定写的
// 原创最小样例，不复制魔方源码。

const sampleBthostsLike = `<?php
function bt_MetaData()
{
    return ['DisplayName' => 'BT对接', 'APIVersion' => '1.7.1', 'HelpDoc' => 'https://example.com/help'];
}

function bt_ConfigOptions()
{
    return [['type' => 'dropdown', 'name' => '开通方式', 'description' => '', 'options' => ['自定义配置', '套餐开通'], 'key' => 'type'],
        ['type' => 'text', 'name' => '套餐ID', 'description' => '留空', 'key' => 'plans_id'],
        ['type' => 'yesno', 'name' => 'NAT产品', 'description' => '勾选表示NAT', 'default' => '0', 'key' => 'nat']];
}

function bt_CreateSign($time, $random, $token)
{
    $data['time'] = $time;
    $data['random'] = $random;
    $data['token'] = $token;
    sort($data, SORT_STRING);
    $str = implode($data);
    $signature = md5($str);
    return strtoupper($signature);
}

function bt_TestLink($params)
{
    $data['time'] = time();
    $data['random'] = mt_rand();
    $data['token'] = $params['accesshash'];
    $datas = $data;
    unset($datas['token']);
    $datas['signature'] = bt_createsign($data['time'], $data['random'], $data['token']);
    $url = bt_geturl($params, '/api/vhost/index', $datas);
    $res = json_decode(bt_get($url), true);
    if ($res['code'] == 1) {
        return ['status' => 200, 'data' => ['server_status' => 1]];
    }
    return ['status' => 200, 'data' => ['server_status' => 0, 'msg' => $res['msg']]];
}

function bt_CreateAccount($params)
{
    $hostid = bt_gethostid($params);
    if (!empty($hostid)) {
        return '已开通,不能重复开通';
    }
    $sys_pwd = empty($params['password']) ? randStr(8) : $params['password'];
    $datas['username'] = $params['domain'];
    $datas['password'] = $sys_pwd;
    $datas['pack[flow_max]'] = $params['configoptions']['flow_max'];
    $datas['plans_id'] = $params['configoptions']['plans_id'];
    $datas['client_id'] = $params['uid'];
    $datas['email'] = $params['user_info']['email'];
    $datas['endtime'] = date('Y-m-d', $params['nextduedate']);
    $datas['time'] = time();
    $datas['random'] = mt_rand();
    $datas['signature'] = bt_createsign($datas['time'], $datas['random'], $params['accesshash']);
    $datas['id'] = $hostid;
    $url = bt_geturl($params, '/api/vhost/user_create');
    $res = json_decode(bt_post($url, $datas), true);
    if ($res['code'] == 1) {
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg']];
}

function bt_SuspendAccount($params)
{
    $hostid = bt_gethostid($params);
    $datas['id'] = $hostid;
    $res = json_decode(bt_post(bt_geturl($params, '/api/vhost/host_locked'), $datas), true);
    if ($res['code'] == 1) {
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg']];
}

function bt_TerminateAccount($params)
{
    $hostid = bt_gethostid($params);
    $res = json_decode(bt_post(bt_geturl($params, '/api/vhost/host_recycle'), ['id' => $hostid]), true);
    if ($res['code'] == 1) {
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg']];
}

function bt_Renew($params)
{
    $datas['endtime'] = date('Y-m-d', $params['nextduedate']);
    $url = bt_geturl($params, '/api/vhost/host_endtime');
    $res = json_decode(bt_post($url, $datas), true);
    if ($res['code'] == 1) {
        return ['status' => 'success', 'msg' => '续费成功'];
    }
    return ['status' => 'success', 'msg' => '续费失败：' . $res['msg']];
}

function bt_Status($params)
{
    $res = json_decode(bt_post(bt_geturl($params, '/api/vhost/host_status'), ['id' => bt_gethostid($params)]), true);
    if ($res['code'] == 1) {
        if ($res['data']['loca'] == 'normal') {
            return ['status' => 'success', 'data' => ['status' => 'on', 'des' => '运行中']];
        }
        return ['status' => 'success', 'data' => ['status' => 'off', 'des' => '暂停']];
    }
    return ['status' => 'error', 'msg' => $res['msg']];
}

function bt_GetServerid($params)
{
    return (int) $params['customfields']['host_id'];
}
`

func TestParseBthostsLike(t *testing.T) {
	res, err := FromFiles("bt/bt.php", map[string][]byte{"bt/bt.php": []byte(sampleBthostsLike)})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := res.Spec
	if s.Slug != "bt" {
		t.Errorf("slug = %q", s.Slug)
	}
	if s.DisplayName != "BT对接" || s.SourceVersion != "1.7.1" {
		t.Errorf("meta = %q/%q", s.DisplayName, s.SourceVersion)
	}
	if s.Auth.Scheme != SchemeMD5SortUpper || s.Auth.Placement != PlacementForm || s.Auth.TokenSource != TokenAccessHash {
		t.Errorf("auth = %+v", s.Auth)
	}
	if s.Success.Field != "code" || s.Success.Equals != "1" {
		t.Errorf("success = %+v", s.Success)
	}
	if len(s.ConfigOptions) != 3 {
		t.Fatalf("config options = %d", len(s.ConfigOptions))
	}
	if s.ConfigOptions[0].Type != "dropdown" || len(s.ConfigOptions[0].Options) != 2 ||
		s.ConfigOptions[0].Options[1].Name != "套餐开通" || s.ConfigOptions[0].Options[1].Value != "1" {
		t.Errorf("dropdown options = %+v", s.ConfigOptions[0].Options)
	}
	if s.ConfigOptions[2].Type != "yesno" || s.ConfigOptions[2].Default != "0" {
		t.Errorf("yesno = %+v", s.ConfigOptions[2])
	}

	create := s.Actions[ActionCreate]
	if create.Path != "/api/vhost/user_create" || create.Method != "POST" {
		t.Errorf("create = %+v", create)
	}
	// 配置项与身份模板
	if create.Body["pack[flow_max]"] != "{{opt_flow_max}}" || create.Body["plans_id"] != "{{opt_plans_id}}" {
		t.Errorf("create body options = %+v", create.Body)
	}
	if create.Body["username"] != "{{domain}}" || create.Body["client_id"] != "{{uid}}" || create.Body["email"] != "{{email}}" {
		t.Errorf("create body identity = %+v", create.Body)
	}
	// 常量赋值里引用 $sys_pwd 变量（不是 $params），不应编造映射
	if v, ok := create.Body["password"]; ok && v != "{{password}}" {
		t.Errorf("create password = %q", v)
	}
	if create.InstanceIDPath != "" {
		// 样例里 create 成功分支只 return 'success'，没有读响应 id —— 不应编造
		t.Errorf("instance id path should be empty, got %q", create.InstanceIDPath)
	}
	if s.Actions[ActionTest].Method != "GET" || s.Actions[ActionTest].Path != "/api/vhost/index" {
		t.Errorf("test = %+v", s.Actions[ActionTest])
	}
	if s.Actions[ActionRenew].Path != "/api/vhost/host_endtime" || s.Actions[ActionRenew].Body["endtime"] != "{{expire_date}}" {
		t.Errorf("renew = %+v", s.Actions[ActionRenew])
	}
	if s.Actions[ActionStatus].StatusField != "data.loca" {
		t.Errorf("status field = %q", s.Actions[ActionStatus].StatusField)
	}
	if s.Actions[ActionSuspend].Path != "/api/vhost/host_locked" || s.Actions[ActionTerminate].Path != "/api/vhost/host_recycle" {
		t.Errorf("suspend/terminate paths")
	}
}

const sampleNokvmLike = `<?php
function nk_MetaData(){
	return ['DisplayName'=>'NOKVM', 'APIVersion'=>'1.1', 'HelpDoc'=>'https://example.com', 'version'=>'1.0.0'];
}
function nk_ConfigOptions(){
	return [
		['type'=>'text', 'name'=>'节点ID', 'key'=>'nodes_id'],
		['type'=>'yesno', 'name'=>'NAT产品', 'description'=>'勾选则表示为NAT产品', 'default'=>'1', 'key'=>'nat'],
	];
}
function nk_CreateSign($token = ''){
	$data['timeStamp'] = time();
	$data['randomStr'] = randStr(6);
	$data['token'] = $token;
	$res['time'] = $data['timeStamp'];
	$res['random'] = $data['randomStr'];
	sort($data, SORT_STRING);
	$str = implode($data);
	$signature = strtoupper(md5($str));
	$res['signature'] = $signature;
	return $res;
}
function nk_TestLink($params){
	$sign = nk_CreateSign($params['server_password']);
	$url = nk_GetUrl($params, '/api/virtual/ping', $sign);
	$res = json_decode(nk_curl($url, [], 30, 'GET'), true);
	if(isset($res['code']) && $res['code'] == 0){
		return ['status'=>200, 'data'=>['server_status'=>1]];
	}
	return ['status'=>200, 'data'=>['server_status'=>0, 'msg'=>$res['message'] ?: '连接失败']];
}
function nk_CreateAccount($params){
	$sign = nk_CreateSign($params['server_password']);
	$post_data['nodes_id'] = $params['configoptions']['nodes_id'];
	$post_data['memory'] = $params['configoptions']['Memory'];
	$post_data['username'] = $params['user_info']['email'];
	$post_data['users_id'] = $params['uid'];
	$url = nk_GetUrl($params, '/api/virtual', $sign);
	$res = json_decode(nk_curl($url, $post_data, 30, 'POST'), true);
	if(isset($res['code']) && $res['code'] == 0){
		return 'ok';
	}
	return ['status'=>'error', 'msg'=>$res['message'] ?: '开通失败'];
}
function nk_SuspendAccount($params){
	$res = json_decode(nk_curl(nk_GetUrl($params, '/api/virtual/stop'), [], 30, 'POST'), true);
	if(isset($res['code']) && $res['code'] == 0){
		return ['status'=>'success', 'msg'=>$res['message']];
	}
	return ['status'=>'error', 'msg'=>$res['message'] ?: '暂停失败'];
}
function nk_TerminateAccount($params){
	$res = json_decode(nk_curl(nk_GetUrl($params, '/api/virtual/del'), [], 30, 'POST'), true);
	if(isset($res['code']) && $res['code'] == 0){
		return ['status'=>'success', 'msg'=>$res['message']];
	}
	return ['status'=>'error', 'msg'=>$res['message'] ?: '删除失败'];
}
function nk_GetServerid($params){
	return (int)$params['customfields']['vserverid'];
}
`

func TestParseNokvmLike(t *testing.T) {
	res, err := FromFiles("", map[string][]byte{"nk/nk.php": []byte(sampleNokvmLike)})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := res.Spec
	if s.Slug != "nk" {
		t.Fatalf("slug = %q", s.Slug)
	}
	if s.Auth.Scheme != SchemeMD5SortUpper || s.Auth.TimeParam != "timeStamp" ||
		s.Auth.RandomParam != "randomStr" || s.Auth.Placement != PlacementQuery {
		t.Errorf("auth = %+v", s.Auth)
	}
	if s.Auth.TokenSource != TokenServerPasswd {
		t.Errorf("token source = %q", s.Auth.TokenSource)
	}
	if s.Success.Field != "code" || s.Success.Equals != "0" || s.Success.MessageField != "message" {
		t.Errorf("success = %+v", s.Success)
	}
	if s.Actions[ActionCreate].Path != "/api/virtual" || s.Actions[ActionCreate].Method != "POST" {
		t.Errorf("create = %+v", s.Actions[ActionCreate])
	}
	if s.Actions[ActionCreate].Body["memory"] != "{{opt_Memory}}" || s.Actions[ActionCreate].Body["users_id"] != "{{uid}}" {
		t.Errorf("create body = %+v", s.Actions[ActionCreate].Body)
	}
	if s.Actions[ActionTest].Method != "GET" {
		t.Errorf("test method = %q", s.Actions[ActionTest].Method)
	}
	if v, ok := s.Actions[ActionCreate].Body["nodes_id"]; !ok || v != "{{opt_nodes_id}}" {
		t.Errorf("nodes_id = %q %v", v, ok)
	}
}

const sampleKangleLike = `<?php
function ka_MetaData()
{
    return ['DisplayName' => 'kangle', 'APIVersion' => '1.0.3'];
}
function ka_ConfigOptions()
{
    return [['type' => 'dropdown', 'name' => '绑定子目录', 'options' => ['1' => '1_允许', '0' => '0_不允许'], 'key' => 'subdir_flag']];
}
function ka_CreateSign($a, $skey, $r)
{
    return md5($a . $skey . $r);
}
function ka_TestLink($params)
{
    $a = 'info';
    $r = rand(100000, 999999);
    $info = ['c' => 'whm', 'a' => $a];
    $skey = ka_createsign($a, $params['accesshash'], $r);
    $url = ka_geturl($params, $info, $skey, $r);
    $res = json_decode(file_get_contents($url), true);
    if ($res['result'] == 200) {
        return ['status' => 200, 'data' => ['server_status' => 1]];
    }
    return ['status' => 200, 'data' => ['server_status' => 0]];
}
function ka_CreateAccount($params)
{
    $a = 'add_vh';
    $r = rand(100000, 999999);
    $info = ['c' => 'whm', 'a' => $a, 'web_quota' => $params['configoptions']['web_quota']];
    $skey = ka_createsign($a, $params['accesshash'], $r);
    $url = ka_geturl($params, $info, $skey, $r);
    $res = json_decode(file_get_contents($url), true);
    if ($res['result'] == 200) {
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg'] ?: '未知错误'];
}
function ka_SuspendAccount($params)
{
    $a = 'update_vh';
    $r = rand(100000, 999999);
    $info = ['c' => 'whm', 'a' => $a];
    $skey = ka_createsign($a, $params['accesshash'], $r);
    $res = json_decode(file_get_contents(ka_geturl($params, $info, $skey, $r)), true);
    if ($res['result'] == 200) {
        return 'success';
    }
    return ['status' => 'error', 'msg' => $res['msg']];
}
`

func TestParseKangleLike(t *testing.T) {
	res, err := FromFiles("", map[string][]byte{"ka/ka.php": []byte(sampleKangleLike)})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := res.Spec
	if s.Auth.Scheme != SchemeMD5Concat || s.Auth.ActionParam != "a" || s.Auth.RandomParam != "r" || s.Auth.SigParam != "s" {
		t.Errorf("auth = %+v", s.Auth)
	}
	if s.Auth.ExtraQuery["json"] != "1" {
		t.Errorf("extra query = %+v", s.Auth.ExtraQuery)
	}
	if s.Success.Field != "result" || s.Success.Equals != "200" {
		t.Errorf("success = %+v", s.Success)
	}
	if s.Actions[ActionTest].Path != "info" {
		t.Errorf("test action = %+v", s.Actions[ActionTest])
	}
	if s.Actions[ActionCreate].Path != "add_vh" || s.Actions[ActionCreate].Method != "GET" {
		t.Errorf("create action = %+v", s.Actions[ActionCreate])
	}
	if s.Actions[ActionSuspend].Path != "update_vh" {
		t.Errorf("suspend action = %+v", s.Actions[ActionSuspend])
	}
	// $info 里的字面量 c=whm 进入 query；a 是动作名不进 query
	if s.Actions[ActionCreate].Query["c"] != "whm" {
		t.Errorf("create query = %+v", s.Actions[ActionCreate].Query)
	}
	if _, has := s.Actions[ActionCreate].Query["a"]; has {
		t.Errorf("action name should not be duplicated in query")
	}
	// 配置项字典形态：'1' => '1_允许'
	co := s.ConfigOptions[0]
	if len(co.Options) != 2 || co.Options[0].Value != "1" || co.Options[0].Name != "允许" {
		t.Errorf("dict options = %+v", co.Options)
	}
}

func TestParseProxmoxLikeIsUnsupported(t *testing.T) {
	src := `<?php
require "ProxmoxClient.pve";
function pve_MetaData(){ return ['DisplayName' => 'PVE', 'APIVersion' => '1.1']; }
function pve_ConfigOptions(){ return [['type'=>'text','name'=>'节点','key'=>'node']]; }
function pve_TestLink($params){ $c = new ProxmoxApi\ProxmoxClient("x", "u", "p", "pam"); return "ok"; }
`
	res, err := FromFiles("", map[string][]byte{"pve/pve.php": []byte(src)})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := res.Spec
	if s.Auth.Scheme != SchemeUnsupported {
		t.Errorf("scheme = %q", s.Auth.Scheme)
	}
	if len(s.Actions) != 0 {
		t.Errorf("actions = %v", s.Actions)
	}
	if len(s.ConfigOptions) != 1 {
		t.Errorf("config options = %d", len(s.ConfigOptions))
	}
}

func TestParseModuleWithCommentedCode(t *testing.T) {
	// 被注释掉的旧签名不应干扰识别
	src := `<?php
function cm_MetaData(){ return ['DisplayName' => 'CM', 'APIVersion' => '1.0']; }
// function cm_CreateSign($a, $b) { return md5($a . $b); }
function cm_CreateSign($time, $random, $token)
{
    $data['time'] = $time;
    $data['random'] = $random;
    $data['token'] = $token;
    sort($data, SORT_STRING);
    return strtoupper(md5(implode($data)));
}
function cm_TestLink($params) { /* $r = 'old_action'; */ return 'success'; }
`
	res, err := FromFiles("", map[string][]byte{"cm/cm.php": []byte(src)})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := res.Spec
	if s.Auth.Scheme != SchemeMD5SortUpper {
		t.Errorf("scheme = %q（被注释的 md5 拼接不应生效）", s.Auth.Scheme)
	}
	if s.Actions[ActionTest].Path != "" {
		t.Errorf("test path = %q（注释里的路径不应生效）", s.Actions[ActionTest].Path)
	}
}

func TestFromZipReader(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"bt/bt.php":          sampleBthostsLike,
		"bt/templates/a.txt": "hello",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := FromZipReader(&buf)
	if err != nil {
		t.Fatalf("FromZipReader: %v", err)
	}
	if res.Spec.Slug != "bt" || res.Spec.Actions[ActionCreate].Path != "/api/vhost/user_create" {
		t.Errorf("spec = %+v", res.Spec)
	}
}

func TestFromZipRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../evil.php")
	w.Write([]byte("<?php function x_MetaData(){ return []; }"))
	zw.Close()
	if _, err := FromZipReader(&buf); err == nil {
		t.Fatal("expected error for zip slip path")
	}
}

func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "bt")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "bt.php"), []byte(sampleBthostsLike), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := FromDir(dir)
	if err != nil {
		t.Fatalf("FromDir: %v", err)
	}
	if res.Spec.Slug != "bt" {
		t.Errorf("slug = %q", res.Spec.Slug)
	}
}

func TestSpecRoundtripNormalizeValidate(t *testing.T) {
	res, err := FromFiles("", map[string][]byte{"x/x.php": []byte(sampleBthostsLike)})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Spec
	if err := s.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// 规格版本超前必须拒绝
	old := s.SpecVersion
	s.SpecVersion = SpecVersion + 1
	if err := s.Validate(); err != ErrSpecTooNew {
		t.Errorf("expected ErrSpecTooNew, got %v", err)
	}
	s.SpecVersion = old
	// 未知方案拒绝
	s.Auth.Scheme = "bogus"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "未知签名方案") {
		t.Errorf("expected unknown scheme error, got %v", err)
	}
}
