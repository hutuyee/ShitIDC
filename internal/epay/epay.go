// Package epay implements the ubiquitous 易支付 (Epay) payment protocol:
// MD5-signed submit parameters, async notify verification and return redirects.
package epay

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	StatusSuccess = "TRADE_SUCCESS"
	SignTypeMD5   = "MD5"
)

type Config struct {
	GatewayURL string
	PID        string
	Key        string
}

func (c Config) Valid() error {
	if strings.TrimSpace(c.GatewayURL) == "" || strings.TrimSpace(c.PID) == "" || strings.TrimSpace(c.Key) == "" {
		return fmt.Errorf("易支付网关地址、商户 ID 和商户密钥均不能为空")
	}
	return nil
}

// Sign computes the MD5 signature of the given parameters using the epay rule:
// sort by key (excluding sign / sign_type / empty values), join as k=v with &,
// append the merchant key directly and hash lower-case hex MD5.
func (c Config) Sign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+params[k])
	}
	sum := md5.Sum([]byte(strings.Join(pairs, "&") + c.Key))
	return hex.EncodeToString(sum[:])
}

// SubmitParams are the parameters for a /submit.php gateway request.
type SubmitParams struct {
	PayType    string // alipay / wxpay / qqpay ...
	OutTradeNo string
	Name       string
	MoneyCents int64
	NotifyURL  string
	ReturnURL  string
	SiteName   string
	ClientIP   string
}

// PayURL builds the redirect URL that sends the user to the gateway checkout.
func (c Config) PayURL(p SubmitParams) (string, error) {
	if err := c.Valid(); err != nil {
		return "", err
	}
	money := fmt.Sprintf("%d.%02d", p.MoneyCents/100, p.MoneyCents%100)
	params := map[string]string{
		"pid":          c.PID,
		"type":         p.PayType,
		"out_trade_no": p.OutTradeNo,
		"notify_url":   p.NotifyURL,
		"return_url":   p.ReturnURL,
		"name":         p.Name,
		"money":        money,
		"sitename":     p.SiteName,
		"device":       "",
		"param":        p.ClientIP,
	}
	params["sign"] = c.Sign(params)
	params["sign_type"] = SignTypeMD5

	base := strings.TrimRight(strings.TrimSpace(c.GatewayURL), "/")
	if !strings.Contains(base, "submit.php") {
		base += "/submit.php"
	}
	q := url.Values{}
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	return base + "?" + q.Encode(), nil
}

// VerifyNotify validates an async notify / sync return callback.
func (c Config) VerifyNotify(values url.Values) error {
	if values.Get("sign") == "" {
		return fmt.Errorf("缺少签名")
	}
	params := map[string]string{}
	for k, vs := range values {
		if len(vs) > 0 {
			params[k] = vs[0]
		}
	}
	expected := c.Sign(params)
	if !strings.EqualFold(expected, values.Get("sign")) {
		return fmt.Errorf("签名校验失败")
	}
	if values.Get("pid") != c.PID {
		return fmt.Errorf("商户 ID 不匹配")
	}
	return nil
}

// MoneyToCents parses a gateway money string such as "10.00" into cents.
func MoneyToCents(money string) (int64, error) {
	money = strings.TrimSpace(money)
	if money == "" {
		return 0, fmt.Errorf("金额为空")
	}
	neg := false
	if strings.HasPrefix(money, "-") {
		neg = true
		money = money[1:]
	}
	intPart, fracPart := money, ""
	if i := strings.Index(money, "."); i >= 0 {
		intPart, fracPart = money[:i], money[i+1:]
	}
	if len(fracPart) > 2 {
		fracPart = fracPart[:2]
	}
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	var intCents int64
	if intPart != "" {
		if _, err := fmt.Sscanf(intPart, "%d", &intCents); err != nil {
			return 0, fmt.Errorf("金额格式错误")
		}
	}
	var fracCents int64
	if _, err := fmt.Sscanf(fracPart, "%d", &fracCents); err != nil {
		return 0, fmt.Errorf("金额格式错误")
	}
	total := intCents*100 + fracCents
	if neg {
		total = -total
	}
	return total, nil
}
