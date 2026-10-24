package panel

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
)

// GenerateRealityKeys 生成 REALITY 的 X25519 密钥对与 shortId。
//
// 格式与 `xray x25519` 一致：**base64url 无填充**（43 字符），shortId 是 8 位十六进制。
// 用标准库 crypto/ecdh 原生生成，不依赖 xray 二进制——装机顺序上 xray 可能还没装，
// 而且少一次子进程调用。
func GenerateRealityKeys() (priv, pub, shortID string, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	priv = base64.RawURLEncoding.EncodeToString(key.Bytes())
	pub = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", err
	}
	return priv, pub, hex.EncodeToString(buf), nil
}
