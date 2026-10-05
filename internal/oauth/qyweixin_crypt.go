// 企业微信回调消息加解密（WXBizMsgCrypt 的 Go 子集）。
//
// 参考实现是官方 PHP 示例（sdk/Prpcrypt.php + SHA1.php + XMLParse.php），
// 口径逐项对齐：
//
//   - EncodingAESKey 固定 43 位，补一个 "=" 后 base64 解出 32 字节 AES-256 密钥；
//   - 签名 = sha1(排序后的 token/timestamp/nonce/encrypt 四个字符串直接拼接)，小写 hex；
//   - AES-256-CBC，IV 取密钥前 16 字节；
//   - 明文结构：16 字节随机 + 4 字节大端 msg_len + msg + receiveid，PKCS7 填充（块大小按微信口径取 32）；
//   - VerifyURL 解的就是 echostr；消息推送先取出 <Encrypt> 再解。
//
// 这里不做 receiveid 强校验：签名已经证明报文来自持有 token 的企业微信，
// 而不同推送场景的 receiveid 是 corp_id 或 suite_id（官方示例自身也不一致）。
package oauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"sort"
	"strings"
)

// qyweixinSignature 计算回调签名：四个字符串排序后直接拼接再 sha1。
func qyweixinSignature(token, timestamp, nonce, encrypt string) string {
	arr := []string{token, timestamp, nonce, encrypt}
	sort.Strings(arr)
	sum := sha1.Sum([]byte(strings.Join(arr, "")))
	return hex.EncodeToString(sum[:])
}

// qyweixinAESKey 解析 EncodingAESKey（43 位）。
func qyweixinAESKey(aesKey string) ([]byte, error) {
	aesKey = strings.TrimSpace(aesKey)
	if len(aesKey) != 43 {
		return nil, errors.New("EncodingAESKey 长度应为 43 位")
	}
	key, err := base64.StdEncoding.DecodeString(aesKey + "=")
	if err != nil || len(key) != 32 {
		return nil, errors.New("EncodingAESKey 不是合法的 base64")
	}
	return key, nil
}

// qyweixinDecrypt 校验签名并解密一条 base64 密文。
func qyweixinDecrypt(token, aesKey, msgSignature, timestamp, nonce, encrypt string) (string, error) {
	want := qyweixinSignature(token, timestamp, nonce, encrypt)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(msgSignature)), []byte(want)) != 1 {
		return "", errors.New("企业微信回调签名校验失败")
	}
	key, err := qyweixinAESKey(aesKey)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(encrypt)
	if err != nil || len(raw) < 32 || len(raw)%aes.BlockSize != 0 {
		return "", errors.New("企业微信回加密文长度不合法")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(plain, raw)
	// PKCS7 去填充：末字节是填充长度（微信官方按 1..32 校验）。
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > 32 || pad > len(plain) {
		return "", errors.New("企业微信回加密文填充不合法")
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return "", errors.New("企业微信回加密文填充不合法")
		}
	}
	plain = plain[:len(plain)-pad]
	if len(plain) < 20 {
		return "", errors.New("企业微信回加密文过短")
	}
	msgLen := int(binary.BigEndian.Uint32(plain[16:20]))
	if msgLen < 0 || msgLen > len(plain)-20 {
		return "", errors.New("企业微信回加密文消息长度不合法")
	}
	return string(plain[20 : 20+msgLen]), nil
}

// QyweixinVerifyURL 处理指令回调 URL 校验（GET echostr），返回要原样吐出的明文。
func QyweixinVerifyURL(token, aesKey, msgSignature, timestamp, nonce, echoStr string) (string, error) {
	return qyweixinDecrypt(token, aesKey, msgSignature, timestamp, nonce, echoStr)
}

// QyweixinDecryptMessage 解密消息推送的 XML，返回内部明文 XML。
func QyweixinDecryptMessage(token, aesKey, msgSignature, timestamp, nonce, body string) (string, error) {
	encrypt := qyweixinXMLTag(body, "Encrypt")
	if encrypt == "" {
		return "", errors.New("企业微信回调缺少 Encrypt 字段")
	}
	return qyweixinDecrypt(token, aesKey, msgSignature, timestamp, nonce, encrypt)
}

// QyweixinParseSuiteTicket 从解密后的明文里取 SuiteTicket（没有则返回空串）。
func QyweixinParseSuiteTicket(plain string) string {
	return qyweixinXMLTag(plain, "SuiteTicket")
}

// qyweixinXMLTag 读出 XML 中某个标签的文本（含 CDATA）。
func qyweixinXMLTag(body, tag string) string {
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == tag {
			var s string
			if err := dec.DecodeElement(&s, &se); err != nil {
				return ""
			}
			return strings.TrimSpace(s)
		}
	}
}
