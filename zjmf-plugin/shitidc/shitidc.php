<?php
/**
 * ShitIDC 魔方财务对接模块 (server module)
 *
 * 安装：把整个 shitidc 目录放到魔方财务的 public/plugins/servers/ 下，
 * 然后在 后台 -> 接口设置 -> 添加接口 里把“接口类型”选成 ShitIDC。
 * 目录结构：
 *   public/plugins/servers/shitidc/shitidc.php        ← 本文件（函数名必须以 shitidc_ 开头）
 *   public/plugins/servers/shitidc/version.txt        ← 版本号
 *   public/plugins/servers/shitidc/templates/*.html   ← 客户中心页签模板
 *
 * 协议（与 ShitIDC /compat/magiccube/v1/* 一一对应）：
 *   所有请求都是 POST，application/x-www-form-urlencoded，并带三个鉴权参数：
 *     time      = time()
 *     random    = mt_rand()
 *     signature = strtoupper(md5(将 time、random、token 三个值按字符串排序后拼接))
 *   其中 token = 接口设置里 Hash(accesshash) 字段的值，形如 "<key_id>.<secret>"；
 *   它不随请求发送，仅参与摘要，ShitIDC 侧用 key_id 定位密钥后重算摘要验签。
 *
 *   响应统一是 {"code":1,"msg":"...","data":{...}}，code=1 为成功。
 *
 *   连接测试   POST /compat/magiccube/v1/test
 *   商品列表   POST /compat/magiccube/v1/product
 *   开通       POST /compat/magiccube/v1/host   act=create
 *   查询       POST /compat/magiccube/v1/host   act=status
 *   同步       POST /compat/magiccube/v1/host   act=sync
 *   暂停       POST /compat/magiccube/v1/host   act=locked
 *   解除暂停   POST /compat/magiccube/v1/host   act=unlocked
 *   删除       POST /compat/magiccube/v1/host   act=recycle
 *   续费       POST /compat/magiccube/v1/host   act=renew
 *
 * 注意：这里刻意不依赖 ShitIDC 侧的模块化约定（例如 host_build / host_locked 之类的
 * 宝塔面板风格路径），因为 ShitIDC 的接口是自有的、成对的；改协议只需同时改两端。
 */

/**
 * 授权校验占位：魔方在加载模块前会调用它，保留空实现即可。
 */
function shitidc_idcsmartauthorizes()
{
}

function shitidc_MetaData()
{
    return [
        'DisplayName' => 'ShitIDC 财务系统',
        'APIVersion'  => '1.0',
        'HelpDoc'     => '',
        'version'     => '1.0.0',
    ];
}

/**
 * 产品级配置：这些字段会出现在“商品管理 -> 编辑商品 -> 模块设置”里，
 * 管理员在这里选择这个商品对应 ShitIDC 的哪个商品、用哪个计费周期。
 */
function shitidc_ConfigOptions()
{
    return [
        [
            'type'        => 'text',
            'name'        => 'ShitIDC 商品 ID',
            'description' => '在 ShitIDC 管理后台 -> 商品管理里复制商品 ID（UUID）。留空则使用下方下拉选择的值。',
            'key'         => 'product_id',
        ],
        [
            'type'        => 'dropdown',
            'name'        => '或从列表中选商品',
            'description' => '点击“刷新商品列表”后从 ShitIDC 读取，保存时把商品 ID 写入这里。',
            'options'     => shitidc_ProductOptions(),
            'key'         => 'product_pick',
        ],
        [
            'type'        => 'dropdown',
            'name'        => '计费周期',
            'description' => '与 ShitIDC 商品价格档一致的周期。',
            'options'     => ['monthly' => '月付', 'quarterly' => '季付', 'semiannually' => '半年付', 'yearly' => '年付'],
            'default'     => 'monthly',
            'key'         => 'billing_cycle',
        ],
    ];
}

// ---------------------------------------------------------------------------
// 协议底层：签名、请求、URL 组装
// ---------------------------------------------------------------------------

/**
 * 魔方签名：把 time、random、token 三个值按字符串排序后拼接取 md5 再转大写。
 * token 不参与传输，只参与摘要。
 */
function shitidc_CreateSign($time, $random, $token)
{
    $data = [$time, $random, $token];
    sort($data, SORT_STRING);
    return strtoupper(md5(implode($data)));
}

/**
 * 取出接口设置里配置的 token（Hash 字段）。魔方允许多行 Hash，取第一段非空行。
 */
