package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 生成测试密钥对（PKCS1 裸 base64，与参考实现的 key 文件形态一致）。
func ocgcTestKeys(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	privB64 := base64.StdEncoding.EncodeToString(der)
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pubDER)
	return privB64, pubB64
}

// ocgcTestSign 用测试自己的方式签名：hex 大写（RSA-SHA1 PKCS1v15）。
func ocgcTestSign(key *rsa.PrivateKey, data string) string {
	digest := sha1.Sum([]byte(data))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, digest[:])
	if err != nil {
		panic(err)
	}
	return strings.ToUpper(hex.EncodeToString(sig))
}

// 登录 + 下单全流程：请求体是 [JSON] 数组、字段顺序固定、签名正确；
// 响应头签名被验证；返回 qrcodeUrl。
func TestOcgcPayURL(t *testing.T) {
	privB64, pubB64 := ocgcTestKeys(t)
	privDER, _ := base64.StdEncoding.DecodeString(privB64)
	privKey, _ := x509.ParsePKCS1PrivateKey(privDER)
	_ = pubB64

	var loginBody, txnBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := ioReadAll(r.Body)
		// 验证请求签名（x-apsignature 覆盖 params= 之后的原始 JSON）。
		if strings.HasPrefix(string(raw), "params=[") {
			inner := strings.TrimPrefix(string(raw), "params=")
			if r.Header.Get("x-apsignature") != ocgcTestSign(privKey, inner) {
				t.Errorf("request signature mismatch")
			}
		} else {
			t.Errorf("body must be params=[json], got %s", raw)
		}
		if strings.Contains(string(raw), "msc/user/login") {
			loginBody = string(raw)
			resp := `[{"code":"000000","batchNo":"BN123","stl_cur":"HKD"}]`
			w.Header().Set("x-apsessionid", "SESS1")
			w.Header().Set("x-apsignature", ocgcTestSign(privKey, resp))
			w.Write([]byte(resp))
			return
		}
		txnBody = string(raw)
		inner := strings.TrimPrefix(txnBody, "params=")
		var arr []map[string]any
		if err := json.Unmarshal([]byte(inner), &arr); err != nil {
			t.Fatalf("txn body not json array: %v", err)
		}
		txn := arr[0]
		if txn["odNo"] != "INV5" {
			t.Errorf("odNo = %v", txn["odNo"])
		}
		if txn["batchNo"] != "BN123" {
			t.Errorf("batchNo = %v", txn["batchNo"])
		}
		if txn["currency"] != "HKD" {
			t.Errorf("currency = %v", txn["currency"])
		}
		// transAmount 是分（int）。
		if amt, ok := txn["transAmount"].(float64); !ok || amt != 4560 {
			t.Errorf("transAmount = %v", txn["transAmount"])
		}
		// 未赋值字段以空字符串出现（object2json 的行为）。
		if v, ok := txn["transTimeOut"]; !ok || v != "" {
			t.Errorf("transTimeOut = %v, want empty string", txn["transTimeOut"])
		}
		dataStr, _ := txn["data"].(string)
		if !strings.Contains(dataStr, "alipayOverSeaOnline") {
			t.Errorf("data = %v", dataStr)
		}
		resp := `[{"code":"000000","data":"{\"qrcodeResult\":\"SUCCESS\",\"qrcodeUrl\":\"https://qr.example.com/pay\"}"}]`
		w.Header().Set("x-apsignature", ocgcTestSign(privKey, resp))
		w.Write([]byte(resp))
	}))
	defer srv.Close()

	g := OcgcPayGateway{Now: func() time.Time { return time.Unix(1700000000, 0) }}
	u, err := g.PayURL(context.Background(),
		ProviderConfig{GatewayURL: srv.URL, MerchantID: "MERCH1", Secret: ocgcSecretJSON(privB64)},
		Prepared{OutTradeNo: "INV5", Subject: "主机", AmountCents: 4560, PayType: "alipay", NotifyURL: "https://s/notify"})
	if err != nil {
		t.Fatalf("PayURL: %v", err)
	}
	if !strings.Contains(u, "qr.example.com/pay") {
		t.Fatalf("url = %s", u)
	}
	_ = loginBody
}

// 回调验签：合法 sign 通过，篡改 body 拒绝。
func TestOcgcVerifyNotify(t *testing.T) {
	privB64, pubB64 := ocgcTestKeys(t)
	privDER, _ := base64.StdEncoding.DecodeString(privB64)
	privKey, _ := x509.ParsePKCS1PrivateKey(privDER)
	cfg := ProviderConfig{MerchantID: "M", Secret: ocgcSecretJSON(pubB64)}
	g := OcgcPayGateway{}

	body := `{"odNo":"INV5","amount":4560,"txnId":"TX9"}`
	form := map[string][]string{
		"body": {body},
		"sign": {ocgcTestSign(privKey, body)},
	}
	res := g.VerifyNotify(cfg, NotifyInput{PostForm: toURLValues(form)})
	if !res.OK || res.TradeNo != "TX9" || res.AmountCents != 4560 {
		t.Fatalf("notify = %+v", res)
	}

	bad := toURLValues(map[string][]string{
		"body": {`{"odNo":"INV5","amount":1,"txnId":"TX9"}`},
		"sign": {ocgcTestSign(privKey, body)},
	})
	if res := g.VerifyNotify(cfg, NotifyInput{PostForm: bad}); res.OK {
		t.Fatal("tampered body must fail")
	}
}

// 二维码地址（非 http）无法用跳转承载，必须明确报错。
func TestOcgcQrcodeURLError(t *testing.T) {
	privB64, _ := ocgcTestKeys(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := ioReadAll(r.Body)
		if strings.Contains(string(raw), "msc/user/login") {
			resp := `[{"batchNo":"BN","stl_cur":"HKD"}]`
			w.Write([]byte(resp))
			return
		}
		resp := `[{"data":"{\"qrcodeResult\":\"SUCCESS\",\"qrcodeUrl\":\"weixin://wxpay/bizpayurl?x=1\"}"}]`
		w.Write([]byte(resp))
	}))
	defer srv.Close()
	g := OcgcPayGateway{Now: func() time.Time { return time.Unix(1700000000, 0) }}
	_, err := g.PayURL(context.Background(),
		ProviderConfig{GatewayURL: srv.URL, MerchantID: "M", Secret: ocgcSecretJSON(privB64)},
		Prepared{OutTradeNo: "1", Subject: "s", AmountCents: 100, PayType: "alipay"})
	if err == nil || !strings.Contains(err.Error(), "二维码") {
		t.Fatalf("want qrcode error, got %v", err)
	}
}

func ocgcSecretJSON(key string) string {
	b, _ := json.Marshal(map[string]string{"private_key": key, "public_key": key, "login_pwd": "123456"})
	return string(b)
}

// 保证夹具里的裸 base64 私钥真的能被解析（PEM 形态同样支持）。
func TestOcgcPrivateKeyParse(t *testing.T) {
	privB64, _ := ocgcTestKeys(t)
	key, err := ocgcPrivateKey(privB64)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if key.N.BitLen() != 2048 {
		t.Fatal("key size wrong")
	}
	der, _ := base64.StdEncoding.DecodeString(privB64)
	pemForm := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	if _, err := ocgcPrivateKey(string(pemForm)); err != nil {
		t.Fatalf("pem form: %v", err)
	}
}

// ioReadAll / toURLValues 是测试侧的小工具。
func ioReadAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

func toURLValues(m map[string][]string) url.Values {
	out := url.Values{}
	for k, vs := range m {
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	return out
}
