package organize

import (
	"fmt"
	"strings"

	"film-fusion/app/service"
)

func buildOrganize115ShadowComparison(recognitionRaw, transferRaw map[string]any) *Organize115ShadowComparison {
	comparison := &Organize115ShadowComparison{
		Differences: make([]Organize115ShadowDifference, 0),
	}
	recognition, _, _, hasRecognition := service.MediaRecognitionShadowFromRaw(recognitionRaw)
	if hasRecognition {
		recognitionCopy := recognition
		comparison.Recognition = &recognitionCopy
		for _, difference := range recognition.Differences {
			comparison.Differences = append(comparison.Differences, Organize115ShadowDifference{
				Stage: "recognition", Field: difference.Field, Label: difference.Label,
				MoviePilot: difference.MoviePilot, Local: difference.Local,
			})
		}
	}
	transfer, hasTransfer := service.MediaTransferShadowFromRaw(transferRaw)
	if hasTransfer {
		transferCopy := transfer
		transferCopy.MoviePilot = dedupeConsecutiveTransferTags(transferCopy.MoviePilot)
		transferCopy.Local = dedupeConsecutiveTransferTags(transferCopy.Local)
		comparison.Transfer = &transferCopy
		if strings.TrimSpace(transferCopy.MoviePilot) != strings.TrimSpace(transferCopy.Local) {
			comparison.Differences = append(comparison.Differences, Organize115ShadowDifference{
				Stage: "transfer", Field: "transfer_name", Label: "重命名",
				MoviePilot: transferCopy.MoviePilot, Local: transferCopy.Local,
			})
		}
	}
	if !hasRecognition && !hasTransfer {
		return nil
	}
	refreshOrganize115ShadowStatus(comparison)
	return comparison
}

func refreshOrganize115ShadowStatus(comparison *Organize115ShadowComparison) {
	if comparison == nil {
		return
	}
	comparison.Status = service.MediaRecognitionShadowMatched
	for _, status := range []string{
		shadowComparisonStatus(comparison.Recognition),
		shadowTransferStatus(comparison.Transfer),
	} {
		if status != "" && status != service.MediaRecognitionShadowMatched && status != service.MediaRecognitionShadowDifferent {
			comparison.Status = status
			comparison.Matched = false
			return
		}
	}
	if strings.TrimSpace(comparison.LocalTargetError) != "" {
		comparison.Status = service.MediaRecognitionShadowLocalError
		comparison.Matched = false
		return
	}
	if len(comparison.Differences) > 0 {
		comparison.Status = service.MediaRecognitionShadowDifferent
		comparison.Matched = false
		return
	}
	comparison.Matched = true
}

func shadowComparisonStatus(comparison *service.MediaRecognitionShadowComparison) string {
	if comparison == nil {
		return ""
	}
	return comparison.Status
}

func shadowTransferStatus(comparison *service.MediaTransferShadowComparison) string {
	if comparison == nil {
		return ""
	}
	return comparison.Status
}

