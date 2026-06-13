package service

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"

	ec115 "github.com/SheltonZhu/115driver/pkg/crypto/ec115"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	web115UploadAppVersionCacheKey = "upload_app_version"
	web115UploadAppVersionCacheTTL = 7 * 24 * time.Hour
	web115UploadMD5Salt            = "Qclm8MGWUv59TnrR0XPg"
)

func (s *Web115Service) RapidUploadWithP115ClientVersion(client *driver.Pan115Client, fileSize int64, fileName, dirID, preID, fileSHA1 string, reader io.ReadSeeker) (*driver.UploadInitResp, error) {
	if client == nil {
		return nil, fmt.Errorf("115 client 为空")
	}
	cipher, err := ec115.NewEcdhCipher()
	if err != nil {
		return nil, err
	}
	if ok, err := client.UploadAvailable(); !ok || err != nil {
		return nil, err
	}
	appVersion, err := s.GetCachedUploadAppVersion(client)
	if err != nil {
		return nil, err
	}

	target := "U_1_" + dirID
	fileSizeStr := strconv.FormatInt(fileSize, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVersion)
	form.Set("userid", strconv.FormatInt(client.UserID, 10))
	form.Set("userkey", client.Userkey)
	form.Set("filename", fileName)
	form.Set("filesize", fileSizeStr)
	form.Set("fileid", fileSHA1)
	form.Set("target", target)
	form.Set("sign_key", "")
	form.Set("sign_val", "")
	form.Set("sig", client.GenerateSignature(fileSHA1, target))
	form.Set("topupload", "true")

	signKey, signVal := "", ""
	for {
		now := driver.NowMilli()
		encodedToken, err := cipher.EncodeToken(now.ToInt64())
		if err != nil {
			return nil, err
		}

		form.Set("t", now.String())
		form.Set("token", generateWeb115UploadToken(client, appVersion, fileSHA1, preID, now.String(), fileSizeStr, signKey, signVal))
		form.Set("sign_key", signKey)
		form.Set("sign_val", signVal)

		encrypted, err := cipher.Encrypt([]byte(form.Encode()))
		if err != nil {
			return nil, err
		}
		result, err := s.postWeb115UploadInit(client, cipher, appVersion, encodedToken, encrypted)
		if err != nil {
			return nil, err
		}
		if result.Status != 7 {
			result.SHA1 = fileSHA1
			return result, nil
		}
		signKey = result.SignKey
		signVal, err = client.UploadDigestRange(reader, result.SignCheck)
		if err != nil {
			return nil, fmt.Errorf("秒传二次验证 range hash 失败: %w", err)
		}
	}
}

func (s *Web115Service) postWeb115UploadInit(client *driver.Pan115Client, cipher *ec115.EcdhCipher, appVersion, encodedToken string, encrypted []byte) (*driver.UploadInitResp, error) {
	req := client.NewRequest().
		SetQueryParam("k_ec", encodedToken).
		SetBody(encrypted).
		SetHeaderVerbatim("Content-Type", "application/x-www-form-urlencoded").
		SetHeaderVerbatim("User-Agent", web115UploadUserAgent(appVersion)).
		SetDoNotParseResponse(true)
	resp, err := req.Post(driver.ApiUploadInit)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.RawBody() == nil {
		return nil, fmt.Errorf("秒传初始化响应为空")
	}
	body := resp.RawBody()
	defer body.Close()

	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	return parseWeb115UploadInitResponse(cipher, bodyBytes)
}