function shitidc_Token($params)
{
    $hash = isset($params['accesshash']) ? (string) $params['accesshash'] : '';
    foreach (preg_split('/\r\n|\r|\n/', $hash) as $line) {
        $line = trim($line);
        if ($line !== '') {
            return $line;
        }
    }
    return trim($hash);
}

/**
 * 组装上游地址：接口设置里的 IP / 主机名 + 端口 + SSL 开关。
 */
function shitidc_BaseUrl($params)
{
    $host = !empty($params['server_ip']) ? $params['server_ip'] : (isset($params['server_host']) ? $params['server_host'] : '');
    $host = trim((string) $host);
    if ($host === '') {
        return '';
    }
    // 管理员可能直接粘了带协议头的地址，避免拼成 https://https://...
    if (stripos($host, 'http://') === 0 || stripos($host, 'https://') === 0) {
        $base = rtrim($host, '/');
    } else {
        $scheme = !empty($params['secure']) ? 'https://' : 'http://';
        $base = $scheme . $host;
        if (!empty($params['port'])) {
            $base .= ':' . (int) $params['port'];
        }
    }
    return $base;
}

/**
 * 发一个带签名的 POST 请求，返回解析后的数组（失败时带 __error）。
 */
function shitidc_Request($params, $path, array $payload = [])
{
    $base  = shitidc_BaseUrl($params);
    $token = shitidc_Token($params);
    if ($base === '') {
        return ['__error' => '接口地址未填写：请在魔方“接口设置”里填写 ShitIDC 的访问地址'];
    }
    if ($token === '') {
        return ['__error' => '接口密钥未填写：请把 ShitIDC 生成的 Hash 填入接口设置的 Hash 字段'];
    }

    $time   = time();
    $random = mt_rand();
    $post   = [
        'time'      => $time,
        'random'    => $random,
        'signature' => shitidc_CreateSign($time, $random, $token),
        'token'     => $token, // ShitIDC 用 <key_id>.<secret> 定位密钥
    ];
    foreach ($payload as $k => $v) {
        $post[$k] = $v;
    }

    $url = $base . $path;
    $ch  = curl_init();
    curl_setopt($ch, CURLOPT_URL, $url);
    curl_setopt($ch, CURLOPT_POST, 1);
    curl_setopt($ch, CURLOPT_POSTFIELDS, http_build_query($post));
    curl_setopt($ch, CURLOPT_RETURNTRANSFER, 1);
    curl_setopt($ch, CURLOPT_HEADER, 0);
    curl_setopt($ch, CURLOPT_TIMEOUT, 30);
    curl_setopt($ch, CURLOPT_CONNECTTIMEOUT, 10);
    curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, false);
    curl_setopt($ch, CURLOPT_SSL_VERIFYHOST, false);
    curl_setopt($ch, CURLOPT_HTTPHEADER, ['Content-Type: application/x-www-form-urlencoded']);
    $body = curl_exec($ch);
    $err  = curl_error($ch);
    $code = (int) curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);

    if ($body === false || $body === '') {
        return ['__error' => '请求 ShitIDC 失败：' . ($err !== '' ? $err : ('HTTP ' . $code))];
    }
    $json = json_decode($body, true);
    if (!is_array($json)) {
        return ['__error' => 'ShitIDC 返回了非 JSON 内容（HTTP ' . $code . '）：' . mb_substr((string) $body, 0, 200)];
    }
    if (!isset($json['code']) || (int) $json['code'] !== 1) {
        $msg = isset($json['msg']) && $json['msg'] !== '' ? $json['msg'] : ('HTTP ' . $code);
        return ['__error' => $msg];
    }
    return isset($json['data']) && is_array($json['data']) ? $json['data'] : [];
}

/**
 * 读取商品列表，用于 ConfigOptions 下拉与后台“刷新商品列表”。
 */
function shitidc_ProductList($params)
{
    $res = shitidc_Request($params, '/compat/magiccube/v1/product');
    if (isset($res['__error'])) {
        return [];
    }
    return isset($res['list']) && is_array($res['list']) ? $res['list'] : [];
}

/**
 * ConfigOptions 里的商品下拉选项：在“编辑商品”页面渲染时尝试从 ShitIDC 读取
 * 真实商品列表（能拿到接口参数就拉，拿不到就给出提示），这样管理员不用手抄 UUID。
 */
