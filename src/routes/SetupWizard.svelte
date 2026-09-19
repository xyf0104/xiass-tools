<script lang="ts">
  import { onDestroy } from "svelte";
  import CodexAccountPanel from "../components/CodexAccountPanel.svelte";
  import { codexAccountErrorKey, discardCodexAccountSession, selectedCodexAccount, type CodexAccountSession } from "../lib/codexAccounts";
  import { cubicOut } from "svelte/easing";
  import { fade, fly } from "svelte/transition";
  import { listProfileModels, previewProfileWrite, saveProfileDraft } from "../lib/api";
  import { t, type TranslationKey } from "../lib/i18n";
  import { profileNameErrorKey, profileProviderFromName, XIASS_API_PRESET } from "../lib/profiles/xiass";
  import {
    canonicalProfileToolId,
    configProtocolIdsForTool,
    PROFILE_PROTOCOL_OPTIONS,
    PROFILE_TOOL_CATALOG,
    profileRequiresApiKeyConfig,
    profileSupportsModelMappings as catalogSupportsModelMappings
  } from "../lib/profiles/catalog";
  import {
    emptyProfileModelMappingForm,
    modelMappingsForRequest,
    profileModelMappingsAreValid,
    type ProfileModelMappingForm
  } from "../lib/profiles/form";
  import AppIcon from "../components/AppIcon.svelte";
  import ModelSelectInput from "../components/ModelSelectInput.svelte";
  import SecretInput from "../components/SecretInput.svelte";
  import ToolIcon from "../components/ToolIcon.svelte";
  import {
    actionButtonRecipe,
    wizardActionsRecipe,
    wizardButtonRowRecipe,
    wizardChoiceButtonRecipe,
    wizardChoiceGridRecipe,
    wizardFieldErrorRecipe,
    wizardFormGridRecipe,
    wizardInlineNoticeRecipe,
    wizardModeChoiceRecipe,
    wizardPanelRecipe,
    wizardPreviewBoxRecipe,
    wizardPreviewHeadingRecipe,
    wizardPreviewWarningsRecipe,
    routeStackRecipe,
    topStripRecipe,
    wizardSecurityNoteRecipe,
    wizardStepContentRecipe,
    wizardStepItemRecipe,
    wizardStepperRecipe,
    spinRecipe,
    wizardWideFieldRecipe,
    wizardWriteContentPreviewRecipe,
    wizardWritePreviewListRecipe,
    wizardWritePreviewMetaRecipe,
    wizardWritePreviewRowRecipe
  } from "../../styled-system/recipes";
  import { css } from "../../styled-system/css";
  import type {
    DetectionSnapshot,
    InstallState,
    PreviewProfileWriteResult,
    ProfileDraft,
    ProfileWritePreviewItem,
    ProfileModelMapping,
    ProfileModelOption,
    ProviderApplyMode,
    SaveProfileDraftRequest,
    WizardPrefill
  } from "../types";

  const wizardStepEnter = { y: 14, duration: 240, opacity: 0, easing: cubicOut };
  const wizardStepExit = { duration: 110 };
  const modelPickerClass = css({
    display: "grid",
    gap: "6px",
    color: "var(--text-soft)",
    fontSize: "13px",
    fontWeight: 800,
    minWidth: 0
  });
  const modelPickerRowClass = css({
    display: "grid",
    gridTemplateColumns: "minmax(0, 1fr) auto",
    alignItems: "center",
    gap: "8px",
    minWidth: 0,
    "& > button": {
      height: "38px"
    },
    "@media (max-width: 860px)": {
      gridTemplateColumns: "1fr",
      alignItems: "stretch"
    }
  });
  const modelPickerStatusClass = css({
    color: "var(--text-muted)",
    fontSize: "12px",
    fontWeight: 700,
    lineHeight: 1.35,
    overflowWrap: "anywhere"
  });
  const modelMappingPanelClass = css({
    display: "grid",
    gap: "10px",
    gridColumn: "1 / -1",
    padding: "12px",
    border: "1px solid var(--border)",
    borderRadius: "8px",
    background: "var(--surface-muted)",
    minWidth: 0
  });
  const modelMappingHeaderClass = css({
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: "10px",
    minWidth: 0,
    "& strong": {
      color: "var(--text)",
      fontSize: "13px",
      fontWeight: 800
    }
  });
  const modelMappingRowsClass = css({
    display: "grid",
    gap: "8px",
    minWidth: 0
  });
  const modelMappingRowClass = css({
    display: "grid",
    gridTemplateColumns: "minmax(120px, 1fr) minmax(120px, 1fr) minmax(120px, 1fr) auto auto",
    gap: "8px",
    alignItems: "end",
    minWidth: 0,
    "& label": {
      minWidth: 0
    },
    "@media (max-width: 980px)": {
      gridTemplateColumns: "1fr"
    }
  });
  const modelMappingToggleClass = css({
    display: "inline-flex",
    alignItems: "center",
    gap: "8px",
    color: "var(--text-soft)",
    fontSize: "13px",
    fontWeight: 800,
    minHeight: "38px"
  });
  const modelMappingFieldClass = css({
    display: "grid",
    gap: "6px",
    minWidth: 0,
    color: "var(--text-soft)",
    fontSize: "13px",
    fontWeight: 800
  });
  const codexCapabilityPanelClass = css({
    display: "grid",
    gap: "12px",
    gridColumn: "1 / -1",
    padding: "14px",
    border: "1px solid color-mix(in srgb, var(--accent) 24%, var(--border))",
    borderRadius: "16px",
    background: "linear-gradient(135deg, color-mix(in srgb, #ff9b51 8%, var(--surface-muted)), color-mix(in srgb, #2f8df4 10%, var(--surface-muted)))",
    "& > strong": { color: "var(--text)", fontSize: "13px", fontWeight: 850 },
    "& small": { color: "var(--text-muted)", lineHeight: 1.45 },
    "& .codex-capability-grid": {
      display: "grid",
      gridTemplateColumns: "repeat(2, minmax(0, 1fr))",
      gap: "10px",
      "@media (max-width: 760px)": { gridTemplateColumns: "1fr" }
    },
    "& label": { minWidth: 0 }
  });

  export let onProfileSaved: (profile: ProfileDraft) => void | Promise<void> = () => {};
  export let prefill: WizardPrefill | null = null;
  export let snapshot: DetectionSnapshot | null = null;
  export let onCancel: () => void = () => {};
  $: toolLocked = Boolean(prefill?.lockTool && prefill?.toolId);
  $: firstStep = toolLocked ? 1 : 0;

  type ToolDefaults = {
    id: string;
    label: string;
    profileNameKey: TranslationKey;
    protocol: string;
    baseUrl: string;
    model: string;
  };


  const protocolOptions = PROFILE_PROTOCOL_OPTIONS;
  type ProtocolOption = (typeof protocolOptions)[number];

  const steps: TranslationKey[] = [
    "wizard.step.bootstrapTarget",
    "wizard.step.chooseProvider",
    "wizard.step.preview"
  ];

  const toolDefaults: ToolDefaults[] = PROFILE_TOOL_CATALOG.map((tool) => ({
    id: tool.id,
    label: tool.label,
    profileNameKey: tool.defaultProfileNameKey as TranslationKey,
    protocol: tool.defaultProtocol,
    baseUrl: "",
    model: ""
  }));

  let currentStep = 0;
  let selectedTool = "codex";
  let profileMode: ProviderApplyMode = "config";
  let protocol = "openai-responses";
  let profileName: string = XIASS_API_PRESET.name;
  let profileRemark = "";
  let apiKey = "";
  let baseUrl: string = XIASS_API_PRESET.baseUrl;
  let model: string = XIASS_API_PRESET.model;
  let reviewModel: string = XIASS_API_PRESET.reviewModel;
  let webSearch: "live" | "cached" | "indexed" | "disabled" = "live";
  let imageModel = "";
  let modelContextWindow = 372000;
  let modelAutoCompactTokenLimit = 334800;
  let contextMode: "235000" | "372000" | "512000" | "1000000" | "custom" = "372000";
  let modelMappings: ProfileModelMappingForm[] = [];
  let modelOptions: ProfileModelOption[] = [];
  let modelLoading = false;
  let modelError: string | null = null;
  let modelLoadedKey = "";
  let saving = false;
  let saveError: string | null = null;
  let savedProfileName: string | null = null;
  let appliedPrefillKey: string | null = null;
  let previewing = false;
  let previewError: string | null = null;
  let writePreview: PreviewProfileWriteResult | null = null;
  let writePreviewKey: string | null = null;
  let codexOAuthConfig = false;
  let apiProfileName: string = XIASS_API_PRESET.name;
  let accountProfileName: string | null = null;
  let accountModel = "";
  let accountReviewModel = "";
  let codexAccountSource: "oauth" | "json" = "oauth";
  let codexAccountSession: CodexAccountSession | null = null;
  let codexAccountIndex = 0;
  let codexAccountBusy = false;
  function clearCodexSelection() {
    if (codexAccountSession) void discardCodexAccountSession(codexAccountSession.id).catch(() => {});
    codexAccountSession = null;
    codexAccountIndex = 0;
    codexAccountBusy = false;
  }
  onDestroy(clearCodexSelection);

  $: if (prefill && appliedPrefillKey !== prefillKey(prefill)) {
    if (prefill.toolId) {
      applyToolDefaults(prefill.toolId, prefill.toolName, prefill.mode ?? "config");
    } else {
      setProfileMode(prefill.mode ?? "config");
      resetDraftState();
    }
    appliedPrefillKey = prefillKey(prefill);
    if (prefill.lockTool && prefill.toolId) currentStep = 1;
  }

  $: selectedToolInstalled = toolCanCreateProfile(selectedTool);
  $: visibleToolDefaults = toolDefaults.filter((tool) => toolVisibleInSnapshot(tool.id));
  $: canUseCodexOAuthConfig = canonicalProfileToolId(selectedTool) === "codex" && profileMode === "config";
  $: if (!canUseCodexOAuthConfig && codexOAuthConfig) {
    codexOAuthConfig = false;
  }
  $: activeProvider = profileProviderFromName(profileName, codexOAuthConfig);
  $: nameErrorKey = profileNameErrorKey(profileName, codexOAuthConfig);
  $: activeProtocol = codexOAuthConfig ? "openai-responses" : protocol;
  $: activeModel = codexOAuthConfig ? accountModel : model;
  $: supportsReviewModel = canonicalProfileToolId(selectedTool) === "codex";
  $: activeReviewModel = supportsReviewModel ? (codexOAuthConfig ? accountReviewModel : reviewModel).trim() || null : null;
  $: activeModelMappings = codexOAuthConfig ? [] : modelMappingsForRequest(selectedTool, modelMappings);
  $: activeBaseUrl = codexOAuthConfig ? "" : baseUrl;
  $: activeApiKey = codexOAuthConfig ? "" : apiKey;
  $: codexContextValid = canonicalProfileToolId(selectedTool) !== "codex"
    || (Number.isInteger(modelContextWindow) && modelContextWindow >= 64000 && modelContextWindow <= 1050000
      && Number.isInteger(modelAutoCompactTokenLimit) && modelAutoCompactTokenLimit >= 16000
      && modelAutoCompactTokenLimit < modelContextWindow);
  $: activeSecretProvided = !codexOAuthConfig && apiKey.trim().length > 0;
  $: codexAccountSelection = selectedCodexAccount(codexAccountSession, codexAccountIndex);
  $: codexOAuthAuthorized = Boolean(codexAccountSelection) && !codexAccountBusy;
  $: availableProtocolOptions = protocolOptionsFor(selectedTool, profileMode);
  $: if (
    availableProtocolOptions.length > 0 &&
    !protocolOptionAvailable(availableProtocolOptions, protocol)
  ) {
    protocol = availableProtocolOptions[0].id;
  }
  $: supportsModelMappings = !codexOAuthConfig && catalogSupportsModelMappings(selectedTool);
  $: if (!supportsModelMappings && modelMappings.length > 0) {
    modelMappings = [];
  }
  $: modelMappingsValid = !supportsModelMappings || profileModelMappingsAreValid(modelMappings);
  $: modelListId = `wizard-model-options-${domSafeId(selectedTool)}`;
  $: modelRequestKey = profileModelRequestKey({
    app: selectedTool,
    mode: profileMode,
    provider: activeProvider,
    protocol: activeProtocol,
    baseUrl: activeBaseUrl,
    apiKey: activeApiKey
  });
  $: if (modelLoadedKey && modelLoadedKey !== modelRequestKey) {
    modelOptions = [];
    modelError = null;
    modelLoadedKey = "";
  }
  $: previewRequestKey = [
    profileName.trim(),
    profileRemark.trim(),
    selectedTool.trim(),
    profileMode,
    activeProvider.trim(),
    activeProtocol.trim(),
    activeModel.trim(),
    activeReviewModel ?? "",
    JSON.stringify(activeModelMappings),
    activeBaseUrl.trim(),
    activeSecretProvided ? "secret" : "no-secret",
    webSearch,
    imageModel.trim(),
    String(modelContextWindow),
    String(modelAutoCompactTokenLimit),
    codexOAuthConfig ? `codex-account:${codexAccountSelection?.sessionId}:${codexAccountIndex}` : "api"
  ].join("|");
  $: baseUrlErrorKey = providerNeedsBaseUrl(activeProvider) ? baseUrlValidationErrorKey(activeBaseUrl) : null;
  $: visibleBaseUrlErrorKey =
    baseUrlErrorKey === "wizard.error.baseUrlRequired" ? null : baseUrlErrorKey;
  $: if (currentStep === steps.length - 1 && canApply && writePreviewKey !== previewRequestKey && !previewing) {
    void refreshWritePreview(previewRequestKey);
  }
  $: canApply =
    !nameErrorKey &&
    selectedTool.trim().length > 0 &&
    selectedToolInstalled &&
    activeProvider.trim().length > 0 &&
    isProtocolAllowedForToolMode(selectedTool, profileMode, activeProtocol) &&
    (!providerNeedsBaseUrl(activeProvider) || baseUrlErrorKey === null) &&
    (!providerRequiresApiKey(activeProvider) || activeSecretProvided) &&
    modelMappingsValid &&
    codexContextValid &&
    (!codexOAuthConfig || codexOAuthAuthorized) &&
    !saving;
  $: canFetchModels =
    !codexOAuthConfig &&
    !providerIsOfficial(activeProvider) &&
    activeProvider.trim().length > 0 &&
    isProtocolAllowedForToolMode(selectedTool, profileMode, activeProtocol) &&
    (!providerNeedsBaseUrl(activeProvider) || baseUrlErrorKey === null) &&
    (!providerRequiresApiKey(activeProvider) || activeSecretProvided) &&
    !modelLoading;
  $: modelFetchDisabled = codexOAuthConfig || providerIsOfficial(activeProvider) || saving || modelLoading;
  $: modelStatus = modelLoading
    ? $t("profiles.fetchingModels")
    : modelError
      ? modelError
      : modelOptions.length > 0
        ? $t("profiles.modelListLoaded", { count: modelOptions.length })
        : null;
  $: canContinue =
    currentStep === 0
      ? selectedToolInstalled
      : currentStep === 1
        ? !nameErrorKey &&
          activeProvider.trim().length > 0 &&
          isProtocolAllowedForToolMode(selectedTool, profileMode, activeProtocol) &&
          (!providerNeedsBaseUrl(activeProvider) || baseUrlErrorKey === null) &&
          (!providerRequiresApiKey(activeProvider) || activeSecretProvided) &&
          modelMappingsValid &&
          codexContextValid &&
          (!codexOAuthConfig || codexOAuthAuthorized)
        : true;

  function prefillKey(value: WizardPrefill) {
    return `${value.toolId ?? ""}:${value.toolName ?? ""}:${value.mode ?? "config"}:${Boolean(value.lockTool)}`;
  }

  function setProfileMode(nextMode: ProviderApplyMode) {
    const normalizedMode = profileRequiresApiKeyConfig(selectedTool) ? "config" : nextMode;
    profileMode = normalizedMode;
    if (normalizedMode !== "config") {
      codexOAuthConfig = false;
    }
    resetModelOptions();
    writePreview = null;
    writePreviewKey = null;
    previewError = null;
  }

  function resetDraftState() {
    currentStep = 0;
    saveError = null;
    savedProfileName = null;
    previewError = null;
    resetModelOptions();
    writePreview = null;
    writePreviewKey = null;
  }

  function applyToolDefaults(toolId: string, fallbackName?: string, mode: ProviderApplyMode = "config") {
    const canonicalToolId = canonicalProfileToolId(toolId);
    const defaults = toolDefaults.find((tool) => tool.id === canonicalToolId);
    selectedTool = defaults?.id ?? canonicalToolId;
    setProfileMode(mode);
    protocol = defaults?.protocol ?? "openai-chat-completions";
    profileName = defaults?.profileNameKey
      ? $t(defaults.profileNameKey)
      : $t("wizard.defaultProfile.generic", { name: fallbackName ?? toolId });
    profileRemark = "";
    apiKey = "";
    baseUrl = defaults?.baseUrl ?? "";
    model = defaults?.model ?? "";
    reviewModel = "";
    if (canonicalToolId === "codex") {
      profileName = XIASS_API_PRESET.name;
      baseUrl = XIASS_API_PRESET.baseUrl;
      model = XIASS_API_PRESET.model;
      reviewModel = XIASS_API_PRESET.reviewModel;
    }
    webSearch = "live";
    imageModel = "";
    modelContextWindow = 372000;
    modelAutoCompactTokenLimit = 334800;
    contextMode = "372000";
    modelMappings = [];
    codexOAuthConfig = false;
    apiProfileName = profileName;
    accountProfileName = null;
    accountModel = "";
    accountReviewModel = "";
    clearCodexSelection();
    resetModelOptions();
    resetDraftState();
  }

  function selectCodexOAuthConfig(nextValue: boolean, source: "oauth" | "json" = "oauth") {
    if (saving || codexAccountBusy) return;
    if (nextValue !== codexOAuthConfig) {
      if (nextValue) {
        apiProfileName = profileName;
        profileName = accountProfileName ?? $t("codexAccount.defaultName");
      } else {
        accountProfileName = profileName;
        profileName = apiProfileName;
      }
    }
    if (nextValue !== codexOAuthConfig || source !== codexAccountSource) clearCodexSelection();
    codexOAuthConfig = nextValue;
    codexAccountSource = source;
    writePreview = null;
    writePreviewKey = null;
    previewError = null;
    saveError = null;
    resetModelOptions();
  }

  function goBack() {
    if (saving) return;
    if (codexAccountBusy) clearCodexSelection();
    if (currentStep > firstStep) currentStep -= 1;
    else onCancel();
  }

  function applyContextPreset(value: string) {
    contextMode = ["235000", "372000", "512000", "1000000"].includes(value)
      ? value as typeof contextMode
      : "custom";
    if (contextMode !== "custom") {
      modelContextWindow = Number(contextMode);
      modelAutoCompactTokenLimit = Math.floor(modelContextWindow * 0.9);
    }
  }

  function updateContextWindow(value: string) {
    contextMode = "custom";
    const parsed = Number(value);
    if (Number.isFinite(parsed)) {
      modelContextWindow = Math.round(parsed);
      if (modelAutoCompactTokenLimit >= modelContextWindow) {
        modelAutoCompactTokenLimit = Math.floor(modelContextWindow * 0.9);
      }
    }
  }

  function updateAutoCompactLimit(value: string) {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) {
      modelAutoCompactTokenLimit = Math.round(parsed);
    }
  }

  function selectedToolLabel(toolId: string) {
    const canonicalToolId = canonicalProfileToolId(toolId);
    return toolDefaults.find((tool) => tool.id === canonicalToolId)?.label ?? canonicalToolId;
  }

  function protocolOptionsFor(toolId: string, mode: ProviderApplyMode): readonly ProtocolOption[] {
    if (mode === "gateway") {
      return protocolOptions;
    }
    const supportedIds = configProtocolIdsForTool(toolId);
    return protocolOptions.filter((option) => supportedIds.includes(option.id));
  }

  function protocolOptionAvailable(options: readonly ProtocolOption[], value: string) {
    const normalized = normalizeProtocol(value);
    return options.some((option) => option.id === normalized);
  }

  function isProtocolAllowedForToolMode(toolId: string, mode: ProviderApplyMode, value: string) {
    return protocolOptionAvailable(protocolOptionsFor(toolId, mode), value);
  }

  function providerRequiresApiKey(providerId: string) {
    return !providerIsOfficial(providerId);
  }

  function providerNeedsBaseUrl(providerId: string) {
    return !providerIsOfficial(providerId);
  }

  function providerIsOfficial(providerId: string) {
    return providerId.trim() === "official";
  }

  function resetModelOptions() {
    modelOptions = [];
    modelLoading = false;
    modelError = null;
    modelLoadedKey = "";
  }

  function addModelMapping() {
    modelMappings = [...modelMappings, emptyProfileModelMappingForm()];
  }

  function updateModelMapping(index: number, patch: Partial<ProfileModelMappingForm>) {
    modelMappings = modelMappings.map((mapping, itemIndex) =>
      itemIndex === index ? { ...mapping, ...patch } : mapping
    );
  }

  function updateModelMappingModel(index: number, value: string) {
    const option = modelOptions.find((item) => item.id === value.trim());
    const current = modelMappings[index];
    updateModelMapping(index, {
      model: value,
      supports1m: option?.supports1m ?? current?.supports1m ?? false,
      description: current?.description || option?.name || ""
    });
  }

  function removeModelMapping(index: number) {
    modelMappings = modelMappings.filter((_, itemIndex) => itemIndex !== index);
  }

  function domSafeId(value: string) {
    return value.replace(/[^a-zA-Z0-9_-]+/g, "-") || "model";
  }

  function profileModelRequestKey(input: {
    app: string;
    mode: ProviderApplyMode;
    provider: string;
    protocol: string;
    baseUrl: string;
    apiKey: string;
  }) {
    return [
      input.app.trim(),
      input.mode,
      input.provider.trim(),
      input.protocol.trim(),
      normalizeBaseUrl(input.baseUrl),
      input.apiKey.trim() ? "inline-key" : "no-key"
    ].join("|");
  }

  function modelOptionLabel(option: ProfileModelOption) {
    const label = option.name && option.name !== option.id ? `${option.id} - ${option.name}` : option.id;
    return option.supports1m ? `${label} (1M)` : label;
  }

  function buildProfileDraftRequest(): SaveProfileDraftRequest {
    return {
      codexAccount: codexOAuthConfig ? codexAccountSelection : null,
      name: profileName,
      icon: null,
      remark: profileRemark,
      app: selectedTool,
      mode: profileMode,
      provider: activeProvider,
      protocol: activeProtocol,
      model: activeModel,
      reviewModel: activeReviewModel,
      modelMappings: activeModelMappings,
      baseUrl: normalizeBaseUrl(activeBaseUrl),
      secretProvided: activeSecretProvided,
      apiKey: activeApiKey,
      webSearch: canonicalProfileToolId(selectedTool) === "codex" ? webSearch : null,
      imageModel: canonicalProfileToolId(selectedTool) === "codex" ? imageModel.trim() || null : null,
      modelContextWindow: canonicalProfileToolId(selectedTool) === "codex" ? modelContextWindow : null,
      modelAutoCompactTokenLimit: canonicalProfileToolId(selectedTool) === "codex" ? modelAutoCompactTokenLimit : null
    };
  }

  async function handleApply() {
    if (!canApply) {
      saveError = applyBlockedMessage();
      return;
    }

    saving = true;
    saveError = null;
    savedProfileName = null;

    try {
      const profile = await saveProfileDraft(buildProfileDraftRequest());
      savedProfileName = profile.name;
      await onProfileSaved(profile);
    } catch (err) {
      saveError = errorLabel(err instanceof Error ? err.message : String(err));
    } finally {
      saving = false;
    }
  }

  async function refreshModels() {
    if (!canFetchModels) {
      modelError = $t("profiles.modelListNeedsConfig");
      return;
    }

    modelLoading = true;
    modelError = null;
    const requestKey = modelRequestKey;

    try {
      const result = await listProfileModels({
        app: selectedTool,
        mode: profileMode,
        provider: activeProvider,
        protocol: activeProtocol,
        baseUrl: normalizeBaseUrl(activeBaseUrl),
        apiKey: activeApiKey
      });
      modelOptions = result.models;
      modelLoadedKey = requestKey;
      if (result.models.length === 0) {
        modelError = $t("profiles.modelListEmpty");
      }
    } catch (err) {
      modelOptions = [];
      modelLoadedKey = "";
      modelError = errorLabel(err instanceof Error ? err.message : String(err));
    } finally {
      modelLoading = false;
    }
  }

  function handlePrimaryAction() {
    if (currentStep === steps.length - 1) {
      void handleApply();
      return;
    }

    if (!canContinue) {
      saveError = applyBlockedMessage();
      return;
    }
    saveError = null;
    currentStep += 1;
  }

  async function refreshWritePreview(expectedKey = previewRequestKey) {
    previewing = true;
    previewError = null;
    writePreviewKey = expectedKey;

    try {
      const nextPreview = await previewProfileWrite(buildProfileDraftRequest());
      if (writePreviewKey === expectedKey) {
        writePreview = nextPreview;
      }
    } catch (err) {
      if (writePreviewKey === expectedKey) {
        writePreview = null;
        previewError = err instanceof Error ? err.message : String(err);
      }
    } finally {
      if (writePreviewKey === expectedKey) {
        previewing = false;
      }
    }
  }

  function actionLabel(action: string) {
    if (action === "staged_local_auth") return $t("common.save");
    if (action === "create") {
      return $t("common.create");
    }
    if (action === "update") {
      return $t("common.update");
    }
    if (action === "create_or_update") {
      return $t("common.save");
    }
    if (action === "not_modified") {
      return $t("common.noWrite");
    }
    if (action === "mode_dependent") {
      return $t("profiles.providerModeTitle");
    }
    if (action === "future_confirmation_required") {
      return $t("common.future");
    }
    if (action === "pending_keychain") {
      return $t("common.keychainLater");
    }
    if (action === "missing") {
      return $t("common.missing");
    }
    return action.replaceAll("_", " ");
  }

  function installStateLabel(state: InstallState) {
    return $t(`status.${state}` as TranslationKey);
  }

  function providerLabel(providerId: string) {
    return providerId === "official" ? $t("wizard.provider.official") : providerId;
  }

  function applyModeLabel(mode: ProviderApplyMode) {
    return mode === "config" ? $t("profiles.mode.config") : $t("profiles.mode.gateway");
  }

  function protocolLabel(value: string) {
    const normalized = normalizeProtocol(value);
    const option = protocolOptions.find((item) => item.id === normalized);
    return option ? $t(option.labelKey) : value;
  }

  function normalizeProtocol(value: string) {
    return value.trim();
  }

  function credentialDetailLabel(providerId: string, secretProvided: boolean) {
    if (providerId.trim() === "official") {
      return $t("wizard.check.officialLoginNoKey");
    }
    if (secretProvided) {
      return $t("wizard.check.credentialReady");
    }
    return $t("wizard.check.credentialMissing");
  }

  function writePreviewLabel(item: ProfileWritePreviewItem) {
    if (item.label === "Profile row") {
      return $t("wizard.preview.profileRow");
    }
    if (item.label === "Active tool profile pointer") {
      return $t("wizard.preview.activeProfilePointer");
    }
    if (item.label === "Credential") {
      return $t("wizard.preview.credential");
    }
    if (item.label.endsWith(" config")) {
      return $t("wizard.preview.toolConfig", { name: item.label.replace(/\sconfig$/, "") });
    }
    return item.label;
  }

  function writePreviewDetail(item: ProfileWritePreviewItem) {
    if (item.label === "Profile row") {
      return $t("wizard.preview.profileRowDetail", {
        protocol: protocolLabel(activeProtocol),
        provider: providerLabel(activeProvider)
      });
    }
    if (item.label === "Active tool profile pointer") {
      return $t(
        item.action === "update"
          ? "wizard.preview.activeProfilePointerAutoApplyDetail"
          : "wizard.preview.activeProfilePointerDetail"
      );
    }
    if (item.label === "Credential") {
      return codexOAuthConfig ? $t("codexAccount.saveDetail") : credentialDetailLabel(activeProvider, activeSecretProvided);
    }
    if (item.label.endsWith(" config")) {
      return $t("wizard.preview.toolConfigDetail");
    }
    return item.detail;
  }

  function warningLabel(warning: string) {
    const duplicateMatch = warning.match(/Profile id '([^']+)' already exists, so this draft will use '([^']+)'/);
    if (duplicateMatch) {
      return $t("wizard.warning.profileIdExists", {
        oldId: duplicateMatch[1],
        newId: duplicateMatch[2]
      });
    }
    const missingToolMatch = warning.match(/Tool '([^']+)' is not in (?:the preview|the local) registry/);
    if (missingToolMatch) {
      return $t("wizard.warning.toolNotInRegistry", { tool: missingToolMatch[1] });
    }
    return warning;
  }

  function errorLabel(message: string) {
    if (message.startsWith("codexAccount.")) return $t(codexAccountErrorKey(message));
    if (message === "Codex uses API Key / config file mode in XIASS Tools and cannot use Local Gateway mode.") {
      return $t("wizard.error.codexConfigOnly");
    }
    if (message === "Profile Name is required" || message === "Configuration name is required") {
      return $t("wizard.error.profileNameRequired");
    }
    if (message === "Base URL must start with http:// or https://") {
      return $t("wizard.error.baseUrlScheme");
    }
    if (message === "Base URL cannot contain whitespace") {
      return $t("wizard.error.baseUrlWhitespace");
    }
    if (message === "Base URL must include a host") {
      return $t("wizard.error.baseUrlHost");
    }
    if (message === "Unsupported Provider API protocol.") {
      return $t("wizard.error.protocolUnsupported");
    }
    const configProtocolMatch = message.match(/Config (?:file mode does|profiles do) not support (.+) for '([^']+)'\./);
    if (configProtocolMatch) {
      return $t("wizard.error.configProtocolUnsupported", {
        protocol: configProtocolMatch[1],
        tool: selectedToolLabel(configProtocolMatch[2])
      });
    }
    if (message === "Provider API key is required for non-official providers.") {
      return $t("wizard.check.credentialMissing");
    }
    if (message === "Claude Code model mappings require both alias and target model.") {
      return $t("profiles.modelMappingsInvalid");
    }
    const duplicateMappingMatch = message.match(/Claude Code model mapping alias '([^']+)' is duplicated\./);
    if (duplicateMappingMatch) {
      return $t("profiles.modelMappingsDuplicate", { alias: duplicateMappingMatch[1] });
    }
    if (message === "Official profiles are built in and cannot be saved as custom profiles.") {
      return $t("profiles.officialCustomSaveBlocked");
    }
    if (
      message === "Official provider uses the client login directly and cannot use Gateway mode." ||
      message === "Official provider uses the client login directly and cannot use Gateway profile." ||
      message === "Official provider uses the client login directly and cannot use Gateway profiles."
    ) {
      return $t("profiles.officialGatewayBlocked");
    }
    const toolNotInstalledMatch = message.match(/Tool '([^']+)' is not installed, so a profile cannot be created for it\./);
    if (toolNotInstalledMatch) {
      return $t("wizard.error.toolNotInstalled", {
        tool: selectedToolLabel(toolNotInstalledMatch[1])
      });
    }
    return message;
  }

  function toolStatusForProfileTool(toolId: string) {
    const canonicalToolId = canonicalProfileToolId(toolId);
    const matches = snapshot?.tools.filter((tool) => canonicalProfileToolId(tool.id) === canonicalToolId) ?? [];
    return matches.find((tool) => tool.installState === "installed") ?? matches[0] ?? null;
  }

  function toolVisibleInSnapshot(toolId: string) {
    if (!snapshot || canonicalProfileToolId(toolId) !== "gemini-code-assist") {
      return true;
    }
    return snapshot.tools.some((tool) => canonicalProfileToolId(tool.id) === "gemini-code-assist");
  }

  function toolCanCreateProfile(toolId: string) {
    return toolStatusForProfileTool(toolId)?.installState === "installed";
  }

  function normalizeBaseUrl(value: string) {
    return value.trim();
  }

  function handleBaseUrlInput(event: Event) {
    const value = (event.currentTarget as HTMLInputElement).value;
    baseUrl = value;
  }

  function normalizeBaseUrlInput() {
    baseUrl = normalizeBaseUrl(baseUrl);
  }

  function baseUrlValidationErrorKey(value: string): TranslationKey | null {
    const input = value.trim();
    const trimmed = normalizeBaseUrl(input);
    if (!input) {
      return "wizard.error.baseUrlRequired";
    }
    if (/\s/.test(input)) {
      return "wizard.error.baseUrlWhitespace";
    }
    if (!/^https?:\/\//i.test(trimmed)) {
        return "wizard.error.baseUrlScheme";
    }
    try {
      const parsed = new URL(trimmed);
      if (!["http:", "https:"].includes(parsed.protocol)) {
        return "wizard.error.baseUrlScheme";
      }
      if (!parsed.hostname || parsed.hostname.startsWith(".")) {
        return "wizard.error.baseUrlHost";
      }
    } catch {
      return "wizard.error.baseUrlHost";
    }
    return null;
  }

  function applyBlockedMessage() {
    if (!selectedToolInstalled) {
      return snapshot
        ? $t("wizard.error.toolNotInstalled", { tool: selectedToolLabel(selectedTool) })
        : $t("wizard.error.toolDetectionPending");
    }
    if (baseUrlErrorKey) {
      return $t(baseUrlErrorKey);
    }
    if (codexOAuthConfig && !codexOAuthAuthorized) {
      return $t("wizard.codexOAuth.authorizationRequired");
    }
    if (!modelMappingsValid) {
      return $t("profiles.modelMappingsInvalid");
    }
    return $t("wizard.applyRequired");
  }
