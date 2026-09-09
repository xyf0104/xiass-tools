<script lang="ts">
  import { onMount, tick } from "svelte";
  import {
    applyProfile,
    openChatGPTDesktopPath,
  } from "../lib/api";
  import {
    chatgptDesktopView,
    installOrUpdateChatGPTDesktop,
    launchManagedChatGPTDesktop,
    refreshChatGPTDesktop,
    removeChatGPTDesktop,
    setChatGPTDesktopConfirmUninstall,
    setChatGPTDesktopSelectedKind,
    stageChatGPTDesktopPackage,
    startChatGPTDesktopProgressListener,
    updateChatGPTDesktopDraft,
    type ChatGPTDesktopNoticeMessage
  } from "../lib/chatgptDesktopStore";
  import {
    brandChatGPTDesktopText,
    chatgptDesktopGeneration
  } from "../lib/chatgptDesktopBranding";
  import { t, type TranslationKey } from "../lib/i18n";
  import AppIcon from "../components/AppIcon.svelte";
  import DismissibleNotice from "../components/DismissibleNotice.svelte";
  import StatusPill from "../components/StatusPill.svelte";
  import ToolIcon from "../components/ToolIcon.svelte";
  import { activeDesktopCodexProfileId, desktopCodexProfiles, resolveDesktopCodexSelection } from "../lib/chatgptDesktopProfiles";
  import { profileDisplayName, profileIconIsImage, profileIconValue, profileUsesToolIcon, providerIsOfficial } from "../lib/profiles/presentation";
  import { css, cx } from "../../styled-system/css";
  import {
    actionButtonRecipe,
    desktopClientActionsRecipe,
    desktopClientModalActionsRecipe,
    desktopClientModalBackdropRecipe,
    desktopClientModalBodyRecipe,
    desktopClientModalPanelRecipe,
    desktopClientMetricsRecipe,
    desktopClientPreviewListRecipe,
    desktopClientProgressRecipe,
    desktopClientSettingsListRecipe,
    desktopClientTabsRecipe,
    doctorListRecipe,
    doctorRowRecipe,
    emptyRowRecipe,
    nativeToggleRecipe,
    panelRecipe,
    profileAvatarRecipe,
    routeStackRecipe,
    sectionHeadingRecipe,
    spinRecipe,
    statusStripRecipe,
    topActionsRecipe,
    topStripRecipe
  } from "../../styled-system/recipes";
  import type {
    ChatGPTDesktopProgress,
    ProfileDraft,
    ProfileSummary,
    Severity
  } from "../types";

  export let profileSummary: ProfileSummary | null = null;
  export let selectedCodexProfileId: string | null = null;
  export let onCreateCodexProfile: () => void = () => {};
  export let onProfileApplied: (summary: ProfileSummary) => void | Promise<void> = () => {};

  let activeSection: "select" | "launch" | "utilities" = "select";
  let profileApplying = false;
  let profileApplyError: string | null = null;
  let workflowHeading: HTMLHeadingElement | undefined;

  async function changeSection(section: typeof activeSection) {
    if (workflowBusy || (section === "launch" && !selectedProfile)) return;
    activeSection = section;
    await tick();
    workflowHeading?.focus({ preventScroll: true });
    workflowHeading?.scrollIntoView?.({ block: "nearest", behavior: "instant" });
  }
  $: codexProfiles = desktopCodexProfiles(profileSummary);
  $: activeCodexProfileId = activeDesktopCodexProfileId(profileSummary);
  $: if (profileSummary) selectedCodexProfileId = resolveDesktopCodexSelection(profileSummary, selectedCodexProfileId);
  $: selectedProfile = codexProfiles.find((profile) => profile.id === selectedCodexProfileId) ?? null;
  $: workflowBusy = profileApplying || Object.values(view.kindViews).some((kind) => kind.busyAction !== null);
  // Installation started from the launchpad must still expose its progress.
  $: if (busyAction === "install" || busyAction === "stage") activeSection = "utilities";

  $: view = $chatgptDesktopView;
  $: installKinds = view.installKinds;
  $: selectedKind = view.selectedKind;
  $: effectiveSelectedKind = selectedKind;
  $: kindView = view.kindViews[effectiveSelectedKind];
  $: state = kindView.state;
  $: settingsDraft = view.settingsDraft;
  $: busyAction = kindView.busyAction;
  $: error = view.error;
  $: success = view.success;
  $: stageReport = kindView.stageReport;
  $: operationResult = kindView.operationResult;
  $: progress = kindView.progress;
  $: confirmUninstall = view.confirmUninstall;
  $: installed = state?.installed ?? null;
  $: plan = state?.plan ?? null;
  $: release = state?.release ?? null;
  $: planRefreshing = kindView.planRefreshing;
  $: planUnavailable = kindView.planStale;
  $: planUnavailableText = $t("chatgptDesktop.planStale");
  $: effectivePlan = planUnavailable ? null : plan;
  $: effectiveRelease = planUnavailable ? null : release;
  $: platform = state?.platform ?? view.kindViews.msix.state?.platform ?? view.kindViews.portable.state?.platform;
  $: isWindows = platform === "windows";
  $: isMacos = platform === "macos";
  $: statusLabel = installed ? $t("common.installed") : $t("common.missing");
  $: statusTone = (installed ? "ok" : "warning") as Severity;
  $: canStage = Boolean(effectivePlan && !effectivePlan.upToDate && effectivePlan.route !== "unsupported");
  $: canInstall = canStage;
  $: canLaunch = Boolean(installed);
  $: canUninstall = Boolean(installed);
  $: progressPercent = progress?.percent ?? null;
  $: progressStepLabel = progress?.step && progress.stepTotal
    ? $t("chatgptDesktop.progressStep", { current: progress.step, total: progress.stepTotal })
    : "";
  $: showProgress = Boolean(progress && (
    busyAction === "stage"
    || busyAction === "install"
    || progress.phase === "done"
    || progress.phase === "error"
  ));
  onMount(() => {
    startChatGPTDesktopProgressListener();
  });

  function brandDesktopText(value: string) {
    return brandChatGPTDesktopText(value, $chatgptDesktopGeneration);
  }

  async function stagePackage() {
    await stageChatGPTDesktopPackage();
  }

  async function installOrUpdate() {
    await installOrUpdateChatGPTDesktop();
  }

  async function removeCodex() {
    await removeChatGPTDesktop();
  }

  async function launchCodex() {
    if (!selectedProfile || !canLaunch || workflowBusy) return;
    const profileId = selectedProfile.id;
    profileApplying = true;
    profileApplyError = null;
    dismissSuccess();
    try {
      const result = await applyProfile({ profileId, restartAfterApply: false, reapply: true });
      if (!result.verified || !result.nativeVerified || result.mode !== "config") {
        throw new Error($t("chatgptDesktop.configVerificationFailed"));
      }
      profileSummary = result.summary;
      await onProfileApplied(result.summary);
      await launchManagedChatGPTDesktop(true);
    } catch (err) {
      profileApplyError = err instanceof Error ? err.message : String(err);
    } finally {
      profileApplying = false;
    }
  }

  function displayProfileName(profile: ProfileDraft) {
    return profileDisplayName(profile, $t("profiles.officialProfile.codex"));
  }

  async function refreshCodex() {
    await refreshChatGPTDesktop();
  }

  function formatBytes(value: number | null | undefined) {
    if (!value) {
      return $t("common.unknown");
    }
    const units = ["B", "KB", "MB", "GB"];
    let size = value;
    let unit = 0;
    while (size >= 1024 && unit < units.length - 1) {
      size /= 1024;
      unit += 1;
    }
    return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
  }

  function progressPhaseLabel(value: string) {
    if (value === "error") {
      return $t("common.error");
    }
    const key = `chatgptDesktop.phase.${value}` as Parameters<typeof $t>[0];
    const label = $t(key);
    return label === key ? value : label;
  }

  function formatNoticeMessage(message: ChatGPTDesktopNoticeMessage | null) {
    if (!message) {
      return "";
    }
    if (typeof message === "string") {
      return message;
    }
    return $t(message.key, message.values);
  }

  function formatProgressMessage(message: string) {
    if (message.startsWith("chatgptDesktop.")) {
      return $t(message as TranslationKey);
    }
    return message;
  }

  function progressByteLabel(value: ChatGPTDesktopProgress) {
    if (value.downloaded !== null && value.total !== null) {
      return $t("chatgptDesktop.progressBytes", {
        downloaded: formatBytes(value.downloaded),
        total: formatBytes(value.total)
      });
    }
    if (value.downloaded !== null) {
      return $t("chatgptDesktop.progressDownloaded", {
        downloaded: formatBytes(value.downloaded)
      });
    }
    return $t("chatgptDesktop.progressWorking");
  }

  function dismissError() {
    chatgptDesktopView.update((current) => ({ ...current, error: null }));
  }

  function dismissSuccess() {
    chatgptDesktopView.update((current) => ({ ...current, success: null }));
  }

  const headingCopyClass = css({
    minWidth: 0
  });
  const sectionActionsClass = css({
    display: "flex",
    alignItems: "center",
    justifyContent: "flex-end",
    gap: "9px",
    flexWrap: "wrap",
    minWidth: 0
  });
  const inlineEmptyRowClass = css({
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    gap: "8px"
  });
  const warningRowClass = css({
    borderColor: "color-mix(in srgb, var(--amber) 35%, transparent) !important",
    background: "color-mix(in srgb, var(--amber) 8%, var(--surface-strong)) !important"
  });
