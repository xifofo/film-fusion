package config

import "testing"

func TestValidateWebhook(t *testing.T) {
	tests := []struct {
		name    string
		config  WebhookConfig
		wantErr bool
	}{
		{name: "disabled without token"},
		{
			name: "enabled with short token",
			config: WebhookConfig{CloudDrive2: CloudDrive2WebhookConfig{
				Enabled: true, Token: "too-short",
			}},
			wantErr: true,
		},
		{
			name: "enabled with strong token",
			config: WebhookConfig{CloudDrive2: CloudDrive2WebhookConfig{
				Enabled: true, Token: "0123456789abcdef0123456789abcdef",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWebhook(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateWebhook() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
