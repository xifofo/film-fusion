package handler

import (
	"film-fusion/app/logger"
	"film-fusion/app/organize"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"
)

type OrganizeHandler = organize.Handler

func NewOrganizeHandler(log *logger.Logger, mp *service.MoviePilotService, tmdb *service.TMDBService, downloads *service.Download115Service, emby *embyhelper.EmbyClient) *OrganizeHandler {
	return organize.NewHandler(log, mp, tmdb, downloads, emby)
}

type OrganizeCategoryConfigResult = organize.OrganizeCategoryConfigResult
type MediaLookupLocalStatus = organize.MediaLookupLocalStatus
type Organize115CookieRequest = organize.Organize115CookieRequest
type Organize115CookieGroup = organize.Organize115CookieGroup
type Organize115ItemResult = organize.Organize115ItemResult
type OrganizeVersionGroup = organize.OrganizeVersionGroup
type OrganizePreviewFolderRequest = organize.OrganizePreviewFolderRequest
type MediaLookupSearchRequest = organize.MediaLookupSearchRequest
type OrganizeUpgradeBinding = organize.OrganizeUpgradeBinding
type Organize115FolderContext = organize.Organize115FolderContext
type Organize115ShadowComparison = organize.Organize115ShadowComparison
type Organize115DirDebug = organize.Organize115DirDebug
type OrganizePreviewTaskListItem = organize.OrganizePreviewTaskListItem
type MediaLookupLocalStatusRequest = organize.MediaLookupLocalStatusRequest
type OrganizeUpgradeDirectory = organize.OrganizeUpgradeDirectory
type OrganizeUpgradeExisting = organize.OrganizeUpgradeExisting
type OrganizeUpgradeDecision = organize.OrganizeUpgradeDecision
type Organize115Request = organize.Organize115Request
type Organize115ShadowVariableComparison = organize.Organize115ShadowVariableComparison
type Organize115DirLookup = organize.Organize115DirLookup
type OrganizePreviewTaskAssignTMDBRequest = organize.OrganizePreviewTaskAssignTMDBRequest
type Organize115CookieResult = organize.Organize115CookieResult
type Organize115ShadowDifference = organize.Organize115ShadowDifference
type OrganizePreviewTaskCreateRequest = organize.OrganizePreviewTaskCreateRequest
type OrganizePreviewTmdbRef = organize.OrganizePreviewTmdbRef
type OrganizePreviewTmdbSeason = organize.OrganizePreviewTmdbSeason

func scoreMediaVersion(item Organize115ItemResult) (int, []string) {
	return organize.ScoreMediaVersion(item)
}
