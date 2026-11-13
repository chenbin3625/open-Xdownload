package xapi

import (
	"crypto/sha256"
	"encoding/hex"
)

// ViewerKey 是成员分页绑定账号用的键：auth_token 的 sha256 十六进制。
// jobs 的检查点持久化了这个值，格式不能变，否则已保存的检查点无法续跑。
func ViewerKey(authToken string) string {
	sum := sha256.Sum256([]byte(authToken))
	return hex.EncodeToString(sum[:])
}
