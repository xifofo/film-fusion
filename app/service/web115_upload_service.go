package service

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
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

type web115RangeHashFunc func(signCheck string) (string, error)

func (s *Web115Service) RapidUploadWithP115ClientVersion(client *driver.Pan115Client, fileSize int64, fileName, dirID, preID, fileSHA1 string, readRangeHash web115RangeHashFunc) (*driver.UploadInitResp, error) {
	if client == nil {
		return nil, fmt.Errorf("115 client 为空")
	}
	cipher, err := ec115.NewEcdhCipher()
	if err != nil {
		return nil, err
	}
	if err := ensureWeb115UploadUserID(client); err != nil {
		return nil, err
	}
	uploadKey, err := s.GetUploadKeyWithClient(client)
	if err != nil {
		return nil, err
	}
	client.Userkey = uploadKey
	appVersion, err := s.GetCachedUploadAppVersion(client)
	if err != nil {
		return nil, err
	}
	fileSHA1 = strings.ToUpper(strings.TrimSpace(fileSHA1))

	target := "U_1_" + dirID
	fileSizeStr := strconv.FormatInt(fileSize, 10)
	form := url.Values{}
	form.Set("appversion", appVersion)
	form.Set("userid", strconv.FormatInt(client.UserID, 10))
	form.Set("userkey", client.Userkey)
	form.Set("filename", fileName)
	form.Set("filesize", fileSizeStr)
	form.Set("fileid", fileSHA1)
	form.Set("target", target)
	form.Set("sig", generateWeb115UploadSignature(client.Userkey, form.Get("userid"), fileSHA1, target))
	form.Set("topupload", "true")

	signKey, signVal := "", ""
	for {
		now := driver.Now()
		encodedToken, err := cipher.EncodeToken(now.ToInt64())
		if err != nil {
			return nil, err
		}

		form.Set("t", now.String())
		form.Set("token", generateWeb115UploadToken(client, appVersion, fileSHA1, preID, now.String(), fileSizeStr, signKey, signVal))
		setOptionalFormValue(form, "sign_key", signKey)
		setOptionalFormValue(form, "sign_val", signVal)

		encrypted, err := cipher.Encrypt([]byte(encodeWeb115UploadForm(form)))
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
		if readRangeHash == nil {
			return nil, fmt.Errorf("秒传需要二次验证 range hash，但未提供读取函数")
		}
		signKey = result.SignKey
		signVal, err = readRangeHash(result.SignCheck)
		if err != nil {
			return nil, fmt.Errorf("秒传二次验证 range hash 失败: %w", err)
		}
		signVal = strings.ToUpper(strings.TrimSpace(signVal))
		if signVal == "" {
			return nil, fmt.Errorf("秒传二次验证 range hash 为空")
		}
	}
}

func ensureWeb115UploadUserID(client *driver.Pan115Client) error {
	if client == nil {
		return fmt.Errorf("115 client 为空")
	}
	if client.UserID > 0 {
		return nil
	}
	if client.Client != nil {
		if credential, err := parse115Credential(client.Client.Header.Get("Cookie")); err == nil {
			if uid, err := parse115UID(credential.UID); err == nil {
				client.UserID = uid
				return nil
			}
		}
	}
	if ok, err := client.UploadAvailable(); !ok || err != nil {
		if err != nil {
			return fmt.Errorf("115 用户 UID 缺失，且自动获取上传信息失败: %w", err)
		}
		return fmt.Errorf("115 用户 UID 缺失，且自动获取上传信息不可用")
	}
	if client.UserID <= 0 {
		return fmt.Errorf("115 用户 UID 缺失")
	}
	return nil
}

func hashWeb115RangeSHA1(reader io.ReadSeeker, rangeSpec string) (string, error) {
	var start, end int64
	if _, err := fmt.Sscanf(rangeSpec, "%d-%d", &start, &end); err != nil {
		return "", err
	}
	if end < start {
		return "", fmt.Errorf("invalid range: %s", rangeSpec)
	}
	if _, err := reader.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha1.New()
	if _, err := io.CopyN(hash, reader, end-start+1); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(hash.Sum(nil))), nil
}

func setOptionalFormValue(form url.Values, key, value string) {
	if strings.TrimSpace(value) == "" {
		form.Del(key)
		return
	}
	form.Set(key, value)
}

func encodeWeb115UploadForm(form url.Values) string {
	keys := make([]string, 0, len(form))
	for key, values := range form {
		if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := make(url.Values, len(keys))
	for _, key := range keys {
		values.Set(key, form.Get(key))
	}
	return values.Encode()
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

func (s *Web115Service) GetUploadKeyWithClient(client *driver.Pan115Client) (string, error) {
	if client == nil {
		return "", fmt.Errorf("115 client 为空")
	}
	var result struct {
		State   bool             `json:"state"`
		Code    int              `json:"code"`
		Errno   driver.StringInt `json:"errno"`
		ErrNo   int              `json:"errNo"`
		Error   string           `json:"error"`
		Message string           `json:"message"`
		Msg     string           `json:"msg"`
		Data    struct {
			Userkey string `json:"userkey"`
		} `json:"data"`
	}

	resp, err := client.NewRequest().
		ForceContentType("application/json;charset=UTF-8").
		SetResult(&result).
		Get("https://proapi.115.com/android/2.0/user/upload_key")
	if err != nil {
		return "", err
	}
	raw := ""
	if resp != nil {
		raw = resp.String()
	}
	if !result.State || result.Code != 0 || int(result.Errno) != 0 || result.ErrNo != 0 {
		msg := firstNonEmptyString(result.Message, result.Msg, result.Error, raw)
		return "", fmt.Errorf("获取 115 upload_key 失败 code=%d errno=%d errNo=%d msg=%s", result.Code, int(result.Errno), result.ErrNo, msg)
	}
	uploadKey := strings.TrimSpace(result.Data.Userkey)
	if uploadKey == "" {
		return "", fmt.Errorf("获取 115 upload_key 失败：userkey 为空")
	}
	return uploadKey, nil
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

func generateWeb115UploadSignature(userKey, userID, fileID, target string) string {
	inner := sha1.Sum([]byte(userID + fileID + target + "0"))
	outer := sha1.New()
	outer.Write([]byte(userKey))
	outer.Write([]byte(hex.EncodeToString(inner[:])))
	outer.Write([]byte("000000"))
	return strings.ToUpper(hex.EncodeToString(outer.Sum(nil)))
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
