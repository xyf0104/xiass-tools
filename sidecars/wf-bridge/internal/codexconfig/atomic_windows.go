//go:build windows

package codexconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8

	minimumWindowsSIDBytes = 8
	// FILE_ALL_ACCESS is not exported by x/sys/windows. This is its documented
	// value: all file-specific rights, standard rights, and SYNCHRONIZE.
	privateFileAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
)

var (
	atomicKernel32  = syscall.NewLazyDLL("kernel32.dll")
	moveFileExWProc = atomicKernel32.NewProc("MoveFileExW")
	errPrivateACL   = errors.New("could not establish private Windows access control for Codex configuration")
)

// preparePrivateFile runs before any API key or Provider header can be
// written. NTFS privacy is established by an explicit protected DACL, not by
// os.File.Chmod, which does not provide an equivalent Windows guarantee.
func preparePrivateFile(path string, _ fs.FileMode) error {
	return protectPrivatePath(path, false)
}

// finalizePrivateFile repeats the protected ACL set and strict verification
// after MoveFileExW. If a filesystem cannot retain or expose the expected
// descriptor, callers receive a failure rather than a successful write.
func finalizePrivateFile(path string, _ fs.FileMode) error {
	return protectPrivatePath(path, false)
}

// protectPrivateDirectory is reserved for XIASS Tools-owned backup roots,
// staged imports, and backup IDs. It is never applied to the user's complete
// Codex home because that directory can contain unrelated user files.
func protectPrivateDirectory(path string) error {
	return protectPrivatePath(path, true)
}

func protectPrivatePath(path string, directory bool) error {
	sid, storage, err := currentProcessUserSID()
	if err != nil {
		return errPrivateACL
	}
	var pinner runtime.Pinner
	pinner.Pin(&storage[0])
	defer pinner.Unpin()

	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: privateFileAllAccess,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil || acl == nil {
		return errPrivateACL
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return errPrivateACL
	}
	if err := verifyCurrentUserOnlyDACL(path, sid, directory); err != nil {
		return errPrivateACL
	}
	runtime.KeepAlive(storage)
	return nil
}

func currentProcessUserSID() (*windows.SID, []byte, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, nil, errors.New("current Windows user SID is unavailable")
	}
	length := user.User.Sid.Len()
	if length < minimumWindowsSIDBytes || length > 256 {
		return nil, nil, errors.New("current Windows user SID is invalid")
	}
	storage := make([]byte, length)
	sid := (*windows.SID)(unsafe.Pointer(&storage[0]))
	copyErr := windows.CopySid(uint32(len(storage)), sid, user.User.Sid)
	runtime.KeepAlive(user)
	if copyErr != nil || !sid.IsValid() {
		return nil, nil, errors.New("could not copy current Windows user SID")
	}
	return sid, storage, nil
}

func verifyCurrentUserOnlyDACL(path string, currentSID *windows.SID, directory bool) error {
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return errors.New("private DACL could not be read")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private DACL is not protected")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("private DACL does not contain one explicit user ACE")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil {
		return errors.New("private DACL ACE could not be read")
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != privateFileAllAccess {
		return errors.New("private DACL ACE is not a full-access user ACE")
	}
	expectedFlags := uint8(windows.NO_INHERITANCE)
	if directory {
		expectedFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if ace.Header.AceFlags != expectedFlags {
		return errors.New("private DACL ACE inheritance is invalid")
	}
	sidOffset := int(unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart))
	if int(ace.Header.AceSize) < sidOffset+minimumWindowsSIDBytes {
		return errors.New("private DACL ACE SID is invalid")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() || aceSID.Len() < minimumWindowsSIDBytes || int(ace.Header.AceSize) < sidOffset+aceSID.Len() || !aceSID.Equals(currentSID) {
		return errors.New("private DACL ACE does not match current user")
	}
	runtime.KeepAlive(descriptor)
	return nil
}

func replaceFileAtomic(source, destination string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileExWProc.Call(
		uintptr(unsafe.Pointer(sourcePtr)),
		uintptr(unsafe.Pointer(destinationPtr)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return fmt.Errorf("MoveFileExW: %w", callErr)
	}
	return nil
}
