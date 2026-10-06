//go:build !windows

package credentials

// nativeCred exists only on Windows; elsewhere it is reached only with a faked GOOS.
type nativeCred struct{}

func (nativeCred) Read(string) ([]byte, error) {
	return nil, &CredentialError{"Credential Manager is only available on Windows"}
}

func (nativeCred) Write(string, string, []byte, string) error {
	return &CredentialError{"Credential Manager is only available on Windows"}
}

func (nativeCred) Delete(string) bool { return false }