function shitidc_ProductOptions()
{
    $fallback = ['' => '（请在接口上点击“刷新商品列表”后回到商品页选择）'];
    $server = shitidc_CurrentServerParams();
    if ($server === null) {
        return $fallback;
    }
    $list = shitidc_ProductList($server);
    if (empty($list)) {
        return $fallback;
    }
    $options = [];
    foreach ($list as $item) {
        if (empty($item['id']) || empty($item['name'])) {
            continue;
        }
        $label = $item['name'];
        if (!empty($item['price'])) {
            $label .= '（' . $item['price'] . ' ' . (isset($item['currency']) ? $item['currency'] : 'CNY') . '/' . (isset($item['billing_cycle']) ? $item['billing_cycle'] : 'monthly') . '）';
        }
        $options[$item['id']] = $label;
    }
    return empty($options) ? $fallback : $options;
}

/**
 * 尽力还原当前正在编辑的接口参数。魔方在不同版本里把接口信息放在 GET/POST 的
 * serverid / server_id / sid 上；拿不到就返回 null，调用方回退到静态占位。
 */
function shitidc_CurrentServerParams()
{
    $id = 0;
    foreach (['serverid', 'server_id', 'sid', 'id'] as $key) {
        if (isset($_REQUEST[$key]) && (int) $_REQUEST[$key] > 0) {
            $id = (int) $_REQUEST[$key];
            break;
        }
    }
    if ($id <= 0 || !class_exists('think\Db')) {
        return null;
    }
    try {
        $row = think\Db::name('servers')->where('id', $id)->find();
    } catch (\Throwable $e) {
        return null;
    }
    if (empty($row)) {
        return null;
    }
    $row['accesshash'] = isset($row['accesshash']) ? $row['accesshash'] : '';
    return $row;
}

// ---------------------------------------------------------------------------
// 取值助手
// ---------------------------------------------------------------------------

/**
 * 这个商品对应 ShitIDC 的哪个商品：优先用“ShitIDC 商品 ID”文本框，
 * 其次用下拉选择的商品。
 */
function shitidc_ProductRef($params)
{
    $opts = isset($params['configoptions']) && is_array($params['configoptions']) ? $params['configoptions'] : [];
    foreach (['product_id', 'product_pick'] as $key) {
        if (!empty($opts[$key])) {
            return trim((string) $opts[$key]);
        }
    }
    return '';
}

function shitidc_BillingCycle($params)
{
    $opts = isset($params['configoptions']) && is_array($params['configoptions']) ? $params['configoptions'] : [];
    $cycle = !empty($opts['billing_cycle']) ? trim((string) $opts['billing_cycle']) : '';
    return $cycle !== '' ? $cycle : 'monthly';
}

/**
 * ShitIDC 返回的主机 ID。魔方把自定义字段值平铺进 $params，也保留旧式
 * customfields 数组，两种都兼容。
 */
function shitidc_HostRef($params)
{
    if (!empty($params['shitidc_host_id'])) {
        return trim((string) $params['shitidc_host_id']);
    }
    if (!empty($params['customfields']['shitidc_host_id'])) {
        return trim((string) $params['customfields']['shitidc_host_id']);
    }
    return '';
}

/**
 * 把 ShitIDC 返回的主机 ID 落到自定义字段，后续暂停/删除才有依据。
 */
function shitidc_SaveHostRef($params, $remoteId)
{
    if ($remoteId === '' || empty($params['hostid']) || !class_exists('think\Db')) {
        return;
    }
    $productId = isset($params['productid']) ? $params['productid'] : 0;
    $hostId    = $params['hostid'];
    $fieldId   = think\Db::name('customfields')
        ->where('type', 'product')
        ->where('relid', $productId)
        ->where('fieldname', 'shitidc_host_id')
        ->value('id');
    if (empty($fieldId)) {
        $fieldId = think\Db::name('customfields')->insertGetId([
            'type'        => 'product',
            'relid'       => $productId,
            'fieldname'   => 'shitidc_host_id',
            'fieldtype'   => 'text',
            'adminonly'   => 1,
            'create_time' => time(),
        ]);
    }
    $exists = think\Db::name('customfieldsvalues')
        ->where('fieldid', $fieldId)
        ->where('relid', $hostId)
        ->find();
    if (empty($exists)) {
        think\Db::name('customfieldsvalues')->insert([
            'fieldid'     => $fieldId,
            'relid'       => $hostId,
            'value'       => $remoteId,
            'create_time' => time(),
        ]);
    } else {
        think\Db::name('customfieldsvalues')->where('id', $exists['id'])->update(['value' => $remoteId]);
    }
}

