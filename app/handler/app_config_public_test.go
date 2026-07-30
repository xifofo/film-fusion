package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"film-fusion/app/config"

	"github.com/gin-gonic/gin"
)

func TestAppConfigHandlerGetPublic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		title      string
		subtitle   string
		formTitle  string
		formText   string
		background string
		footer     string
		icp        string
		police     string
		wantTitle  string
		want       string
		wantForm   string
		wantText   string
	}{
		{
			name:       "custom content",
			title:      "我的媒体中心",
			subtitle:   "简单的媒体管理工具",
			formTitle:  "欢迎进入",
			formText:   "请使用管理员账号登录",
			background: "https://example.com/background.jpg",
			footer:     "© 2026 我的媒体中心",
			icp:        "京ICP备12345678号",
			police:     "京公网安备 11000002000001号",
			wantTitle:  "我的媒体中心",
			want:       "简单的媒体管理工具",
			wantForm:   "欢迎进入",
			wantText:   "请使用管理员账号登录",
		},
		{
			name:      "default content",
			title:     " ",
			subtitle:  " ",
			wantTitle: config.DefaultLoginTitle,
			want:      config.DefaultLoginSubtitle,
			wantForm:  config.DefaultLoginFormTitle,
			wantText:  config.DefaultLoginFormSubtitle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Site: config.SiteConfig{
				LoginTitle:         tt.title,
				LoginSubtitle:      tt.subtitle,
				LoginFormTitle:     tt.formTitle,
				LoginFormSubtitle:  tt.formText,
				LoginBackgroundURL: tt.background,
				FooterText:         tt.footer,
				ICPNumber:          tt.icp,
				PoliceNumber:       tt.police,
			}}
			handler := NewAppConfigHandler(nil, cfg, nil, nil)
			handler.db = nil
			router := gin.New()
			router.GET("/api/public-config", handler.GetPublic)

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/public-config", nil)
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", recorder.Code)
			}

			var response struct {
				Code int `json:"code"`
				Data struct {
					LoginTitle         string   `json:"login_title"`
					LoginSubtitle      string   `json:"login_subtitle"`
					LoginFormTitle     string   `json:"login_form_title"`
					LoginFormSubtitle  string   `json:"login_form_subtitle"`
					LoginBackgroundURL string   `json:"login_background_url"`
					LoginBackgrounds   []string `json:"login_backgrounds"`
					BackgroundSource   string   `json:"login_background_source"`
					BackgroundMode     string   `json:"login_background_mode"`
					BackgroundInterval int      `json:"login_background_interval"`
					FooterText         string   `json:"footer_text"`
					ICPNumber          string   `json:"icp_number"`
					PoliceNumber       string   `json:"police_number"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Code != 0 {
				t.Fatalf("expected response code 0, got %d", response.Code)
			}
			if response.Data.LoginTitle != tt.wantTitle {
				t.Fatalf("expected title %q, got %q", tt.wantTitle, response.Data.LoginTitle)
			}
			if response.Data.LoginSubtitle != tt.want {
				t.Fatalf("expected subtitle %q, got %q", tt.want, response.Data.LoginSubtitle)
			}
			if response.Data.LoginFormTitle != tt.wantForm {
				t.Fatalf("expected form title %q, got %q", tt.wantForm, response.Data.LoginFormTitle)
			}
			if response.Data.LoginFormSubtitle != tt.wantText {
				t.Fatalf("expected form subtitle %q, got %q", tt.wantText, response.Data.LoginFormSubtitle)
			}
			if response.Data.LoginBackgroundURL != tt.background {
				t.Fatalf("expected background %q, got %q", tt.background, response.Data.LoginBackgroundURL)
			}
			if response.Data.BackgroundSource != config.DefaultLoginBackgroundSource {
				t.Fatalf("expected default source %q, got %q", config.DefaultLoginBackgroundSource, response.Data.BackgroundSource)
			}
			if response.Data.BackgroundMode != config.DefaultLoginBackgroundMode {
				t.Fatalf("expected default mode %q, got %q", config.DefaultLoginBackgroundMode, response.Data.BackgroundMode)
			}
			if response.Data.BackgroundInterval != config.DefaultLoginBackgroundInterval {
				t.Fatalf("expected default interval %d, got %d", config.DefaultLoginBackgroundInterval, response.Data.BackgroundInterval)
			}
			if tt.background != "" {
				if len(response.Data.LoginBackgrounds) != 1 || response.Data.LoginBackgrounds[0] != tt.background {
					t.Fatalf("expected public backgrounds [%q], got %v", tt.background, response.Data.LoginBackgrounds)
				}
			} else if len(response.Data.LoginBackgrounds) != 0 {
				t.Fatalf("expected no public backgrounds, got %v", response.Data.LoginBackgrounds)
			}
			if response.Data.FooterText != tt.footer {
				t.Fatalf("expected footer %q, got %q", tt.footer, response.Data.FooterText)
			}
			if response.Data.ICPNumber != tt.icp {
				t.Fatalf("expected ICP number %q, got %q", tt.icp, response.Data.ICPNumber)
			}
			if response.Data.PoliceNumber != tt.police {
				t.Fatalf("expected police number %q, got %q", tt.police, response.Data.PoliceNumber)
			}
		})
	}
}
