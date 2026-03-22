package tls

import "testing"

func boolPtr(v bool) *bool {
	return &v
}

func TestClientConfigTLSConfigAutoEnable(t *testing.T) {
	cfg := &ClientConfig{
		ServerName: "redis.example.com",
	}

	tlsCfg, err := cfg.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg == nil {
		t.Fatal("expected tls config when tls fields are present")
	}
	if tlsCfg.ServerName != "redis.example.com" {
		t.Fatalf("expected server name to be preserved, got %q", tlsCfg.ServerName)
	}
}

func TestClientConfigTLSConfigExplicitDisable(t *testing.T) {
	cfg := &ClientConfig{
		UseTLS:     boolPtr(false),
		ServerName: "redis.example.com",
	}

	tlsCfg, err := cfg.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg != nil {
		t.Fatal("expected nil tls config when use_tls is explicitly false")
	}
}
