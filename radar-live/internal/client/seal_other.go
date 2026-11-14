//go:build !windows

package client

// Off Windows there is no per-user key store to rely on, so the key is only obfuscated (see savedkey.go).
func sealBytes(plain []byte) ([]byte, error)  { return sealAESGCM(plain) }
func openBytes(sealed []byte) ([]byte, error) { return openAESGCM(sealed) }
