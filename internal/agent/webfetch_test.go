package agent

import (
	"context"
	"testing"
)

func TestValidateWebFetchURLBlocksInternalTargets(t *testing.T) {
	ctx := context.Background()
	cases := []string{
		"http://127.0.0.1:8080/",
		"http://[::1]/",
		"http://10.0.0.5/",
		"http://172.16.0.5/",
		"http://192.168.1.10/",
		"http://169.254.169.254/latest/meta-data/",
		"http://0.0.0.0/",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if err := validateWebFetchURL(ctx, raw); err == nil {
				t.Fatalf("expected %s to be blocked", raw)
			}
		})
	}
}

func TestValidateWebFetchURLAllowsPublicIP(t *testing.T) {
	if err := validateWebFetchURL(context.Background(), "https://8.8.8.8/"); err != nil {
		t.Fatalf("public IP should be allowed: %v", err)
	}
}

func TestValidateWebFetchURLRejectsUserinfo(t *testing.T) {
	if err := validateWebFetchURL(context.Background(), "https://user:pass@example.com/"); err == nil {
		t.Fatal("userinfo URL should be rejected")
	}
}
