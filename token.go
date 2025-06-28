package udpx

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const (
	secretKey       = "udpx#9vR0zYw5nC"
	TokenTimeWindow = 10 // Token 有效期, 单位: 秒
)

func GenToken() []byte {
	return generateToken(secretKey)
}
func VerifyToken(token []byte) (bool, error) {
	return verifyToken(token, secretKey, TokenTimeWindow)
}

// generateToken 生成动态 Token (4字节)
// 参数: secret - 预设的密钥字符串
// 返回: 4字节的 token
func generateToken(secret string) []byte {
	// 获取当前 UTC 秒数
	now := time.Now().UTC().Unix()
	t := uint16(now)

	// 准备用于计算摘要的数据: secret + 完整时间戳
	data := append([]byte(secret), make([]byte, 2)...)
	binary.BigEndian.PutUint16(data[len(secret):], t)

	// 计算 SHA-256 摘要
	hash := sha256.Sum256(data)

	// 构建 4 字节 Token:
	//   [0] = 摘要首字节
	//   [1] = 时间高位字节
	//   [2] = 时间低位字节
	//   [3] = 摘要尾字节
	token := make([]byte, 4)
	token[0] = hash[0]           // 摘要头
	token[1] = byte(t >> 8)      // 时间高位
	token[2] = byte(t)           // 时间低位
	token[3] = hash[len(hash)-1] // 摘要尾

	return token
}

// verifyToken 验证 Token 有效性
// 参数:
//
//	token  - 待验证的 4 字节 token
//	secret - 预设密钥
//	window - 时间窗口(秒)，默认 10 秒
//
// 返回: 是否验证通过
func verifyToken(token []byte, secret string, window int) (bool, error) {
	if len(token) != 4 {
		return false, errors.New("invalid token length")
	}

	if window <= 0 {
		window = 10 // 默认 10秒有效期
	}

	// 从 Token 提取时间部分 (低16位)
	tokenTime := uint16(token[1])<<8 | uint16(token[2])

	// 获取当前 UTC 秒数
	now := time.Now().UTC().Unix()
	currentTime := uint16(now)

	if tokenTime-currentTime > uint16(window) {
		return false, fmt.Errorf("Token timestamp too far from now, tokenTime: %d, currentTime: %d, window: %d\n", tokenTime, currentTime, window) // 时间戳相差太远
	}

	// 准备用于计算摘要的数据: secret + 部分时间戳
	data := append([]byte(secret), make([]byte, 2)...)
	binary.BigEndian.PutUint16(data[len(secret):], currentTime)

	// 计算 SHA-256 摘要
	hash := sha256.Sum256(data)

	// 验证摘要头尾是否匹配
	if hash[0] == token[0] && hash[len(hash)-1] == token[3] {
		return true, nil // 验证通过
	}

	return false, nil // 所有候选值均验证失败
}
