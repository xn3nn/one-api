package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"one-api/common"
	"one-api/model"
	"one-api/providers"
	providers_base "one-api/providers/base"
	"one-api/types"

	"github.com/gin-gonic/gin"
)

func relayHelper(c *gin.Context, relayMode int) *types.OpenAIErrorWithStatusCode {
	// 获取请求参数
	channelType := c.GetInt("channel")
	channelId := c.GetInt("channel_id")
	tokenId := c.GetInt("token_id")
	userId := c.GetInt("id")
	group := c.GetString("group")

	// 获取 Provider
	provider := providers.GetProvider(channelType, c)
	if provider == nil {
		return common.ErrorWrapper(errors.New("channel not found"), "channel_not_found", http.StatusNotImplemented)
	}

	if !provider.SupportAPI(relayMode) {
		return common.ErrorWrapper(errors.New("channel does not support this API"), "channel_not_support_api", http.StatusNotImplemented)
	}

	modelMap, err := parseModelMapping(c.GetString("model_mapping"))
	if err != nil {
		return common.ErrorWrapper(err, "unmarshal_model_mapping_failed", http.StatusInternalServerError)
	}

	quotaInfo := &QuotaInfo{
		modelName:    "",
		promptTokens: 0,
		userId:       userId,
		channelId:    channelId,
		tokenId:      tokenId,
	}

	var usage *types.Usage
	var openAIErrorWithStatusCode *types.OpenAIErrorWithStatusCode

	switch relayMode {
	case common.RelayModeChatCompletions:
		usage, openAIErrorWithStatusCode = handleChatCompletions(c, provider, modelMap, quotaInfo, group)
	case common.RelayModeCompletions:
		usage, openAIErrorWithStatusCode = handleCompletions(c, provider, modelMap, quotaInfo, group)
	case common.RelayModeEmbeddings:
		usage, openAIErrorWithStatusCode = handleEmbeddings(c, provider, modelMap, quotaInfo, group)
	case common.RelayModeModerations:
		usage, openAIErrorWithStatusCode = handleModerations(c, provider, modelMap, quotaInfo, group)
	case common.RelayModeAudioSpeech:
		usage, openAIErrorWithStatusCode = handleSpeech(c, provider, modelMap, quotaInfo, group)
	case common.RelayModeAudioTranscription:
		usage, openAIErrorWithStatusCode = handleTranscriptions(c, provider, modelMap, quotaInfo, group)
	case common.RelayModeAudioTranslation:
		usage, openAIErrorWithStatusCode = handleTranslations(c, provider, modelMap, quotaInfo, group)
	case common.RelayModeImagesGenerations:
		usage, openAIErrorWithStatusCode = handleImageGenerations(c, provider, modelMap, quotaInfo, group)
	case common.RelayModeImagesEdits:
		usage, openAIErrorWithStatusCode = handleImageEdits(c, provider, modelMap, quotaInfo, group, "edit")
	case common.RelayModeImagesVariations:
		usage, openAIErrorWithStatusCode = handleImageEdits(c, provider, modelMap, quotaInfo, group, "variation")
	default:
		return common.ErrorWrapper(errors.New("invalid relay mode"), "invalid_relay_mode", http.StatusBadRequest)
	}

	if openAIErrorWithStatusCode != nil {
		if quotaInfo.preConsumedQuota != 0 {
			go func(ctx context.Context) {
				// return pre-consumed quota
				err := model.PostConsumeTokenQuota(tokenId, -quotaInfo.preConsumedQuota)
				if err != nil {
					common.LogError(ctx, "error return pre-consumed quota: "+err.Error())
				}
			}(c.Request.Context())
		}
		return openAIErrorWithStatusCode
	}

	tokenName := c.GetString("token_name")
	defer func(ctx context.Context) {
		go func() {
			err = quotaInfo.completedQuotaConsumption(usage, tokenName, ctx)
			if err != nil {
				common.LogError(ctx, err.Error())
			}
		}()
	}(c.Request.Context())

	return nil
}

