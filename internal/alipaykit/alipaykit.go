// Package alipaykit 是支付宝 openapi 网关共用的签名工具。
//
// 支付（alipay.trade.page.pay）与第三方登录（alipay.system.oauth.token /
// alipay.user.info.share）走的是同一个网关协议：参数按名排序、排除 sign/sign_type
// 与空值后拼成 k=v&k=v，用商户私钥做 RSA2(SHA256) 签名。签名与密钥解析只有一份实现，
// 支付与登录两个包都从这里取，避免同一套字节级规则出现两个副本。
package alipaykit

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/url"
	"sort"
	"strings"
)

// ParsePrivateKey 接受 PKCS8/PKCS1 PEM 或纯 base64（PKCS8）的商户私钥。
func ParsePrivateKey(raw string) (*rsa.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	if block, _ := pem.Decode([]byte(raw)); block != nil {
		if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if key, ok := k.(*rsa.PrivateKey); ok {
				return key, nil
			}
			return nil, errors.New("商户私钥不是 RSA 私钥")
		}
		if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return k, nil
		}
		return nil, errors.New("商户私钥 PEM 解析失败")
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("商户私钥既不是 PEM 也不是 base64")
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if key, ok := k.(*rsa.PrivateKey); ok {
			return key, nil
		}
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, errors.New("商户私钥解析失败")
	}
	return key, nil
}

// ParsePublicKey 接受 X.509 PEM 或纯 base64 SubjectPublicKeyInfo 的支付宝公钥。
func ParsePublicKey(raw string) (*rsa.PublicKey, error) {
	raw = strings.TrimSpace(raw)
	if block, _ := pem.Decode([]byte(raw)); block != nil {
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, errors.New("支付宝公钥 PEM 解析失败")
		}
		key, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("支付宝公钥不是 RSA 公钥")
		}
		return key, nil
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("支付宝公钥既不是 PEM 也不是 base64")
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, errors.New("支付宝公钥解析失败")
	}
	key, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("支付宝公钥不是 RSA 公钥")
	}
	return key, nil
}

// SignContent 构造待签名串：参数按名排序拼成 k=v&k=v，排除 sign / sign_type 与
// 空值，且不做 URL 编码（支付宝签名规范原文如此）。
func SignContent(params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "sign" || k == "sign_type" || params.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params.Get(k))
	}
	return strings.Join(parts, "&")
}

// SignRSA2 用商户私钥签名（SHA256 摘要 + PKCS1v15），返回 base64。
func SignRSA2(privateKey *rsa.PrivateKey, content string) (string, error) {
	digest := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyRSA2 验证支付宝侧的 RSA2 签名。
func VerifyRSA2(publicKey *rsa.PublicKey, content, signature string) bool {
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(content))
	return rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], sig) == nil
}