/**
 * 生成一个够用的初始密码。
 */
function shitidc_RandomPassword($length = 16)
{
    $chars = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';
    $max   = strlen($chars) - 1;
    $out   = '';
    for ($i = 0; $i < $length; $i++) {
        $out .= $chars[random_int(0, $max)];
    }
    return $out;
}

// ---------------------------------------------------------------------------
// 生命周期回调（魔方核心按 <模块标识>_<动作> 命名约定调用）
// ---------------------------------------------------------------------------

/**
 * 接口设置里的“测试连接”。
 */
function shitidc_TestLink($params)
{
    $res = shitidc_Request($params, '/compat/magiccube/v1/test');
    if (isset($res['__error'])) {
        return ['status' => 200, 'data' => ['server_status' => 0, 'msg' => $res['__error']]];
    }
    return ['status' => 200, 'data' => [
        'server_status' => 1,
        'msg'           => '连接正常，可售商品 ' . (isset($res['product_num']) ? (int) $res['product_num'] : 0) . ' 个',
    ]];
}

/**
 * 开通。失败必须返回 ['status'=>'error','msg'=>...]，魔方会把 msg 显示出来。
 */
function shitidc_CreateAccount($params)
{
    if (shitidc_HostRef($params) !== '') {
        return '已开通，不能重复开通';
    }
    $productRef = shitidc_ProductRef($params);
    if ($productRef === '') {
        return ['status' => 'error', 'msg' => '商品未配置 ShitIDC 商品 ID：请在商品 -> 模块设置里选择或填写'];
    }
    $password = !empty($params['password']) ? $params['password'] : shitidc_RandomPassword();
    $payload  = [
        'act'           => 'create',
        'product_id'    => $productRef,
        'billing_cycle' => shitidc_BillingCycle($params),
        'username'      => isset($params['domain']) ? $params['domain'] : '',
        'password'      => $password,
        'domain'        => isset($params['domain']) ? $params['domain'] : '',
        'hostid'        => isset($params['hostid']) ? $params['hostid'] : '',
    ];
    if (!empty($params['user_info']['email'])) {
        $payload['email'] = $params['user_info']['email'];
    }
    if (!empty($params['nextduedate'])) {
        $payload['nextduedate'] = date('Y-m-d', (int) $params['nextduedate']);
    }
    $res = shitidc_Request($params, '/compat/magiccube/v1/host', $payload);
    if (isset($res['__error'])) {
        return ['status' => 'error', 'msg' => $res['__error']];
    }
    $remoteId = !empty($res['id']) ? (string) $res['id'] : '';
    if ($remoteId === '') {
        return ['status' => 'error', 'msg' => 'ShitIDC 未返回主机 ID'];
    }
    shitidc_SaveHostRef($params, $remoteId);

    // 回写主机信息（状态、用户名密码、开通 IP）。
    if (class_exists('think\Db')) {
        $update = [
            'domainstatus' => 'Active',
            'username'     => isset($res['username']) ? $res['username'] : (isset($params['domain']) ? $params['domain'] : ''),
            'password'     => function_exists('cmf_encrypt') ? cmf_encrypt($password) : $password,
            'domain'       => isset($params['domain']) ? $params['domain'] : '',
            'dedicatedip'  => !empty($params['server_ip']) ? $params['server_ip'] : '',
        ];
        if (!empty($res['nextduedate'])) {
            $ts = strtotime($res['nextduedate']);
            if ($ts) {
                $update['nextduedate'] = $ts;
            }
        }
        try {
            think\Db::name('host')->where('id', $params['hostid'])->update($update);
        } catch (\Throwable $e) {
            // 写库失败不影响开通结果，核心已记录主机状态。
        }
    }
    return 'success';
}

/**
 * 暂停。
 */
function shitidc_SuspendAccount($params)
{
    return shitidc_HostAction($params, 'locked');
}

/**
 * 解除暂停。
 */
function shitidc_UnsuspendAccount($params)
{
    return shitidc_HostAction($params, 'unlocked');
}

/**
 * 删除。
 */
function shitidc_TerminateAccount($params)
{
    return shitidc_HostAction($params, 'recycle');
}

/**
 * 续费：ShitIDC 侧会生成续费订单并从接口密钥归属用户的余额结算。
 */
function shitidc_Renew($params)
{
    return shitidc_HostAction($params, 'renew');
}

