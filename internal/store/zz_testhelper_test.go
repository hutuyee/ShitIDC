package store

import "github.com/hutuyee/ShitIDC/internal/security"

// securityHash 只是把密码哈希包一层，方便测试里直接调用。
func securityHash(plain string) (string, error) { return security.HashPassword(plain) }