func handleChatCompletions(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var chatRequest types.ChatCompletionRequest
	isModelMapped := false

	chatProvider, ok := provider.(providers_base.ChatInterface)
	if !ok {
		return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
	}

	err := common.UnmarshalBodyReusable(c, &chatRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if chatRequest.Messages == nil || len(chatRequest.Messages) == 0 {
		return nil, common.ErrorWrapper(errors.New("field messages is required"), "required_field_missing", http.StatusBadRequest)
	}

	if modelMap != nil && modelMap[chatRequest.Model] != "" {
		chatRequest.Model = modelMap[chatRequest.Model]
		isModelMapped = true
	}
	promptTokens := common.CountTokenMessages(chatRequest.Messages, chatRequest.Model)

	quotaInfo.modelName = chatRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}
	return chatProvider.ChatAction(&chatRequest, isModelMapped, promptTokens)
}

func handleCompletions(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var completionRequest types.CompletionRequest
	isModelMapped := false
	completionProvider, ok := provider.(providers_base.CompletionInterface)
	if !ok {
		return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
	}

	err := common.UnmarshalBodyReusable(c, &completionRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if completionRequest.Prompt == "" {
		return nil, common.ErrorWrapper(errors.New("field prompt is required"), "required_field_missing", http.StatusBadRequest)
	}

	if modelMap != nil && modelMap[completionRequest.Model] != "" {
		completionRequest.Model = modelMap[completionRequest.Model]
		isModelMapped = true
	}
	promptTokens := common.CountTokenInput(completionRequest.Prompt, completionRequest.Model)

	quotaInfo.modelName = completionRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}
	return completionProvider.CompleteAction(&completionRequest, isModelMapped, promptTokens)
}

func handleEmbeddings(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var embeddingsRequest types.EmbeddingRequest
	isModelMapped := false
	embeddingsProvider, ok := provider.(providers_base.EmbeddingsInterface)
	if !ok {
		return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
	}

	err := common.UnmarshalBodyReusable(c, &embeddingsRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if embeddingsRequest.Input == "" {
		return nil, common.ErrorWrapper(errors.New("field input is required"), "required_field_missing", http.StatusBadRequest)
	}

	if modelMap != nil && modelMap[embeddingsRequest.Model] != "" {
		embeddingsRequest.Model = modelMap[embeddingsRequest.Model]
		isModelMapped = true
	}
	promptTokens := common.CountTokenInput(embeddingsRequest.Input, embeddingsRequest.Model)

	quotaInfo.modelName = embeddingsRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}
	return embeddingsProvider.EmbeddingsAction(&embeddingsRequest, isModelMapped, promptTokens)
}

func handleModerations(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var moderationRequest types.ModerationRequest
	isModelMapped := false
	moderationProvider, ok := provider.(providers_base.ModerationInterface)
	if !ok {
		return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
	}

	err := common.UnmarshalBodyReusable(c, &moderationRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if moderationRequest.Input == "" {
		return nil, common.ErrorWrapper(errors.New("field input is required"), "required_field_missing", http.StatusBadRequest)
	}

	if moderationRequest.Model == "" {
		moderationRequest.Model = "text-moderation-latest"
	}

	if modelMap != nil && modelMap[moderationRequest.Model] != "" {
		moderationRequest.Model = modelMap[moderationRequest.Model]
		isModelMapped = true
	}
	promptTokens := common.CountTokenInput(moderationRequest.Input, moderationRequest.Model)

	quotaInfo.modelName = moderationRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}
	return moderationProvider.ModerationAction(&moderationRequest, isModelMapped, promptTokens)
}

func handleSpeech(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var speechRequest types.SpeechAudioRequest
	isModelMapped := false
	speechProvider, ok := provider.(providers_base.SpeechInterface)
	if !ok {
		return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
	}

	err := common.UnmarshalBodyReusable(c, &speechRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if speechRequest.Input == "" {
		return nil, common.ErrorWrapper(errors.New("field input is required"), "required_field_missing", http.StatusBadRequest)
	}

	if modelMap != nil && modelMap[speechRequest.Model] != "" {
		speechRequest.Model = modelMap[speechRequest.Model]
		isModelMapped = true
	}
	promptTokens := len(speechRequest.Input)

	quotaInfo.modelName = speechRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}
	return speechProvider.SpeechAction(&speechRequest, isModelMapped, promptTokens)
}