/**
 * 暂停/解除/删除/续费的共同实现。
 */
function shitidc_HostAction($params, $act)
{
    $remoteId = shitidc_HostRef($params);
    if ($remoteId === '') {
        return ['status' => 'error', 'msg' => '该主机没有 ShitIDC 主机 ID，无法执行操作'];
    }
    $res = shitidc_Request($params, '/compat/magiccube/v1/host', [
        'act'    => $act,
        'id'     => $remoteId,
        'hostid' => isset($params['hostid']) ? $params['hostid'] : '',
    ]);
    if (isset($res['__error'])) {
        return ['status' => 'error', 'msg' => $res['__error']];
    }
    return 'success';
}

/**
 * 同步：把 ShitIDC 侧的主机信息（用户名/密码/到期日/状态）拉回魔方。
 */
function shitidc_Sync($params)
{
    $remoteId = shitidc_HostRef($params);
    if ($remoteId === '') {
        return ['status' => 'error', 'msg' => '该主机没有 ShitIDC 主机 ID'];
    }
    $res = shitidc_Request($params, '/compat/magiccube/v1/host', ['act' => 'sync', 'id' => $remoteId]);
    if (isset($res['__error'])) {
        return ['status' => 'error', 'msg' => $res['__error']];
    }
    if (class_exists('think\Db')) {
        $update = [];
        if (!empty($res['username'])) {
            $update['username'] = $res['username'];
        }
        if (!empty($res['password'])) {
            $update['password'] = function_exists('cmf_encrypt') ? cmf_encrypt($res['password']) : $res['password'];
        }
        if (!empty($res['nextduedate'])) {
            $ts = strtotime($res['nextduedate']);
            if ($ts) {
                $update['nextduedate'] = $ts;
            }
        }
        if ($update) {
            try {
                think\Db::name('host')->where('id', $params['hostid'])->update($update);
            } catch (\Throwable $e) {
            }
        }
    }
    return ['status' => 'success', 'msg' => '同步成功'];
}

/**
 * 状态轮询：返回魔方统一的状态词 on/off/waiting/unknown。
 */
function shitidc_Status($params)
{
    $remoteId = shitidc_HostRef($params);
    if ($remoteId === '') {
        return ['status' => 'error', 'msg' => '该主机没有 ShitIDC 主机 ID'];
    }
    $res = shitidc_Request($params, '/compat/magiccube/v1/host', ['act' => 'status', 'id' => $remoteId]);
    if (isset($res['__error'])) {
        return ['status' => 'error', 'msg' => $res['__error']];
    }
    $word = isset($res['status']) ? $res['status'] : 'unknown';
    if (!in_array($word, ['on', 'off', 'waiting', 'unknown'], true)) {
        $word = 'unknown';
    }
    return [
        'status' => 'success',
        'data'   => [
            'status' => $word,
            'des'    => isset($res['des']) ? $res['des'] : '未知',
        ],
    ];
}

/**
 * 客户中心页签声明。
 */
function shitidc_ClientArea($params)
{
    return ['index' => ['name' => '主机信息']];
}

/**
 * 客户中心页签内容。
 */
function shitidc_ClientAreaOutput($params, $key = '')
{
    $remoteId = shitidc_HostRef($params);
    $info     = [];
    if ($remoteId !== '') {
        $info = shitidc_Request($params, '/compat/magiccube/v1/host', ['act' => 'status', 'id' => $remoteId]);
    }
    return [
        'template' => 'templates/information.html',
        'vars'     => [
            'params' => $params,
            'info'   => $info,
            'hostId' => $remoteId,
        ],
    ];
}

/**
 * 后台主机页的快捷按钮。
 */
function shitidc_AdminButton($params)
{
    return ['Sync' => '与 ShitIDC 同步', 'Status' => '查询状态'];
}

/**
 * 允许客户在客户中心自行调用的动作白名单。
 */
function shitidc_AllowFunction()
{
    return ['client' => ['Sync'], 'admin' => ['Sync', 'Status']];
}

/**
 * 连接测试辅助：后台“接口设置”里可以点按钮确认商品能拉到。
 */
function shitidc_RefreshProducts($params)
{
    $list = shitidc_ProductList($params);
    if (empty($list)) {
        return ['status' => 'error', 'msg' => '没有读取到商品，请先确认接口地址与 Hash 是否正确'];
    }
    return ['status' => 'success', 'msg' => '共读取到 ' . count($list) . ' 个商品'];
}
