package redact

import "testing"

func TestRedactSecrets_SecretsLookingLikePathsOrTestNamesStillRedacted(t *testing.T) {
	for _, in := range []string{
		"token=/aB3dEf7hIj9kLmN0pQrStUvWx",
		"secret: ~aB3dEf7hIj9kLmN0pQrStUvWx",
		"password /aB3dEf7hIj9kLmN0pQrStUvWx",
		"<password>/aB3dEf7hIj9kLmN0pQrStUvWx</password>",
		`TOKEN="/aB3dEf7hIj9kLmN0pQrStUvWx"`,
		"token=TestPassword12345678901234",
		"Server=db;uid=sa;pwd=Zk3qP9vX2mN7bR4tY8wL1cH6;",
		"PWD=Zk3qP9vX2mN7bR4tY8wL1cH6",
		"pass=Zk3qP9vX2mN7bR4tY8wL1cH6",
	} {
		if got := RedactSecrets(in); got == in {
			t.Errorf("RedactSecrets(%q) left the secret visible", in)
		}
	}
}