func handleTranscriptions(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var audioRequest types.AudioRequest
	isModelMapped := false
	speechProvider, ok := provider.(providers_base.TranscriptionsInterface)
	if !ok {
		return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
	}

	err := common.UnmarshalBodyReusable(c, &audioRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if audioRequest.File == nil {
		fmt.Println(audioRequest)
		return nil, common.ErrorWrapper(errors.New("field file is required"), "required_field_missing", http.StatusBadRequest)
	}

	if modelMap != nil && modelMap[audioRequest.Model] != "" {
		audioRequest.Model = modelMap[audioRequest.Model]
		isModelMapped = true
	}
	promptTokens := 0

	quotaInfo.modelName = audioRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}
	return speechProvider.TranscriptionsAction(&audioRequest, isModelMapped, promptTokens)
}

func handleTranslations(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var audioRequest types.AudioRequest
	isModelMapped := false
	speechProvider, ok := provider.(providers_base.TranslationInterface)
	if !ok {
		return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
	}

	err := common.UnmarshalBodyReusable(c, &audioRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if audioRequest.File == nil {
		fmt.Println(audioRequest)
		return nil, common.ErrorWrapper(errors.New("field file is required"), "required_field_missing", http.StatusBadRequest)
	}

	if modelMap != nil && modelMap[audioRequest.Model] != "" {
		audioRequest.Model = modelMap[audioRequest.Model]
		isModelMapped = true
	}
	promptTokens := 0

	quotaInfo.modelName = audioRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}
	return speechProvider.TranslationAction(&audioRequest, isModelMapped, promptTokens)
}

func handleImageGenerations(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var imageRequest types.ImageRequest
	isModelMapped := false
	imageGenerationsProvider, ok := provider.(providers_base.ImageGenerationsInterface)
	if !ok {
		return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
	}

	err := common.UnmarshalBodyReusable(c, &imageRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if imageRequest.Model == "" {
		imageRequest.Model = "dall-e-2"
	}

	if imageRequest.Size == "" {
		imageRequest.Size = "1024x1024"
	}

	if imageRequest.Quality == "" {
		imageRequest.Quality = "standard"
	}

	if modelMap != nil && modelMap[imageRequest.Model] != "" {
		imageRequest.Model = modelMap[imageRequest.Model]
		isModelMapped = true
	}
	promptTokens, err := common.CountTokenImage(imageRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "count_token_image_failed", http.StatusInternalServerError)
	}

	quotaInfo.modelName = imageRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}
	return imageGenerationsProvider.ImageGenerationsAction(&imageRequest, isModelMapped, promptTokens)
}

func handleImageEdits(c *gin.Context, provider providers_base.ProviderInterface, modelMap map[string]string, quotaInfo *QuotaInfo, group string, imageType string) (*types.Usage, *types.OpenAIErrorWithStatusCode) {
	var imageEditRequest types.ImageEditRequest
	isModelMapped := false
	var imageEditsProvider providers_base.ImageEditsInterface
	var imageVariations providers_base.ImageVariationsInterface
	var ok bool
	if imageType == "edit" {
		imageEditsProvider, ok = provider.(providers_base.ImageEditsInterface)
		if !ok {
			return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
		}
	} else {
		imageVariations, ok = provider.(providers_base.ImageVariationsInterface)
		if !ok {
			return nil, common.ErrorWrapper(errors.New("channel not implemented"), "channel_not_implemented", http.StatusNotImplemented)
		}
	}

	err := common.UnmarshalBodyReusable(c, &imageEditRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "bind_request_body_failed", http.StatusBadRequest)
	}

	if imageEditRequest.Model == "" {
		imageEditRequest.Model = "dall-e-2"
	}

	if imageEditRequest.Size == "" {
		imageEditRequest.Size = "1024x1024"
	}

	if modelMap != nil && modelMap[imageEditRequest.Model] != "" {
		imageEditRequest.Model = modelMap[imageEditRequest.Model]
		isModelMapped = true
	}
	promptTokens, err := common.CountTokenImage(imageEditRequest)
	if err != nil {
		return nil, common.ErrorWrapper(err, "count_token_image_failed", http.StatusInternalServerError)
	}

	quotaInfo.modelName = imageEditRequest.Model
	quotaInfo.promptTokens = promptTokens
	quotaInfo.initQuotaInfo(group)
	quota_err := quotaInfo.preQuotaConsumption()
	if quota_err != nil {
		return nil, quota_err
	}

	if imageType == "edit" {
		return imageEditsProvider.ImageEditsAction(&imageEditRequest, isModelMapped, promptTokens)
	}

	return imageVariations.ImageVariationsAction(&imageEditRequest, isModelMapped, promptTokens)
}