func (s *Web115Service) GetCachedUploadAppVersion(client *driver.Pan115Client) (string, error) {
	now := time.Now()
	var cache model.Web115AppVersionCache
	if database.DB != nil {
		err := database.DB.Where("cache_key = ?", web115UploadAppVersionCacheKey).First(&cache).Error
		if err == nil && strings.TrimSpace(cache.Version) != "" && cache.ExpiresAt.After(now) {
			return cache.Version, nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
	}

	version, platform, rawJSON, err := s.FetchOfficialUploadAppVersion(client)
	if err != nil {
		if strings.TrimSpace(cache.Version) != "" {
			if s != nil && s.logger != nil {
				s.logger.Warnf("刷新 115 appversion 失败，继续使用过期缓存 version=%s err=%v", cache.Version, err)
			}
			return cache.Version, nil
		}
		return "", err
	}
	if database.DB != nil {
		record := model.Web115AppVersionCache{
			CacheKey:  web115UploadAppVersionCacheKey,
			Platform:  platform,
			Version:   version,
			RawJSON:   rawJSON,
			FetchedAt: now,
			ExpiresAt: now.Add(web115UploadAppVersionCacheTTL),
		}
		if err := database.DB.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "cache_key"}},
			DoUpdates: clause.Assignments(map[string]any{
				"platform":   record.Platform,
				"version":    record.Version,
				"raw_json":   record.RawJSON,
				"fetched_at": record.FetchedAt,
				"expires_at": record.ExpiresAt,
				"updated_at": now,
			}),
		}).Create(&record).Error; err != nil {
			if s != nil && s.logger != nil {
				s.logger.Warnf("写入 115 appversion 缓存失败，继续使用本次拉取版本 version=%s err=%v", version, err)
			}
		}
	}
	return version, nil
}

type web115OfficialVersionResp struct {
	State   bool                             `json:"state"`
	ErrCode int                              `json:"err_code"`
	Error   string                           `json:"error"`
	Data    map[string]web115OfficialVersion `json:"data"`
}

type web115OfficialVersion struct {
	VersionCode string `json:"version_code"`
	CreatedTime int64  `json:"created_time"`
	VersionURL  string `json:"version_url"`
}

func (s *Web115Service) FetchOfficialUploadAppVersion(client *driver.Pan115Client) (version, platform, rawJSON string, err error) {
	if client == nil {
		return "", "", "", fmt.Errorf("115 client 为空")
	}
	var result web115OfficialVersionResp
	resp, err := client.NewRequest().
		SetResult(&result).
		ForceContentType("application/json;charset=UTF-8").
		Get(driver.ApiGetVersion)
	if err != nil {
		return "", "", "", err
	}
	rawJSON = ""
	if resp != nil {
		rawJSON = resp.String()
	}
	if !result.State {
		return "", "", rawJSON, fmt.Errorf("获取 115 官方 appversion 失败 err_code=%d error=%s", result.ErrCode, result.Error)
	}
	for _, candidate := range []string{"android", "win", "win64", "mac", "mac_arm", "linux", "window_115"} {
		item, ok := result.Data[candidate]
		if !ok || strings.TrimSpace(item.VersionCode) == "" {
			continue
		}
		return strings.TrimSpace(item.VersionCode), candidate, rawJSON, nil
	}
	return "", "", rawJSON, fmt.Errorf("获取 115 官方 appversion 失败：版本列表为空")
}

func web115UploadUserAgent(appVersion string) string {
	version := strings.TrimSpace(appVersion)
	return fmt.Sprintf("Mozilla/5.0 115disk/%s 115Browser/%s 115wangpan_android/%s", version, version, version)
}

func generateWeb115UploadToken(client *driver.Pan115Client, appVersion, fileID, preID, timeStamp, fileSize, signKey, signVal string) string {
	userID := strconv.FormatInt(client.UserID, 10)
	userIDMD5 := md5.Sum([]byte(userID))
	tokenMD5 := md5.Sum([]byte(web115UploadMD5Salt + fileID + fileSize + signKey + signVal + userID + timeStamp + hex.EncodeToString(userIDMD5[:]) + appVersion))
	return hex.EncodeToString(tokenMD5[:])
}

func parseWeb115UploadInitResponse(cipher *ec115.EcdhCipher, bodyBytes []byte) (*driver.UploadInitResp, error) {
	decrypted, err := cipher.Decrypt(bodyBytes)
	if err != nil {
		return nil, err
	}
	var result driver.UploadInitResp
	if err := json.Unmarshal(decrypted, &result); err != nil {
		return nil, fmt.Errorf("解析秒传初始化响应失败: %w", err)
	}
	if err := result.Err(string(decrypted)); err != nil {
		return nil, fmt.Errorf("秒传初始化失败 statuscode=%d statusmsg=%s: %w", result.ErrorCode, result.ErrorMsg, err)
	}
	return &result, nil
}