</script>

<div class={routeStackRecipe({ width: "full" })}>
  <section class={topStripRecipe()}>
    <div>
      <h1>{$t(steps[currentStep])}</h1>
      <p>{$t("wizard.progress", { current: currentStep + 1 - firstStep, total: steps.length - firstStep })}{#if toolLocked} · Codex · {$t("profiles.mode.config")}{/if}</p>
    </div>
    <div class={wizardActionsRecipe()}>
      <button class={actionButtonRecipe()} title={$t("common.back")} disabled={saving || (!toolLocked && currentStep === 0)} on:click={goBack}>
        <AppIcon name="arrowLeft" size={16} />
        {$t("common.back")}
      </button>
      <button
        class={actionButtonRecipe({ tone: "primary" })}
        title={$t(currentStep === steps.length - 1 ? "common.save" : "common.next")}
        disabled={currentStep === steps.length - 1 ? !canApply || previewing : !canContinue}
        on:click={handlePrimaryAction}
      >
        {#if currentStep === steps.length - 1}
          {saving ? $t("common.saving") : $t("common.save")}
          <AppIcon name="check" tone="action" size={16} />
        {:else}
          {$t("common.next")}
          <AppIcon name="arrowRight" size={16} />
        {/if}
      </button>
    </div>
  </section>

  <div class={wizardStepperRecipe()}>
    {#each steps as step, index}
      {#if index >= firstStep}
      <div
        class={wizardStepItemRecipe()}
        data-step-state={index === currentStep ? "active" : index < currentStep ? "done" : "idle"}
        title={$t(step)}
        aria-current={index === currentStep ? "step" : undefined}
      >
        {#if index < currentStep}
          <AppIcon name="check" size={14} />
        {:else}
          <span>{index + 1 - firstStep}</span>
        {/if}
      </div>
      {/if}
    {/each}
  </div>

  <section class={wizardPanelRecipe()}>
    {#key currentStep}
    <div class={wizardStepContentRecipe()} in:fly={wizardStepEnter} out:fade={wizardStepExit}>
    {#if currentStep === 0}
      <div class={wizardPreviewHeadingRecipe()}>
        <div>
          <h2>{$t("wizard.chooseClientTitle")}</h2>
          <p>{$t("wizard.chooseClientDescription")}</p>
        </div>
      </div>
      <div class={wizardChoiceGridRecipe({ kind: "tool" })}>
        {#each visibleToolDefaults as tool}
          {@const toolStatus = toolStatusForProfileTool(tool.id)}
          {@const installed = toolCanCreateProfile(tool.id)}
          <button
            class={wizardChoiceButtonRecipe({ kind: "tool" })}
            data-selected={selectedTool === tool.id}
            disabled={!installed}
            title={installed ? tool.label : $t("wizard.error.toolNotInstalled", { tool: tool.label })}
            on:click={() => applyToolDefaults(tool.id, undefined, profileMode)}
          >
            <ToolIcon toolId={tool.id} label={tool.label} variant="choice" />
            <span>{tool.label}</span>
            <small>{toolStatus ? installStateLabel(toolStatus.installState) : $t("status.unknown")}</small>
          </button>
        {/each}
      </div>
      <div class={wizardModeChoiceRecipe()}>
        <strong>{$t("profiles.providerModeTitle")}</strong>
        <div class={wizardChoiceGridRecipe({ kind: "compact" })}>
          <button
            class={wizardChoiceButtonRecipe({ kind: "compact" })}
            data-selected={profileMode === "config"}
            type="button"
            on:click={() => setProfileMode("config")}
          >
            <AppIcon name="profiles" size={18} />
            <span>{$t("profiles.mode.config")}</span>
          </button>
          {#if !profileRequiresApiKeyConfig(selectedTool)}
            <button
              class={wizardChoiceButtonRecipe({ kind: "compact" })}
              data-selected={profileMode === "gateway"}
              type="button"
              on:click={() => setProfileMode("gateway")}
            >
              <AppIcon name="gateway" size={18} />
              <span>{$t("profiles.mode.gateway")}</span>
            </button>
          {/if}
        </div>
        {#if profileRequiresApiKeyConfig(selectedTool)}
          <small class={modelPickerStatusClass}>{$t("wizard.codexConfigOnlyHint")}</small>
        {/if}
      </div>
      {#if !selectedToolInstalled}
        <div class={wizardInlineNoticeRecipe({ tone: "error" })}>{applyBlockedMessage()}</div>
      {/if}
    {:else if currentStep === 1}
      <div class={wizardPreviewHeadingRecipe()}>
        <div>
          <h2>{$t("wizard.connectionTitle")}</h2>
          <p>{$t("wizard.connectionDescription", { mode: applyModeLabel(profileMode) })}</p>
        </div>
      </div>

      {#if canUseCodexOAuthConfig}
        <div class={wizardChoiceGridRecipe({ kind: "compact" })} role="group" aria-label={$t("codexAccount.title")}>
          <button
            class={wizardChoiceButtonRecipe({ kind: "compact" })}
            data-selected={!codexOAuthConfig}
            aria-pressed={!codexOAuthConfig}
            disabled={saving || codexAccountBusy}
            type="button"
            on:click={() => selectCodexOAuthConfig(false)}
          >
            <AppIcon name="key" size={18} />
            <span>{$t("wizard.codexOAuth.typeApi")}</span>
          </button>
          <button
            class={wizardChoiceButtonRecipe({ kind: "compact" })}
            data-selected={codexOAuthConfig && codexAccountSource === "oauth"}
            aria-pressed={codexOAuthConfig && codexAccountSource === "oauth"}
            disabled={saving || codexAccountBusy}
            type="button"
            on:click={() => selectCodexOAuthConfig(true)}
          >
            <AppIcon name="user" size={18} />
            <span>{$t("wizard.codexOAuth.typeOAuth")}</span>
          </button>
          <button class={wizardChoiceButtonRecipe({ kind: "compact" })} type="button"
            data-selected={codexOAuthConfig && codexAccountSource === "json"}
            aria-pressed={codexOAuthConfig && codexAccountSource === "json"}
            disabled={saving || codexAccountBusy} on:click={() => selectCodexOAuthConfig(true, "json")}>
            <AppIcon name="upload" size={18} /><span>{$t("codexAccount.importTitle")}</span>
          </button>
        </div>
      {/if}

      <div class={wizardFormGridRecipe()}>
        <label>
          {$t("wizard.profileName")}
          <input bind:value={profileName} />
          {#if nameErrorKey}<small class={wizardFieldErrorRecipe()}>{$t(nameErrorKey)}</small>{/if}
        </label>
        {#if !supportsReviewModel}
        <label class={wizardWideFieldRecipe()}>
          {$t("profiles.remarkLabel")}
          <textarea bind:value={profileRemark} rows="2" placeholder={$t("profiles.remarkPlaceholder")}></textarea>
        </label>
        {/if}
        {#if !codexOAuthConfig}
          <label>
            {$t(profileMode === "gateway" ? "wizard.upstreamApi" : "wizard.protocol")}
            <select bind:value={protocol}>
              {#each availableProtocolOptions as option}
                <option value={option.id}>{$t(option.labelKey)}</option>
              {/each}
            </select>
          </label>
          <label>
            {$t("wizard.providerApiKey")}
            <SecretInput bind:value={apiKey} />
          </label>
          <label>
            {$t("wizard.providerBaseUrl")}
            <input value={baseUrl} on:input={handleBaseUrlInput} on:blur={normalizeBaseUrlInput} />
            {#if visibleBaseUrlErrorKey}
              <small class={wizardFieldErrorRecipe()}>{$t(visibleBaseUrlErrorKey)}</small>
            {/if}
          </label>
          <div class={modelPickerClass}>
            <label for={`${modelListId}-input`}>{$t("wizard.modelOptional")}</label>
            <div class={modelPickerRowClass}>
              <ModelSelectInput
                id={`${modelListId}-input`}
                bind:value={model}
                options={modelOptions}
                optionLabel={modelOptionLabel}
                toggleTitle={$t("wizard.modelOptional")}
              />
              <button
                class={actionButtonRecipe()}
                type="button"
                data-refresh-button="true"
                disabled={modelFetchDisabled}
                title={$t("profiles.fetchModels")}
                on:click={refreshModels}
              >
                <AppIcon name={modelLoading ? "loading" : "refresh"} class={modelLoading ? spinRecipe() : ""} size={15} />
                {modelLoading ? $t("profiles.fetchingModels") : $t("profiles.fetchModels")}
              </button>
            </div>
            {#if modelStatus}
              <small class={modelPickerStatusClass}>{modelStatus}</small>
            {/if}
          </div>
          {#if supportsModelMappings}
            <section class={modelMappingPanelClass}>
              <div class={modelMappingHeaderClass}>
                <strong>{$t("profiles.modelMappingsTitle")}</strong>
                <button class={actionButtonRecipe()} type="button" on:click={addModelMapping}>
                  <AppIcon name="add" size={15} />
                  {$t("profiles.modelMappingAdd")}
                </button>
              </div>
              {#if modelMappings.length > 0}
                <div class={modelMappingRowsClass}>
                  {#each modelMappings as mapping, index}
                    <div class={modelMappingRowClass}>
                      <label>
                        {$t("profiles.modelMappingAlias")}
                        <input
                          value={mapping.alias}
                          on:input={(event) => updateModelMapping(index, { alias: event.currentTarget.value })}
                        />
                      </label>
                      <div class={modelMappingFieldClass}>
                        <label for={`${modelListId}-mapping-${index}`}>{$t("profiles.modelMappingTarget")}</label>
                        <ModelSelectInput
                          id={`${modelListId}-mapping-${index}`}
                          value={mapping.model}
                          options={modelOptions}
                          optionLabel={modelOptionLabel}
                          toggleTitle={$t("profiles.modelMappingTarget")}
                          on:input={(event) => updateModelMappingModel(index, event.detail.value)}
                        />
                      </div>
                      <label>
                        {$t("profiles.modelMappingDescription")}
                        <input
                          value={mapping.description}
                          on:input={(event) => updateModelMapping(index, { description: event.currentTarget.value })}
                        />
                      </label>
                      <label class={modelMappingToggleClass}>
                        <input
                          type="checkbox"
                          checked={mapping.supports1m}
                          on:change={(event) => updateModelMapping(index, { supports1m: event.currentTarget.checked })}
                        />
                        {$t("profiles.modelMappingSupports1m")}
                      </label>
                      <button class={actionButtonRecipe()} type="button" on:click={() => removeModelMapping(index)}>
                        <AppIcon name="delete" size={15} />
                        {$t("profiles.modelMappingRemove")}
                      </button>
                    </div>
                  {/each}
                </div>
                {#if !modelMappingsValid}
                  <small class={wizardFieldErrorRecipe()}>{$t("profiles.modelMappingsInvalid")}</small>
                {/if}
              {/if}
            </section>
          {/if}
        {/if}
        {#if codexOAuthConfig}
          <div class={modelPickerClass}>
            <label for={`${modelListId}-oauth-input`}>{$t("wizard.modelOptional")}</label>
            <ModelSelectInput
              id={`${modelListId}-oauth-input`}
              bind:value={accountModel}
              options={[]}
              optionLabel={modelOptionLabel}
              toggleTitle={$t("wizard.modelOptional")}
              placeholder={$t("wizard.modelOptional")}
            />
            <small class={modelPickerStatusClass}>{$t("wizard.codexOAuth.modelHint")}</small>
          </div>
        {/if}
        {#if supportsReviewModel}
          <div class={modelPickerClass}>
            <label for={`${modelListId}-review-input`}>{$t("profiles.reviewModelLabel")}</label>
            {#if codexOAuthConfig}
            <ModelSelectInput
              id={`${modelListId}-review-input`}
              bind:value={accountReviewModel}
              options={[]}
              toggleTitle={$t("profiles.reviewModelLabel")}
              placeholder={$t("profiles.reviewModelPlaceholder")}
            />
            {:else}
            <ModelSelectInput
              id={`${modelListId}-review-input`}
              bind:value={reviewModel}
              options={modelOptions}
              optionLabel={modelOptionLabel}
              toggleTitle={$t("profiles.reviewModelLabel")}
              placeholder={$t("profiles.reviewModelPlaceholder")}
            />
            {/if}
          </div>
        {/if}
        {#if canonicalProfileToolId(selectedTool) === "codex"}
          <section class={codexCapabilityPanelClass} aria-label={$t("wizard.contextWindow")}>
            <strong>{$t("wizard.contextWindow")} &amp; model capabilities</strong>
            <small>{$t("wizard.contextWindowHint")}</small>
            <div class="codex-capability-grid">
              <label>
                {$t("wizard.webSearch")}
                <select bind:value={webSearch}>
                  <option value="live">{$t("wizard.webSearchLive")}</option>
                  <option value="cached">{$t("wizard.webSearchCached")}</option>
                  <option value="indexed">{$t("wizard.webSearchIndexed")}</option>
                  <option value="disabled">{$t("wizard.webSearchDisabled")}</option>
                </select>
              </label>
              <label>
                {$t("wizard.imageModel")}
                <input bind:value={imageModel} placeholder="例如 gpt-image-1 / imagen-4" />
                <small>{$t("wizard.imageModelHint")}</small>
              </label>
              <label>
                {$t("wizard.contextWindow")}
                <select bind:value={contextMode} on:change={(event) => applyContextPreset(event.currentTarget.value)}>
                  <option value="235000">235K</option>
                  <option value="372000">372K</option>
                  <option value="512000">512K</option>
                  <option value="1000000">1M</option>
                  <option value="custom">{$t("wizard.contextCustom")}</option>
                </select>
                {#if contextMode === "custom"}
                  <input type="number" min="64000" max="1050000" step="1000" value={modelContextWindow} on:input={(event) => updateContextWindow(event.currentTarget.value)} />
                {/if}
              </label>
              <label>
                {$t("wizard.contextAutoCompact")}
                <input type="number" min="16000" max="1049000" step="1000" value={modelAutoCompactTokenLimit} on:input={(event) => updateAutoCompactLimit(event.currentTarget.value)} />
                <small>{$t("wizard.contextAutoCompactLinked")}: {Math.floor(modelContextWindow * 0.9).toLocaleString()}</small>
              </label>
            </div>
          </section>
        {/if}
      </div>
      {#if codexOAuthConfig}
        {#key codexAccountSource}
          <CodexAccountPanel mode={codexAccountSource} bind:session={codexAccountSession}
            bind:accountIndex={codexAccountIndex} bind:busy={codexAccountBusy} disabled={saving} />
        {/key}
      {:else}
        <div class={wizardSecurityNoteRecipe()}>
          <AppIcon name="key" size={18} />
          {$t("wizard.securityNote")}
        </div>
      {/if}
    {:else if currentStep === 2}
      <div class={wizardPreviewBoxRecipe()}>
        <div class={wizardPreviewHeadingRecipe()}>
          <div>
            <h2>{$t("wizard.writePreview")}</h2>
            {#if writePreview}
              <p>{writePreview.profileId} / {new Date(writePreview.generatedAt).toLocaleString()}</p>
            {:else}
              <p>{previewing ? $t("wizard.buildingPreview") : $t("wizard.reviewBeforeSaving")}</p>
            {/if}
          </div>
        </div>

        {#if previewError}
          <div class={wizardInlineNoticeRecipe({ tone: "error" })}>{errorLabel(previewError)}</div>
        {/if}
        {#if saveError}
          <div class={wizardInlineNoticeRecipe({ tone: "error" })}>{saveError}</div>
        {/if}
        {#if savedProfileName}
          <div class={wizardInlineNoticeRecipe({ tone: "success" })}>{$t("wizard.savedProfile", { name: savedProfileName })}</div>
        {/if}

        {#if writePreview}
          <div class={wizardWritePreviewListRecipe()}>
            {#each writePreview.items as item}
              <div class={wizardWritePreviewRowRecipe()}>
                <strong>{writePreviewLabel(item)}</strong>
                <span class={wizardWritePreviewMetaRecipe()}>
                  <b>{actionLabel(item.action)}</b>
                </span>
                {#if item.path}
                  <code>{item.path}</code>
                {/if}
                <span>{writePreviewDetail(item)}</span>
                {#if item.content}
                  <div class={wizardWriteContentPreviewRecipe()}>
                    <strong>{$t("wizard.writeContentPreview")}</strong>
                    <pre>{item.content}</pre>
                  </div>
                {/if}
              </div>
            {/each}
          </div>

          {#if writePreview.warnings.length > 0}
            <div class={wizardPreviewWarningsRecipe()}>
              {#each writePreview.warnings as warning}
                <span>{warningLabel(warning)}</span>
              {/each}
            </div>
          {/if}
        {/if}
      </div>
    {/if}
    </div>
    {/key}
  </section>
</div>
