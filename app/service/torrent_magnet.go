package service

import (
	"bytes"
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

const maxAutomationTorrentBytes = 8 << 20

// Keep offsets into the original bytes: re-encoding info can change its hash.
type torrentValue struct {
	kind       byte
	start, end int
	text       []byte
	number     int64
	list       []torrentValue
	dict       map[string]torrentValue
}

type torrentDecoder struct {
	data   []byte
	pos    int
	values int
}

func (d *torrentDecoder) read(depth int) (torrentValue, error) {
	v := torrentValue{start: d.pos}
	d.values++
	if depth > 64 || d.values > 100000 || d.pos >= len(d.data) {
		return v, errors.New("种子结构不完整或超过解析限制")
	}
	v.kind = d.data[d.pos]
	d.pos++
	switch v.kind {
	case 'i':
		end := bytes.IndexByte(d.data[d.pos:], 'e')
		if end < 1 {
			return v, errors.New("种子包含无效整数")
		}
		raw := string(d.data[d.pos : d.pos+end])
		number, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || strconv.FormatInt(number, 10) != raw {
			return v, errors.New("种子包含无效整数")
		}
		v.number = number
		d.pos += end + 1
	case 'l', 'd':
		if v.kind == 'd' {
			v.dict = map[string]torrentValue{}
		}
		for {
			if d.pos >= len(d.data) {
				return v, errors.New("种子列表或字典未结束")
			}
			if d.data[d.pos] == 'e' {
				d.pos++
				break
			}
			child, err := d.read(depth + 1)
			if err != nil {
				return v, err
			}
			if v.kind == 'l' {
				v.list = append(v.list, child)
				continue
			}
			if child.kind != 's' {
				return v, errors.New("种子字典键必须是字符串")
			}
			key := string(child.text)
			if _, exists := v.dict[key]; exists {
				return v, errors.New("种子字典包含重复键")
			}
			value, err := d.read(depth + 1)
			if err != nil {
				return v, err
			}
			v.dict[key] = value
		}
	default:
		d.pos--
		colon := bytes.IndexByte(d.data[d.pos:], ':')
		if colon < 1 || colon > 8 {
			return v, errors.New("种子包含无效字符串长度")
		}
		raw := string(d.data[d.pos : d.pos+colon])
		length, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || strconv.FormatUint(length, 10) != raw {
			return v, errors.New("种子包含无效字符串长度")
		}
		d.pos += colon + 1
		if length > uint64(len(d.data)-d.pos) {
			return v, errors.New("种子字符串内容被截断")
		}
		v.kind = 's'
		v.text = d.data[d.pos : d.pos+int(length)]
		d.pos += int(length)
	}
	v.end = d.pos
	return v, nil
}

func torrentText(v torrentValue) string {
	if v.kind != 's' {
		return ""
	}
	return strings.ToValidUTF8(string(v.text), "�")
}

func parseTorrentMagnet(data []byte, includeTrackers bool) (map[string]any, error) {
	if len(data) == 0 || len(data) > maxAutomationTorrentBytes {
		return nil, errors.New("种子文件为空或超过 8 MiB")
	}
	d := torrentDecoder{data: data}
	root, err := d.read(0)
	if err != nil {
		return nil, fmt.Errorf("无法解析种子文件（需要 .torrent 内容，不能是网页）: %w", err)
	}
	if root.kind != 'd' || d.pos != len(data) {
		return nil, errors.New("种子必须是完整的 Bencode 字典，不能包含尾随内容")
	}
	info, ok := root.dict["info"]
	if !ok || info.kind != 'd' {
		return nil, errors.New("种子缺少 info 字典")
	}
	version := "v1"
	if meta, exists := info.dict["meta version"]; exists {
		if meta.kind != 'i' || meta.number != 2 {
			return nil, errors.New("不支持该种子的 meta version")
		}
		if _, hasPieces := info.dict["pieces"]; !hasPieces {
			return nil, errors.New("暂不支持纯 BitTorrent v2 种子；请使用 v1 或 v1/v2 混合种子")
		}
		version = "hybrid"
	}
	name := torrentText(info.dict["name.utf-8"])
	if name == "" {
		name = torrentText(info.dict["name"])
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("种子缺少文件名称")
	}
	private := false
	if value, exists := info.dict["private"]; exists {
		if value.kind != 'i' || (value.number != 0 && value.number != 1) {
			return nil, errors.New("种子 private 标记无效")
		}
		private = value.number == 1
	}
	files := make([]map[string]any, 0)
	var total int64
	appendFile := func(path string, size torrentValue) error {
		if size.kind != 'i' || size.number < 0 || size.number > math.MaxInt64-total {
			return errors.New("种子文件大小无效或总大小溢出")
		}
		if len(files) >= 10000 {
			return errors.New("种子文件数量超过 10000")
		}
		files = append(files, map[string]any{"index": len(files), "path": path, "size": size.number})
		total += size.number
		return nil
	}
	length, single := info.dict["length"]
	list, multi := info.dict["files"]
	if single == multi {
		return nil, errors.New("种子必须包含单文件 length 或多文件 files")
	}
	if single {
		err = appendFile(name, length)
	} else {
		if list.kind != 'l' || len(list.list) == 0 {
			return nil, errors.New("种子文件列表为空或格式无效")
		}
		for _, file := range list.list {
			if file.kind != 'd' {
				return nil, errors.New("种子文件条目必须是字典")
			}
			path, exists := file.dict["path.utf-8"]
			if !exists {
				path = file.dict["path"]
			}
			if path.kind != 'l' || len(path.list) == 0 {
				return nil, errors.New("种子文件路径无效")
			}
			parts := make([]string, 0, len(path.list)+1)
			parts = append(parts, name)
			for _, part := range path.list {
				text := torrentText(part)
				if text == "" {
					return nil, errors.New("种子文件路径包含空值或非字符串")
				}
				parts = append(parts, text)
			}
			if err = appendFile(strings.Join(parts, "/"), file.dict["length"]); err != nil {
				return nil, err
			}
		}
	}
	if err != nil {
		return nil, err
	}
	pieceLength, pieces := info.dict["piece length"], info.dict["pieces"]
	if pieceLength.kind != 'i' || pieceLength.number <= 0 || pieces.kind != 's' || len(pieces.text)%20 != 0 {
		return nil, errors.New("种子 pieces 或 piece length 无效")
	}
	expectedPieces := total / pieceLength.number
	if total%pieceLength.number != 0 {
		expectedPieces++
	}
	if expectedPieces != int64(len(pieces.text)/20) {
		return nil, errors.New("种子分片数量与文件大小不一致")
	}
	trackers := []string{}
	seen := map[string]bool{}
	addTracker := func(value torrentValue) {
		text := torrentText(value)
		parsed, err := url.Parse(text)
		if err != nil || parsed.Hostname() == "" || len(text) > 8192 || seen[text] || len(trackers) >= 128 {
			return
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "udp", "ws", "wss":
			seen[text] = true
			trackers = append(trackers, text)
		}
	}
	addTracker(root.dict["announce"])
	for _, tier := range root.dict["announce-list"].list {
		for _, tracker := range tier.list {
			addTracker(tracker)
		}
	}
	hash := sha1.Sum(data[info.start:info.end])
	infoHash := hex.EncodeToString(hash[:])
	query := url.Values{"xt": {"urn:btih:" + infoHash}, "dn": {name}}
	if includeTrackers {
		query["tr"] = trackers
	}
	warnings := []string{}
	if private {
		warnings = append(warnings, "这是私有种子；转换磁链不代表下载器能取得元数据或访问私有 Tracker。")
	}
	return map[string]any{
		"magnet": "magnet:?" + query.Encode(), "info_hash": infoHash,
		"torrent_name": name, "file_size": total, "file_count": len(files), "files": files,
		"private": private, "trackers": trackers, "trackers_included": includeTrackers,
		"torrent_version": version, "metadata_available": true, "warnings": warnings,
	}, nil
}

func passthroughTorrentMagnet(value string, includeTrackers bool) (map[string]any, error) {
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "magnet") || parsed.Host != "" || parsed.Path != "" || parsed.Fragment != "" {
		return nil, errors.New("磁力链接格式无效")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, errors.New("磁力链接参数无效")
	}
	infoHash := ""
	for _, xt := range query["xt"] {
		if !strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
			continue
		}
		raw := xt[len("urn:btih:"):]
		var decoded []byte
		if len(raw) == 32 {
			decoded, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(raw))
		} else {
			decoded, err = hex.DecodeString(raw)
		}
		if err != nil || len(decoded) != sha1.Size {
			return nil, errors.New("磁力链接的 BT info_hash 无效")
		}
		hash := hex.EncodeToString(decoded)
		if infoHash != "" && infoHash != hash {
			return nil, errors.New("磁力链接包含相互冲突的 BT info_hash")
		}
		infoHash = hash
	}
	if infoHash == "" {
		return nil, errors.New("磁力链接缺少 v1 BT info_hash；暂不支持纯 v2 磁链")
	}
	trackers := append([]string{}, query["tr"]...)
	if !includeTrackers {
		query.Del("tr")
	}
	return map[string]any{
		"magnet": "magnet:?" + query.Encode(), "info_hash": infoHash,
		"torrent_name": query.Get("dn"), "trackers": trackers, "trackers_included": includeTrackers,
		"metadata_available": false, "warnings": []string{"输入已是磁力链接，未获取种子文件；文件列表、大小和私有标记未知。"},
	}, nil
}
