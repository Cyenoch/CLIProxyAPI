// Package devin implements Devin authentication and shared client metadata.
package devin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	clientVersion          = "3000.10.21"
	devinFingerprintHexLen = 732
)

// GenerateDeviceFingerprint generates a 732-character hex device fingerprint.
// When seed is empty, it generates a cryptographically random 732-character hex string per request.
// When seed is provided, it derives a deterministic 732-character hex fingerprint.
func GenerateDeviceFingerprint(seed string) string {
	if seed == "" {
		var b [devinFingerprintHexLen / 2]byte
		if _, err := rand.Read(b[:]); err == nil {
			return hex.EncodeToString(b[:])
		}
		seed = uuid.New().String()
	}
	var sb strings.Builder
	counter := 0
	for sb.Len() < devinFingerprintHexLen {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, counter)))
		sb.WriteString(hex.EncodeToString(h[:]))
		counter++
	}
	return sb.String()[:devinFingerprintHexLen]
}

// BuildClientMetadataBytes constructs the Devin CLI identity shared by chat and catalog requests.
func BuildClientMetadataBytes(sessionToken, userJWT, deviceSeed, osName string) []byte {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken != "" && !strings.HasPrefix(sessionToken, devinTokenPrefix) {
		sessionToken = devinTokenPrefix + sessionToken
	}
	metadata := buildClientMetadata(sessionToken, GenerateDeviceFingerprint(deviceSeed), osName, "devin-cli")
	if userJWT != "" {
		metadata = protowire.AppendTag(metadata, 21, protowire.BytesType)
		metadata = protowire.AppendString(metadata, userJWT)
	}
	metadata = protowire.AppendTag(metadata, 28, protowire.BytesType)
	return protowire.AppendString(metadata, "chisel")
}

func buildClientMetadata(sessionToken, deviceFingerprint, osName, application string) []byte {
	if osName == "" {
		osName = runtime.GOOS
	}
	var f1Bytes []byte
	f1Bytes = protowire.AppendTag(f1Bytes, 1, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, application)

	f1Bytes = protowire.AppendTag(f1Bytes, 2, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, clientVersion)

	f1Bytes = protowire.AppendTag(f1Bytes, 3, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, sessionToken)

	f1Bytes = protowire.AppendTag(f1Bytes, 4, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "en")

	f1Bytes = protowire.AppendTag(f1Bytes, 5, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, osName)

	f1Bytes = protowire.AppendTag(f1Bytes, 7, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, clientVersion)

	f1Bytes = protowire.AppendTag(f1Bytes, 12, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, "chisel")

	f1Bytes = protowire.AppendTag(f1Bytes, 31, protowire.BytesType)
	f1Bytes = protowire.AppendString(f1Bytes, deviceFingerprint)

	return f1Bytes
}
