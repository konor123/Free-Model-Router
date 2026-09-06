//go:build windows

package config

import (
	"errors"
	"syscall"
	"unsafe"
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	winErrorNotFound        = 1168 // ERROR_NOT_FOUND
)

// windowsCredentialBackend uses the per-user Windows Credential Manager. The
// adapter intentionally has no fallback behavior of its own; SecretStore
// chooses the protected file only when this backend is unavailable.
type windowsCredentialBackend struct{}

type credentialAttributeW struct {
	Keyword   *uint16
	Flags     uint32
	ValueSize uint32
	Value     *byte
}

// credentialW mirrors the Windows CREDENTIALW layout. Attribute fields are
// unused but retained so the pointer returned by CredReadW can be decoded
// safely on both 32-bit and 64-bit Windows.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        [8]byte
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         *credentialAttributeW
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	advapi32       = syscall.NewLazyDLL("advapi32.dll")
	procCredRead   = advapi32.NewProc("CredReadW")
	procCredWrite  = advapi32.NewProc("CredWriteW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

func newPlatformCredentialBackend() CredentialBackend { return windowsCredentialBackend{} }

func (windowsCredentialBackend) Get(key string) (string, error) {
	target, err := syscall.UTF16PtrFromString(credentialTarget(key))
	if err != nil {
		return "", ErrCredentialStoreUnavailable
	}
	var credential *credentialW
	result, _, callErr := procCredRead.Call(
		uintptr(unsafe.Pointer(target)),
		credTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if result == 0 {
		if isWindowsError(callErr, winErrorNotFound) {
			return "", ErrSecretNotFound
		}
		return "", ErrCredentialStoreUnavailable
	}
	if credential == nil {
		return "", ErrCredentialStoreUnavailable
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlob == nil || credential.CredentialBlobSize == 0 {
		return "", ErrSecretNotFound
	}
	blob := unsafe.Slice(credential.CredentialBlob, int(credential.CredentialBlobSize))
	value := string(append([]byte(nil), blob...))
	if value == "" {
		return "", ErrSecretNotFound
	}
	return value, nil
}

func (windowsCredentialBackend) Set(key, value string) error {
	target, err := syscall.UTF16PtrFromString(credentialTarget(key))
	if err != nil {
		return ErrCredentialStoreUnavailable
	}
	user, err := syscall.UTF16PtrFromString(ApplicationName)
	if err != nil {
		return ErrCredentialStoreUnavailable
	}
	blob := []byte(value)
	if len(blob) == 0 {
		return ErrCredentialStoreUnavailable
	}
	credential := credentialW{
		Type:               credTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}
	result, _, callErr := procCredWrite.Call(uintptr(unsafe.Pointer(&credential)), 0)
	if result == 0 {
		_ = callErr
		return ErrCredentialStoreUnavailable
	}
	return nil
}

func (windowsCredentialBackend) Delete(key string) error {
	target, err := syscall.UTF16PtrFromString(credentialTarget(key))
	if err != nil {
		return ErrCredentialStoreUnavailable
	}
	result, _, callErr := procCredDelete.Call(
		uintptr(unsafe.Pointer(target)),
		credTypeGeneric,
		0,
	)
	if result == 0 {
		if isWindowsError(callErr, winErrorNotFound) {
			return nil
		}
		return ErrCredentialStoreUnavailable
	}
	return nil
}

func credentialTarget(key string) string { return ApplicationName + "/" + key }

func isWindowsError(err error, code uintptr) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return uintptr(errno) == code
}