</script>

<div class={cx(routeStackRecipe({ width: "desktopClient" }), "desktop-workflow")}>
  <section class={cx(topStripRecipe({ compact: true }), "desktop-workflow-header")}>
    <div>
      <h1>{$t("chatgptDesktop.title")}</h1>
      <p>{$t("chatgptDesktop.workflowSubtitle")}</p>
      <div class={statusStripRecipe()}>
        <StatusPill status={statusTone} label={statusLabel} />
        <span>{state ? $t("dashboard.lastScan", { time: new Date(state.generatedAt).toLocaleString() }) : $t("dashboard.waitingForScan")}</span>
      </div>
    </div>
    <div class={cx(topActionsRecipe(), "desktop-header-actions")}>
      <button class={actionButtonRecipe({ tone: "primary" })} disabled={workflowBusy} on:click={onCreateCodexProfile}>
        <AppIcon name="add" size={16} />
        {$t("common.createConfig")}
      </button>
      <button class={actionButtonRecipe()} data-refresh-button="true" disabled={kindView.loading || workflowBusy} on:click={refreshCodex}>
        <AppIcon name={kindView.loading ? "loading" : "refresh"} size={15} class={kindView.loading ? spinRecipe() : ""} />
        {$t(kindView.loading ? "common.refreshing" : "common.refresh")}
      </button>
    </div>
  </section>

  <nav class={cx(panelRecipe(), "desktop-workflow-nav")} aria-label={$t("chatgptDesktop.workflowNavigation")}>
    <button class={actionButtonRecipe()} data-active={activeSection === "select"} aria-current={activeSection === "select" ? "step" : undefined} disabled={workflowBusy} on:click={() => changeSection("select")}>
      <AppIcon name="profiles" tone="info" size={16} />
      1 · {$t("chatgptDesktop.selectConfig")}
    </button>
    <button class={actionButtonRecipe()} data-active={activeSection === "launch"} aria-current={activeSection === "launch" ? "step" : undefined} disabled={!selectedProfile || workflowBusy} on:click={() => changeSection("launch")}>
      <AppIcon name="play" tone="action" size={16} />
      2 · {$t("chatgptDesktop.launch")}
    </button>
    <button class={actionButtonRecipe()} data-active={activeSection === "utilities"} aria-current={activeSection === "utilities" ? "page" : undefined} disabled={workflowBusy} on:click={() => changeSection("utilities")}>
      <AppIcon name="settings" tone="violet" size={16} />
      {$t("chatgptDesktop.utilities")}
    </button>
  </nav>

  {#if activeSection !== "select" && isWindows && installKinds}
    <div class={desktopClientTabsRecipe()} role="tablist">
      <button
        role="tab"
        data-selected={effectiveSelectedKind === "msix"}
        aria-selected={effectiveSelectedKind === "msix"}
        disabled={workflowBusy}
        on:click={() => setChatGPTDesktopSelectedKind("msix")}
      >
        {$t("desktopClient.kind.windowsApp")}
      </button>
      <button
        role="tab"
        data-selected={effectiveSelectedKind === "portable"}
        aria-selected={effectiveSelectedKind === "portable"}
        disabled={workflowBusy}
        on:click={() => setChatGPTDesktopSelectedKind("portable")}
      >
        {$t("desktopClient.kind.exe")}
      </button>
    </div>
  {/if}

  {#if error}
    <DismissibleNotice tone="error" message={brandDesktopText(error)} on:dismiss={dismissError} />
  {/if}
  {#if success}
    <DismissibleNotice tone="success" message={brandDesktopText(formatNoticeMessage(success))} on:dismiss={dismissSuccess} />
  {/if}

  {#if profileApplyError}
    <DismissibleNotice tone="error" message={profileApplyError} on:dismiss={() => profileApplyError = null} />
  {/if}

  {#if activeSection === "select"}
    <section class={panelRecipe()} aria-labelledby="desktop-select-title">
      <div class={cx(sectionHeadingRecipe(), "desktop-workflow-heading")}>
        <div class={headingCopyClass}>
          <h2 id="desktop-select-title" tabindex="-1" bind:this={workflowHeading}>{$t("chatgptDesktop.selectConfig")}</h2>
          <p>{$t("chatgptDesktop.selectConfigHint")}</p>
        </div>
      </div>
      <div class="desktop-profile-list" role="group" aria-label={$t("chatgptDesktop.selectConfig")}>
        {#each codexProfiles as profile (profile.id)}
          {@const name = displayProfileName(profile)}
          {@const icon = profileIconValue(profile, name)}
          <button type="button" class="desktop-profile-option" data-selected={selectedCodexProfileId === profile.id}
            aria-pressed={selectedCodexProfileId === profile.id} disabled={workflowBusy}
            on:click={() => { selectedCodexProfileId = profile.id; profileApplyError = null; }}>
            <span class={profileAvatarRecipe()} aria-hidden="true">
              {#if profileUsesToolIcon(profile)}
                <ToolIcon toolId="codex" label={name} variant="heading" />
              {:else if profileIconIsImage(icon)}<img src={icon} alt="" />
              {:else}<span>{icon}</span>{/if}
            </span>
            <span class="desktop-profile-copy">
              <strong>{name}</strong>
              <span>{providerIsOfficial(profile.provider) ? $t("profiles.officialProfileEndpoint") : profile.baseUrl}</span>
              {#if profile.model}<small>{$t("common.model")}: {profile.model}</small>{/if}
            </span>
            <span class="desktop-profile-flags">
              {#if activeCodexProfileId === profile.id}<span class="desktop-profile-active">{$t("common.active")}</span>{/if}
              <span class="desktop-selection-mark" data-checked={selectedCodexProfileId === profile.id}>
                {#if selectedCodexProfileId === profile.id}<AppIcon name="check" tone="success" size={18} />{/if}
              </span>
            </span>
          </button>
        {:else}
          <div class={emptyRowRecipe()}>{profileSummary ? $t("chatgptDesktop.noCodexProfiles") : $t("common.loading")}</div>
        {/each}
      </div>
      <div class={cx(desktopClientActionsRecipe(), "desktop-workflow-actions")}>
        <button class={actionButtonRecipe({ tone: "primary" })} disabled={!selectedProfile || workflowBusy} on:click={() => changeSection("launch")}>
          {$t("common.next")}<AppIcon name="arrowRight" size={16} />
        </button>
      </div>
    </section>
  {:else if activeSection === "launch"}
    <section class={panelRecipe()} aria-labelledby="desktop-launch-title">
      <div class={cx(sectionHeadingRecipe(), "desktop-workflow-heading")}>
        <div class={headingCopyClass}>
          <h2 id="desktop-launch-title" tabindex="-1" bind:this={workflowHeading}>{$t("chatgptDesktop.launchWithConfig")}</h2>
          <p>{$t("chatgptDesktop.launchWithConfigHint")}</p>
        </div>
      </div>
      {#if selectedProfile}
        <dl class="desktop-launch-summary" aria-label={$t("chatgptDesktop.launchWithConfig")}>
          <div><dt>{$t("wizard.profileName")}</dt><dd>{displayProfileName(selectedProfile)}</dd></div>
          <div><dt>{$t("wizard.providerBaseUrl")}</dt><dd>{providerIsOfficial(selectedProfile.provider) ? $t("profiles.officialProfileEndpoint") : selectedProfile.baseUrl}</dd></div>
          <div><dt>{$t("common.model")}</dt><dd>{selectedProfile.model || $t("chatgptDesktop.clientDefaultModel")}</dd></div>
        </dl>
        {#if providerIsOfficial(selectedProfile.provider)}
          <p class="desktop-launch-hint">{$t("chatgptDesktop.officialLoginHint")}</p>
        {/if}
      {/if}
      {#if !canLaunch}<p class="desktop-launch-hint">{$t("chatgptDesktop.installBeforeLaunch")}</p>{/if}
      <div class={cx(desktopClientActionsRecipe(), "desktop-workflow-actions")}>
        <button class={actionButtonRecipe()} disabled={workflowBusy} on:click={() => changeSection("select")}>
          <AppIcon name="arrowLeft" size={16} />{$t("chatgptDesktop.backToSelection")}
        </button>
        {#if canLaunch}
          <button class={actionButtonRecipe({ tone: "primary" })} disabled={!selectedProfile || workflowBusy} on:click={launchCodex}>
            <AppIcon name={workflowBusy ? "loading" : "play"} size={16} class={workflowBusy ? spinRecipe() : ""} />
            {profileApplying && busyAction !== "launch" ? $t("chatgptDesktop.applyingConfig") : busyAction === "launch" ? $t("toolLaunch.starting") : $t("chatgptDesktop.launch")}
          </button>
        {:else}
          <button class={actionButtonRecipe({ tone: "primary" })} disabled={workflowBusy} on:click={() => changeSection("utilities")}>
            <AppIcon name="download" size={16} />{$t("chatgptDesktop.openInstallTools")}
          </button>
        {/if}
        <button class={actionButtonRecipe()} disabled={workflowBusy} on:click={() => changeSection("utilities")}>
          <AppIcon name="settings" size={16} />{$t("chatgptDesktop.utilities")}
        </button>
      </div>
    </section>
  {/if}

  {#if activeSection === "utilities"}

  <section class={panelRecipe()}>
    <div class={sectionHeadingRecipe()}>
      <div class={headingCopyClass}>
        <h2 tabindex="-1" bind:this={workflowHeading}>{$t("chatgptDesktop.launchOptionsTitle")}</h2>
      </div>
    </div>
    {#if settingsDraft}
      <div class={desktopClientSettingsListRecipe({ layout: "grid" })}>
        <label class={nativeToggleRecipe()} data-native-toggle>
          <input
            type="checkbox"
            checked={settingsDraft.syncHistoryOnLaunch}
            on:change={(event) => updateChatGPTDesktopDraft({ syncHistoryOnLaunch: event.currentTarget.checked })}
          />
          <span>
            <strong>{$t("chatgptDesktop.syncHistoryOnLaunch")}</strong>
          </span>
        </label>
        <label class={nativeToggleRecipe()} data-native-toggle>
          <input
            type="checkbox"
            checked={settingsDraft.pluginMarketplaceUnlockOnLaunch}
            on:change={(event) => updateChatGPTDesktopDraft({ pluginMarketplaceUnlockOnLaunch: event.currentTarget.checked })}
          />
          <span>
            <strong>{$t("chatgptDesktop.pluginMarketplaceUnlockOnLaunch")}</strong>
          </span>
        </label>
        <label class={nativeToggleRecipe()} data-native-toggle>
          <input
            type="checkbox"
            checked={settingsDraft.officialRemotePluginCacheOnLaunch}
            on:change={(event) => updateChatGPTDesktopDraft({ officialRemotePluginCacheOnLaunch: event.currentTarget.checked })}
          />
          <span>
            <strong>{$t("chatgptDesktop.officialRemotePluginCacheOnLaunch")}</strong>
            <small>{$t("chatgptDesktop.officialRemotePluginCacheOnLaunchHint")}</small>
          </span>
        </label>
        <label class={nativeToggleRecipe()} data-native-toggle>
          <input
            type="checkbox"
            checked={settingsDraft.pluginAutoExpandOnLaunch}
            on:change={(event) => updateChatGPTDesktopDraft({ pluginAutoExpandOnLaunch: event.currentTarget.checked })}
          />
          <span>
            <strong>{$t("chatgptDesktop.pluginAutoExpandOnLaunch")}</strong>
          </span>
        </label>
        <label class={nativeToggleRecipe()} data-native-toggle>
          <input
            type="checkbox"
            checked={settingsDraft.modelWhitelistUnlockOnLaunch}
            on:change={(event) => updateChatGPTDesktopDraft({ modelWhitelistUnlockOnLaunch: event.currentTarget.checked })}
          />
          <span>
            <strong>{$t("chatgptDesktop.modelWhitelistUnlockOnLaunch")}</strong>
          </span>
        </label>
        <label class={nativeToggleRecipe()} data-native-toggle>
          <input
            type="checkbox"
            checked={settingsDraft.serviceTierControlsOnLaunch}
            on:change={(event) => updateChatGPTDesktopDraft({ serviceTierControlsOnLaunch: event.currentTarget.checked })}
          />
          <span>
            <strong>{$t("chatgptDesktop.serviceTierControlsOnLaunch")}</strong>
          </span>
        </label>
        {#if isWindows}
          <label class={nativeToggleRecipe()} data-native-toggle>
            <input
              type="checkbox"
              checked={settingsDraft.computerUseGuardOnLaunch}
              on:change={(event) => updateChatGPTDesktopDraft({ computerUseGuardOnLaunch: event.currentTarget.checked })}
            />
            <span>
              <strong>{$t("chatgptDesktop.computerUseGuardOnLaunch")}</strong>
              <small>{$t("chatgptDesktop.computerUseGuardOnLaunchHint")}</small>
            </span>
          </label>
        {/if}
      </div>
    {/if}
  </section>

  <section class={panelRecipe()}>
    <div class={sectionHeadingRecipe()}>
      <div class={headingCopyClass}>
        <h2>{$t("chatgptDesktop.statusTitle")}</h2>
        <p>{installed?.path ?? $t("chatgptDesktop.notInstalled")}</p>
      </div>
      <StatusPill status={statusTone} label={statusLabel} />
    </div>
    <div class={desktopClientMetricsRecipe()}>
      <div>
        <span>{$t("chatgptDesktop.currentVersion")}</span>
        <strong>{installed?.version ?? $t("common.none")}</strong>
        <small>{installed?.source ?? $t("common.unknown")}</small>
      </div>
      <div>
        <span>{$t("chatgptDesktop.latestVersion")}</span>
        <strong>{effectiveRelease?.version ?? $t("common.unknown")}</strong>
        <small>{planUnavailable ? planUnavailableText : effectiveRelease?.packageMoniker ?? $t("chatgptDesktop.planNotLoaded")}</small>
      </div>
      <div>
        <span>{$t("chatgptDesktop.packageSize")}</span>
        <strong>{formatBytes(effectivePlan?.downloadSize ?? effectiveRelease?.contentLength)}</strong>
        <small>{effectiveRelease?.sha256 ? `${effectiveRelease.sha256.slice(0, 12)}...` : $t("common.unknown")}</small>
      </div>
    </div>
    <div class={desktopClientActionsRecipe()}>
      <button class={actionButtonRecipe()} disabled={!canStage || workflowBusy} on:click={stagePackage}>
        <AppIcon name="download" size={16} />
        {busyAction === "stage" ? $t("chatgptDesktop.staging") : $t("chatgptDesktop.stage")}
      </button>
      <button class={actionButtonRecipe()} on:click={() => openChatGPTDesktopPath("staging")}>
        <AppIcon name="folder" size={16} />
        {$t("chatgptDesktop.openStagingPath")}
      </button>
      <button class={actionButtonRecipe({ tone: "primary" })} disabled={!canInstall || workflowBusy} on:click={installOrUpdate}>
        <AppIcon name="rocket" size={16} />
        {busyAction === "install" ? $t("chatgptDesktop.installing") : installed ? $t("chatgptDesktop.update") : $t("chatgptDesktop.install")}
      </button>
      <button class={actionButtonRecipe()} disabled={!canUninstall || workflowBusy} on:click={() => setChatGPTDesktopConfirmUninstall(true)}>
        <AppIcon name="delete" size={16} />
        {$t("common.uninstall")}
      </button>
    </div>
    {#if showProgress && progress}
      <div class={desktopClientProgressRecipe()} aria-live="polite">
        <div data-desktop-client-progress-copy>
          <strong>{progressStepLabel ? `${progressStepLabel} / ${progressPhaseLabel(progress.phase)}` : progressPhaseLabel(progress.phase)}</strong>
          <span>{formatProgressMessage(progress.message)}</span>
        </div>
        <div data-desktop-client-progress-track data-indeterminate={progressPercent === null}>
          <span
            data-desktop-client-progress-fill
            style={`width: ${progressPercent === null ? 38 : Math.max(2, Math.min(100, progressPercent)).toFixed(1)}%`}
          ></span>
        </div>
        <div data-desktop-client-progress-meta>
          <span>{progressPercent === null ? $t("chatgptDesktop.progressUnknown") : `${progressPercent.toFixed(0)}%`}</span>
          <span>{progressByteLabel(progress)}</span>
        </div>
      </div>
    {/if}
  </section>

  <section class={panelRecipe()}>
    <div class={sectionHeadingRecipe()}>
      <div class={headingCopyClass}>
        <h2>{$t("chatgptDesktop.planTitle")}</h2>
        <p>{planUnavailable ? planUnavailableText : effectiveRelease?.manifestUrl ?? $t("chatgptDesktop.planNotLoaded")}</p>
      </div>
      <div class={sectionActionsClass}>
        {#if planUnavailable}
          <StatusPill status="info" label={planUnavailableText} />
        {:else if effectivePlan}
          <StatusPill status={effectivePlan.upToDate ? "ok" : "warning"} label={effectivePlan.upToDate ? $t("chatgptDesktop.upToDate") : $t("chatgptDesktop.updateAvailable")} />
        {/if}
      </div>
    </div>
    <div class={desktopClientPreviewListRecipe()}>
      {#if planUnavailable}
        <div class={cx(emptyRowRecipe(), inlineEmptyRowClass)}>
          <AppIcon name={planRefreshing ? "loading" : "info"} class={planRefreshing ? spinRecipe() : ""} size={18} />
          {planUnavailableText}
        </div>
      {:else if effectivePlan}
        <div>
          <strong>{$t("chatgptDesktop.downloadUrl")}</strong>
          <span>{effectivePlan.packageUrl}</span>
        </div>
        <div>
          <strong>SHA-256</strong>
          <span>{effectivePlan.sha256}</span>
        </div>
        <div>
          <strong>{$t("chatgptDesktop.installRoot")}</strong>
          <span>{effectivePlan.installRoot ?? $t("common.none")}</span>
        </div>
        {#if stageReport}
          <div>
            <strong>{$t("chatgptDesktop.stageReport")}</strong>
            <span>
              {stageReport.stagedPath ?? $t("common.none")} / {formatBytes(stageReport.downloadSize)}
              / {stageReport.hashVerified ? $t("chatgptDesktop.hashVerified") : $t("common.error")}
            </span>
          </div>
        {/if}
        {#if operationResult}
          <div>
            <strong>{$t("chatgptDesktop.lastOperation")}</strong>
            <span>{operationResult.action} / {brandDesktopText(operationResult.notes.join(" "))}</span>
          </div>
        {/if}
        {#each effectivePlan.warnings as warning}
          <div class={warningRowClass}>
            <strong>{$t("status.warning")}</strong>
            <span>{brandDesktopText(warning)}</span>
          </div>
        {/each}
      {:else}
        <div class={emptyRowRecipe()}>{$t("chatgptDesktop.planNotLoaded")}</div>
      {/if}
    </div>
  </section>

  <section class={panelRecipe()}>
    <div class={sectionHeadingRecipe()}>
      <div class={headingCopyClass}>
        <h2>{$t("chatgptDesktop.capabilities")}</h2>
        <p>{$t("chatgptDesktop.capabilityHint")}</p>
      </div>
    </div>
    <div class={doctorListRecipe()}>
      {#if planUnavailable}
        <div class={cx(emptyRowRecipe(), inlineEmptyRowClass)}>
          <AppIcon name={planRefreshing ? "loading" : "info"} class={planRefreshing ? spinRecipe() : ""} size={18} />
          {planUnavailableText}
        </div>
      {:else}
        {#each effectivePlan?.capabilities ?? [] as capability}
          <div class={doctorRowRecipe()}>
            <StatusPill status={capability.status} label={$t(`status.${capability.status}` as Parameters<typeof $t>[0])} />
            <div>
              <h3>{brandDesktopText(capability.label)}</h3>
              <p>{brandDesktopText(capability.detail)}</p>
            </div>
          </div>
        {:else}
          <div class={emptyRowRecipe()}>{$t("chatgptDesktop.capabilityEmpty")}</div>
        {/each}
      {/if}
    </div>
  </section>

  <section class={panelRecipe()}>
    <div class={sectionHeadingRecipe()}>
      <div class={headingCopyClass}>
        <h2>{$t("chatgptDesktop.settingsTitle")}</h2>
        <p>{$t("chatgptDesktop.settingsHint")}</p>
      </div>
    </div>
    {#if settingsDraft}
      <div class={desktopClientSettingsListRecipe()}>
        {#if isMacos}
          <label>
            {$t("chatgptDesktop.source")}
            <select
              value={settingsDraft.source}
              on:change={(event) => updateChatGPTDesktopDraft({ source: event.currentTarget.value as "mirror" | "official" })}
            >
              <option value="mirror">{$t("chatgptDesktop.source.mirror")}</option>
              <option value="official">{$t("chatgptDesktop.source.official")}</option>
            </select>
          </label>
        {/if}
        <label>
          {$t("chatgptDesktop.installRoot")}
          <input
            value={settingsDraft.installRoot}
            on:input={(event) => updateChatGPTDesktopDraft({ installRoot: event.currentTarget.value })}
          />
        </label>
        <label class={nativeToggleRecipe()} data-native-toggle>
          <input
            type="checkbox"
            checked={settingsDraft.autoCheck}
            on:change={(event) => updateChatGPTDesktopDraft({ autoCheck: event.currentTarget.checked })}
          />
          <span>
            <strong>{$t("chatgptDesktop.autoCheck")}</strong>
            <small>{$t("chatgptDesktop.autoCheckHint")}</small>
          </span>
        </label>
        <label class={nativeToggleRecipe()} data-native-toggle>
          <input
            type="checkbox"
            checked={settingsDraft.keepUserDataOnUninstall}
            on:change={(event) => updateChatGPTDesktopDraft({ keepUserDataOnUninstall: event.currentTarget.checked })}
          />
          <span>
            <strong>{$t("chatgptDesktop.keepUserData")}</strong>
            <small>{$t("chatgptDesktop.keepUserDataHint")}</small>
          </span>
        </label>
      </div>
    {/if}
  </section>

  {/if}
</div>

{#if confirmUninstall}
  <div class={desktopClientModalBackdropRecipe()}>
    <div class={desktopClientModalPanelRecipe()}>
      <div class={desktopClientModalBodyRecipe()}>
        <div>
        <h2>{$t("chatgptDesktop.uninstallTitle")}</h2>
        <p>{$t("chatgptDesktop.uninstallDescription")}</p>
      </div>
      <div class={desktopClientPreviewListRecipe()}>
        <div>
          <strong>{$t("chatgptDesktop.currentVersion")}</strong>
          <span>{installed?.version ?? $t("common.none")} / {installed?.path ?? $t("common.none")}</span>
        </div>
        <div>
          <strong>{$t("chatgptDesktop.keepUserData")}</strong>
          <span>{settingsDraft?.keepUserDataOnUninstall ? $t("common.enabled") : $t("common.disabled")}</span>
        </div>
      </div>
      </div>

      <div class={desktopClientModalActionsRecipe()}>
        <button class={actionButtonRecipe()} on:click={() => setChatGPTDesktopConfirmUninstall(false)}>{$t("common.cancel")}</button>
        <button class={actionButtonRecipe({ tone: "primary" })} disabled={workflowBusy} on:click={removeCodex}>
          <AppIcon name="delete" size={16} />
          {busyAction === "uninstall" ? $t("chatgptDesktop.uninstalling") : $t("chatgptDesktop.confirmUninstall")}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  /* Match the compact Profiles page without scaling the app or changing other routes. */
  .desktop-workflow {
    container: desktop-workflow / inline-size;
    gap: var(--space-md);
    font-size: 13px;
  }
  .desktop-workflow-header {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: var(--space-md);
    padding: 16px var(--space-md);
  }
  .desktop-workflow-header h1 { margin: 0; font-size: 24px; line-height: 1.25; }
  .desktop-workflow-header p { margin: 6px 0 0; font-size: 13px; line-height: 1.5; color: var(--text-soft); }
  .desktop-workflow-header :global(.cs-status-strip) { margin-top: 8px; font-size: 12px; }
  .desktop-header-actions { flex-wrap: nowrap; }
  .desktop-header-actions button { flex: 0 0 auto; }
  .desktop-workflow-nav { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; padding: 8px; }
  .desktop-workflow-nav button { min-height: 38px; font-size: 12px; }
  .desktop-workflow-nav button[data-active="true"] {
    border-color: var(--accent); color: var(--text);
    background: linear-gradient(135deg, color-mix(in srgb, var(--amber) 20%, var(--surface)), color-mix(in srgb, var(--accent) 24%, var(--surface)));
  }
  .desktop-workflow-heading { padding: 12px var(--space-md); }
  .desktop-workflow-heading h2 { font-size: 14px; line-height: 1.3; }
  .desktop-workflow :global(h2[tabindex="-1"]:focus-visible) { outline: 2px solid var(--accent); outline-offset: 4px; border-radius: 4px; }
  .desktop-workflow-heading p { margin-top: 5px; font-size: 12px; line-height: 1.5; color: var(--text-soft); }
  .desktop-profile-list { display: grid; gap: 10px; padding: 12px var(--space-md); }
  .desktop-profile-option { display: grid; grid-template-columns: 34px minmax(0, 1fr) auto; align-items: center; gap: 10px; width: 100%; min-width: 0; min-height: 64px; padding: 12px; border: 1px solid var(--border); border-radius: var(--control-radius); background: var(--surface-soft); color: var(--text); text-align: left; cursor: pointer; transition: border-color 160ms ease, background 160ms ease; }
  .desktop-profile-option:hover:not(:disabled) { border-color: var(--accent); }
  .desktop-profile-option[data-selected="true"] { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 13%, var(--surface)); }
  .desktop-profile-option:focus-visible, .desktop-workflow-nav button:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px; }
  .desktop-profile-copy { display: grid; min-width: 0; gap: 4px; }
  .desktop-profile-copy strong { font-size: 14px; font-weight: 700; line-height: 1.25; color: var(--text); overflow-wrap: anywhere; }
  .desktop-profile-copy > span, .desktop-profile-copy small { font-size: 12px; line-height: 1.4; color: var(--text-soft); overflow-wrap: anywhere; }
  .desktop-profile-flags { display: flex; align-items: center; gap: 8px; }
  .desktop-profile-active { color: var(--text); background: var(--surface); padding: 3px 7px; border-radius: 8px; font-size: 12px; }
  .desktop-selection-mark { width: 18px; height: 18px; display: grid; place-items: center; border: 1px solid var(--border-strong); border-radius: 50%; }
  .desktop-selection-mark[data-checked="true"] { border-color: transparent; }
  .desktop-workflow-actions { gap: 8px; padding: 0 var(--space-md) var(--space-md); }
  .desktop-workflow-actions button { min-height: 38px; font-size: 12px; flex: 0 0 auto; }
  .desktop-launch-summary { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr) minmax(0, 1fr); margin: 12px var(--space-md); border: 1px solid var(--border); border-radius: var(--control-radius); background: var(--surface-soft); }
  .desktop-launch-summary > div { display: grid; align-content: start; min-width: 0; gap: 5px; padding: 12px; }
  .desktop-launch-summary > div + div { border-left: 1px solid var(--border); }
  .desktop-launch-summary dt { color: var(--text-soft); font-size: 12px; line-height: 1.4; }
  .desktop-launch-summary dd { margin: 0; color: var(--text); font-size: 13px; font-weight: 600; line-height: 1.5; overflow-wrap: anywhere; }
  .desktop-launch-hint { margin: 12px var(--space-md); color: var(--text-soft); font-size: 12px; line-height: 1.5; }
  @container desktop-workflow (max-width: 620px) {
    .desktop-workflow-header { grid-template-columns: minmax(0, 1fr); }
    .desktop-header-actions { justify-content: flex-start; flex-wrap: wrap; }
    .desktop-launch-summary { grid-template-columns: minmax(0, 1fr); }
    .desktop-launch-summary > div { grid-template-columns: 100px minmax(0, 1fr); align-items: baseline; gap: 10px; padding: 10px 12px; }
    .desktop-launch-summary > div + div { border-left: 0; border-top: 1px solid var(--border); }
  }
  @container desktop-workflow (max-width: 380px) {
    .desktop-workflow-nav { grid-template-columns: minmax(0, 1fr); }
    .desktop-profile-option { grid-template-columns: 34px minmax(0, 1fr); }
    .desktop-profile-flags { grid-column: 2; justify-self: end; }
    .desktop-launch-summary > div { grid-template-columns: minmax(0, 1fr); gap: 4px; }
  }
  @media (pointer: coarse) {
    .desktop-workflow-nav button, .desktop-workflow-actions button, .desktop-header-actions button { min-height: 44px; }
  }
  @media (prefers-reduced-motion: reduce) { .desktop-profile-option { transition: none; } }
</style>
