/*
Copyright 2022 The cert-manager Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cert-manager/trust-manager/test/dummy"
)

func TestValidateAndSanitizePEMBundle(t *testing.T) {
	poisonComment := []byte{0xFF}
	// strippableComments is a list of things which should not be present in the output
	strippableText := [][]byte{
		[]byte(randomComment),
		poisonComment,
	}

	cases := map[string]struct {
		parts []string

		expectErr bool
	}{
		"valid bundle with all types of cert and no comments succeeds": {
			parts:     []string{dummy.TestCertificate1, dummy.TestCertificate2, dummy.TestCertificate3},
			expectErr: false,
		},
		"valid bundle with all types of cert and a random comment succeeds": {
			parts:     []string{dummy.TestCertificate1, randomComment, dummy.TestCertificate2, randomComment, dummy.TestCertificate3, randomComment},
			expectErr: false,
		},
		"valid bundle with all types of cert and a poison comment succeeds": {
			parts:     []string{dummy.TestCertificate1, string(poisonComment), dummy.TestCertificate2, randomComment, dummy.TestCertificate3, string(poisonComment)},
			expectErr: false,
		},
		"invalid bundle with a certificate with a header fails": {
			parts:     []string{dummy.TestCertificate1, dummyCertificateWithHeader, dummy.TestCertificate3},
			expectErr: true,
		},
		"invalid bundle with a certificate with invalid base64 fails": {
			parts:     []string{dummy.TestCertificate1, invalidCertificate, dummy.TestCertificate3},
			expectErr: true,
		},
		"invalid bundle containing a private key fails": {
			parts:     []string{dummy.TestCertificate1, privateKey},
			expectErr: true,
		},
		"invalid bundle with no certificates fails": {
			parts:     []string{"abc123"},
			expectErr: true,
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			inputBundle := []byte(strings.Join(test.parts, "\n"))

			sanitizedBundleBytes, err := ValidateAndSanitizePEMBundle(inputBundle)

			if test.expectErr != (err != nil) {
				t.Fatalf("ValidateAndSanitizePEMBundle: expectErr: %v | err: %v", test.expectErr, err)
			}

			if test.expectErr {
				return
			}

			if sanitizedBundleBytes == nil {
				t.Fatalf("got no error from ValidateAndSanitizePEMBundle but sanitizedBundle was nil")
			}

			for _, strippable := range strippableText {
				if bytes.Contains(sanitizedBundleBytes, strippable) {
					// can't print the comment since it could be an invalid string
					t.Errorf("expected sanitizedBundle to not contain a comment but it did")
				}
			}

			if !utf8.Valid(sanitizedBundleBytes) {
				t.Error("expected sanitizedBundle to be valid UTF-8 but it wasn't")
			}

			sanitizedBundle := string(sanitizedBundleBytes)

			if strings.HasSuffix(sanitizedBundle, "\n") {
				t.Errorf("expected sanitizedBundle not to end with a newline")
			}

			for _, line := range strings.Split(sanitizedBundle, "\n") {
				// Check that each "encapsulation boundary" (-----BEGIN/END <x>-----) is on its
				// own line. ("Encapsulation boundary" is apparently the name according to rfc7468)
				if !strings.HasPrefix(line, "-----") {
					continue
				}

				if !strings.HasSuffix(line, "-----") || strings.Count(line, "-----") != 2 {
					t.Errorf("invalid encapsulation boundary on line of certificate")
				}
			}
		})
	}
}

const randomComment = `some random commentary`

const dummyCertificateWithHeader = `-----BEGIN CERTIFICATE-----
My-Header: Abc123

MIIBVDCCAQagAwIBAgIRANcos1c12CXTCm8qyZto2LswBQYDK2VwMDAxFTATBgNV
BAoTDGNlcnQtbWFuYWdlcjEXMBUGA1UEAxMOY21jdC10ZXN0LXJvb3QwHhcNMjIx
MjA1MTYyMjQyWhcNMzIxMjAyMTYyMjQyWjAwMRUwEwYDVQQKEwxjZXJ0LW1hbmFn
ZXIxFzAVBgNVBAMTDmNtY3QtdGVzdC1yb290MCowBQYDK2VwAyEAWjVDu9495KZ4
g0YFJ94jggGrt3NFXWk6Mb51pCBylSyjNTAzMBIGA1UdEwEB/wQIMAYBAf8CAQMw
HQYDVR0OBBYEFFjCqrTVVpQRdBANLzgdKx3agWxIMAUGAytlcANBAEqb5PmhXtlA
gySihG5glByO5ZajFBNBIhjOF6+yfN1Bo5XjJ7bGwVIhGoRPHCtbvsnfuQ5ySz95
CFD1BItRnQM=
-----END CERTIFICATE-----`

// invalidCertificate has random characters manually replaced with "a"s; if we'd just randomly
// deleted characters to make the base64 invalid, then pem.Decode would skip over the block and we
// wouldn't ever try to parse it
const invalidCertificate = `-----BEGIN CERTIFICATE-----
MIIBVDCCAQagAwIBAgIRANcos1c12CXTCm8qyZto2LswBQYDK2VwMDAxFTATBgNV
BAoTDGNlcnQtbWFuYWdlcjEXMBUGA1UEAxMOY21jdC10ZXN0LHhcNMjIxaaaaaaa
MjA1MTYyMjQyWhcNMzIxMjAyMTYyMjQyWjAwMRUwEwYDVQQKEwxjZXJ0LW1hbmFn
xFzAVBgNVBAMTDmNtY3QtdGVzdC1yb290MCowBQYDK2VwAyEAWjVDu9495KZ4aaa
g0YFJ94jggGrt3NFXWk6Mb51pCBylSyjNTAzMBIGA1UdEwEB/wQIMAYBAf8CAQMw
VR0OBBYEFFjCqrTVVpQRdBANLzgdKx3agWxIMAUGAytlcAEqb5PmhXtlAaaaaaaa
gySihG5glByO5ZajFBNBIhjOF6+yfN1Bo5XjJ7bGwVIhGoRPHCtbvsnfuQ5ySz95
CFD1BItRnQM=
-----END CERTIFICATE-----`

const privateKey = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIHThSpdYMjW1k4K2r8RwhIGmknKrr0XKQLOJeL2fVoxToAoGCCqGSM49
AwEHoUQDQgAEoMocv03WW/kCmyYM7CN7Ge7J5NOhJOKUYjF15NRBevWbxd8GYsvj
9yCaAWu1mIQpIuWI4pXHU9s4V0FDlIKerQ==
-----END EC PRIVATE KEY-----`
