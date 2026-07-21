package config

import "testing"

func TestValidateEmbySecurity(t *testing.T) {
	valid := EmbySecurityConfig{
		Enabled: true, WindowMinutes: 10, MaxFailuresPerAccountIP: 5,
		MaxFailuresPerIP: 20, BlockMinutes: 30,
		TrustedProxyCIDRs: []string{"127.0.0.1", "10.0.0.0/8", "2001:db8::/32"},
	}
	if err := ValidateEmbySecurity(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	invalid := valid
	invalid.TrustedProxyCIDRs = []string{"not-a-network"}
	if err := ValidateEmbySecurity(invalid); err == nil {
		t.Fatal("invalid trusted proxy was accepted")
	}

	invalid = valid
	invalid.WindowMinutes = 0
	if err := ValidateEmbySecurity(invalid); err == nil {
		t.Fatal("zero window was accepted")
	}
}
