package workbuddy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// GenerateQimei36 生成 36 位小写十六进制 qimei36 设备指纹。
func GenerateQimei36() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%036x", time.Now().UnixNano())[:36]
	}
	return hex.EncodeToString(b)
}

// GenerateMachineID 生成小写标准 UUID v4 作为成对 machineId。
func GenerateMachineID() string {
	return generateUUIDv4()
}

// GenerateDevice 生成一套全新的成对设备指纹 (qimei36, machineID)。
func GenerateDevice() (string, string) {
	return GenerateQimei36(), GenerateMachineID()
}

// EnsureDevice 确保成对设备指纹有效：缺失或格式非法时补齐，已合法时原样保留。
func EnsureDevice(qimei36, machineID string) (string, string) {
	q := strings.TrimSpace(qimei36)
	m := strings.TrimSpace(machineID)
	if !IsValidQimei36(q) {
		q = GenerateQimei36()
	}
	if !IsValidMachineID(m) {
		m = GenerateMachineID()
	} else {
		m = strings.ToLower(m)
	}
	return q, m
}

// IsValidQimei36 检查是否为 36 位小写十六进制字符串。
func IsValidQimei36(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// IsValidMachineID 检查是否为标准 8-4-4-4-12 GUID 格式。
func IsValidMachineID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func generateUUIDv4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		now := uint64(time.Now().UnixNano())
		return fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x",
			uint32(now>>32), uint16(now>>16), uint16(now)&0x0fff, uint16(now>>8)&0x0fff, now&0xffffffffffff)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
