package organize

import (
	"regexp"
)

var (
	previewTaskTMDBIDRegexp             = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	previewTaskTMDBMarkerRegexp         = regexp.MustCompile(`(?i)[\s._-]*\{tmdb(?:id)?-[0-9]+\}`)
	defaultFilenameFallbackRegexp       = regexp.MustCompile(`.* - (.*)-.*`)
	duplicateMovieTitleYearPrefixRegexp = regexp.MustCompile(`^\s*.+?[\(（]\s*(?:19|20)\d{2}\s*[\)）]\s+[-–—]\s+(.+?)\s*$`)
	movieReleaseYearRegexp              = regexp.MustCompile(`(?:^|[^0-9])(?:19|20)\d{2}(?:[^0-9]|$)`)
	movieAudioChannelRegexp             = regexp.MustCompile(`(?i)\b(MA|AAC|DDP|DD|AC3|EAC3)([257])\.([01])\b`)
	episodeLeadingFilenameRegexp        = regexp.MustCompile(`(?i)^s\d{1,2}[\s._-]*e\d{1,3}(?:\s*[-~]\s*e?\d{1,3}|e\d{1,3})?(?:$|[^0-9])`)
	multiEpisodeRenameRegexp            = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(s(\d{1,2})e(\d{1,3})(?:\s*[-~]\s*e?(\d{1,3})|e(\d{1,3})))(?:$|[^a-z0-9])`)
	seasonEpisodeRegexp                 = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[\s._-]*e(\d{1,3})(?:[^0-9]|$)`)
	versionTrackSeasonEpisodeRegexp     = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(s(\d{1,2})[\s._-]*e(\d{1,3})(?:\s*[-~]\s*e?\d{1,3}|e\d{1,3})?)(?:$|[^0-9])`)
	seasonEpisodeXRegexp                = regexp.MustCompile(`(?i)(?:^|[^0-9])(\d{1,2})x(\d{1,3})(?:[^0-9]|$)`)
	chineseSeasonEpisodeRegexp          = regexp.MustCompile(`第\s*(\d{1,2})\s*季.*?第\s*(\d{1,3})\s*[集话話]`)
	episodeOnlyRegexp                   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:e|ep|episode)[\s._-]*(\d{1,3})(?:[^0-9]|$)`)
	chineseEpisodeRegexp                = regexp.MustCompile(`第\s*(\d{1,3})\s*[集话話]`)
	seasonOnlyRegexp                    = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:season[\s._-]*|s)(\d{1,2})(?:[^0-9]|$)|第\s*(\d{1,2})\s*季`)
	lowQualityReleaseRegexp             = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:cam|hdcam|telesync|ts|tc)(?:$|[^a-z0-9])`)
	subtitleQualifierSuffixRegexp       = regexp.MustCompile(`(?i)(?:[._ -]+(?:zh(?:[-_](?:cn|tw|hans|hant))?|chs|cht|chi|zho|cn|tw|sc|tc|en|eng|ja|jpn|ko|kor|forced|default|sdh|cc|简体|繁体|简中|繁中|双语|中字))+$`)
	transferNameTokenRegexp             = regexp.MustCompile(`[[:alnum:]]+`)
)