func (h *Service) completeOrganize115ShadowTargetComparison(
	comparison *Organize115ShadowComparison,
	input organize115ShadowTargetInput,
) {
	if comparison == nil {
		return
	}
	comparison.MoviePilotTargetPath = strings.TrimSpace(input.PrimaryTargetPath)
	_, localInfo, localRaw, hasRecognition := service.MediaRecognitionShadowFromRaw(input.RecognitionRaw)
	transfer, hasTransfer := service.MediaTransferShadowFromRaw(input.TransferRaw)
	localTransferName := dedupeConsecutiveTransferTags(transfer.Local)
	if !hasRecognition || localInfo == nil || !hasTransfer || strings.TrimSpace(localTransferName) == "" || transfer.LocalError != "" {
		refreshOrganize115ShadowStatus(comparison)
		return
	}

	localCategory := strings.TrimSpace(input.CategoryOverride)
	if localCategory == "" {
		localCategory = strings.TrimSpace(localInfo.Category)
	}
	if localCategory == "" {
		localCategory = service.SelectMoviePilotCategory(localInfo.MediaType, *localInfo, input.CategoryConfig)
	}
	localInfo.Category = localCategory
	categoryForPath := localCategory
	if !input.ClassifyByCategory {
		categoryForPath = ""
	}
	legacyOriginalName := strings.TrimSpace(input.OriginalName)
	if shouldStripDuplicateMovieTitleYearPrefix("", localInfo.MediaType, localCategory) {
		legacyOriginalName = stripDuplicateMovieTitleYearPrefix(legacyOriginalName)
	}

	primaryRenameInput := service.MediaRenameInput{
		Info: input.PrimaryInfo, MediaType: input.PrimaryMediaType, Category: input.PrimaryCategory,
		RecognizeName: input.RecognizeName, TransferName: input.PrimaryTransferName, OriginalName: input.OriginalName,
		LegacyOriginalName: input.LegacyOriginalName,
		RecognitionRaw:     input.RecognitionRaw, TransferRaw: input.TransferRaw,
	}
	localRenameInput := service.MediaRenameInput{
		Info: *localInfo, MediaType: localInfo.MediaType, Category: localCategory,
		RecognizeName: input.RecognizeName, TransferName: localTransferName, OriginalName: input.OriginalName,
		LegacyOriginalName: legacyOriginalName,
		RecognitionRaw:     localRaw, TransferRaw: transfer.LocalRaw,
	}
	comparison.RenameVariables = buildOrganize115ShadowVariableComparison(primaryRenameInput, localRenameInput)
	for _, variable := range comparison.RenameVariables {
		if variable.Matched {
			continue
		}
		comparison.Differences = append(comparison.Differences, Organize115ShadowDifference{
			Stage: "variable", Field: variable.Name, Label: variable.Label,
			MoviePilot: variable.MoviePilot, Local: variable.Local,
		})
	}

	localTargetPath, err := h.buildTargetPathWithDirectory(input.DirectoryName, categoryForPath, localRenameInput)
	if err != nil {
		comparison.LocalTargetError = err.Error()
		refreshOrganize115ShadowStatus(comparison)
		return
	}
	comparison.LocalTargetPath = strings.TrimSpace(localTargetPath)
	if comparison.MoviePilotTargetPath != comparison.LocalTargetPath {
		comparison.Differences = append(comparison.Differences, Organize115ShadowDifference{
			Stage: "target", Field: "target_path", Label: "目标路径",
			MoviePilot: comparison.MoviePilotTargetPath, Local: comparison.LocalTargetPath,
		})
	}
	refreshOrganize115ShadowStatus(comparison)
}

func buildOrganize115ShadowVariableComparison(primary, local service.MediaRenameInput) []Organize115ShadowVariableComparison {
	primaryValues := service.BuildMediaRenameVariables(primary)
	localValues := service.BuildMediaRenameVariables(local)
	variables := append([]service.MediaRenameVariable{}, service.MediaRenameCommonVariables()...)
	if organizeShadowIsTV(primary) || organizeShadowIsTV(local) {
		variables = append(variables, service.MediaRenameTVVariables()...)
	}
	rows := make([]Organize115ShadowVariableComparison, 0, len(variables))
	for _, variable := range variables {
		moviePilotValue := strings.TrimSpace(fmt.Sprint(primaryValues[variable.Name]))
		localValue := strings.TrimSpace(fmt.Sprint(localValues[variable.Name]))
		rows = append(rows, Organize115ShadowVariableComparison{
			Name: variable.Name, Label: variable.Label,
			MoviePilot: moviePilotValue, Local: localValue,
			Matched: moviePilotValue == localValue,
		})
	}
	return rows
}

func organizeShadowIsTV(input service.MediaRenameInput) bool {
	for _, value := range []string{input.MediaType, input.Info.MediaType} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "tv", "series", "show", "tvshow", "电视剧", "剧集", "番剧", "动漫", "动画", "动画番剧":
			return true
		}
	}
	return false
}
