package plugin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// 双向 HMAC-SHA256 签名（与 Avalon avalonSignature 同构，仅头名不同）：
// 请求与应答都携带时间戳 + 签名头，签名覆盖 "时间戳.原始正文"，
// 防正文与时间戳篡改。
const (
	headerTimestamp = "X-Cascade-Timestamp"
	headerSignature = "X-Cascade-Signature"

	// signaturePrefix 签名值前缀（版本化，便于将来换算法）
	signaturePrefix = "v1="

	// maxClockSkew 时间戳偏移容忍（秒）
	maxClockSkew = 300
)

// signBody 计算 body 在给定密钥与时间戳下的签名值（含 "v1=" 前缀）：
//
//	v1=hex(HMAC-SHA256("<unix秒>.<原始正文>", secret))
//
// GET /v1/manifest 的正文按空串参与签名。
func signBody(secret []byte, unixSec int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", unixSec)
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// signRequest 给一条出站请求打上签名头（用主密钥 = 列表第一个）
func signRequest(req *http.Request, secret []byte, body []byte) {
	ts := time.Now().Unix()
	req.Header.Set(headerTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(headerSignature, signBody(secret, ts, body))
}

// verifyResponse 校验应答的签名头：时间戳新鲜 + 任一密钥验签通过。
// 任一环节不满足都返回错误——Cascade 只接受可验证的应答。
func verifyResponse(secrets [][]byte, resp *http.Response, body []byte) error {
	tsRaw := resp.Header.Get(headerTimestamp)
	sig := resp.Header.Get(headerSignature)
	if tsRaw == "" || sig == "" {
		return fmt.Errorf("missing signature headers")
	}
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp header: %w", err)
	}
	if d := time.Now().Unix() - ts; d > maxClockSkew || d < -maxClockSkew {
		return fmt.Errorf("timestamp skew %ds exceeds %ds", d, maxClockSkew)
	}
	for _, s := range secrets {
		if hmac.Equal([]byte(signBody(s, ts, body)), []byte(sig)) {
			return nil
		}
	}
	return fmt.Errorf("signature mismatch")
}
