<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from "vue";
import Dashboard from "@/views/Dashboard.vue";
import Tools from "@/views/Tools.vue";
import Models from "@/views/Models.vue";
import Accounts from "@/views/Accounts.vue";
import Permissions from "@/views/Permissions.vue";
import Settings from "@/views/Settings.vue";
import TOTPSettingsCard from "@/components/TOTPSettingsCard.vue";
import CodexConfigurationModal from "@/components/CodexConfigurationModal.vue";
import ClaudeCodeConfigurationModal from "@/components/ClaudeCodeConfigurationModal.vue";
import MCPConfigurationModal from "@/components/MCPConfigurationModal.vue";
import AgentIcon from "@/components/AgentIcon.vue";
import Button from "@/components/ui/Button.vue";
import Modal from "@/components/ui/Modal.vue";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
import {
	state,
	bootstrap,
	setEmbeddedRuntimeMode,
	requestQuit,
	checkForUpdates,
	installLatestUpdate,
	applyPatch,
	loadPatchStatus,
	refreshAgentStatuses,
	diagnoseAgent,
	selectAgentDesktopInstallation,
	launchDetectedAgent,
} from "@/state/appState";

const embeddedParams = new URLSearchParams(window.location.search);
const embeddedMode = embeddedParams.get("embedded") === "1";
const embeddedVersionPillLabel = "由宿主更新";
const versionPillLabel = computed(() => embeddedMode ? embeddedVersionPillLabel : "v1.8.0");
setEmbeddedRuntimeMode(embeddedMode);
if (embeddedMode) document.documentElement.dataset.embedded = "true";
const embeddedModuleID = embeddedParams.get("module") || "antigravity";
const supportedEmbeddedModules = new Set(["antigravity", "codex", "claude-code", "cursor", "windsurf"]);
const activeModuleID = ref(supportedEmbeddedModules.has(embeddedModuleID) ? embeddedModuleID : "antigravity");
const embeddedSection = embeddedParams.get("section") || "dashboard";
const supportedAntigravitySections = new Set(["dashboard", "models", "accounts", "permissions", "totp"]);
const antigravityTab = ref(supportedAntigravitySections.has(embeddedSection) ? embeddedSection : "dashboard");
const exitDialogOpen = ref(false);
const updateDialogOpen = ref(false);
const updateDialogError = ref("");
const dismissedUpdateVersion = ref("");
const repatchDialogOpen = ref(false);
const repatchError = ref("");
const agentDetailOpen = ref(false);
const codexConfigurationOpen = ref(false);
const codexConfigurationSection = ref("");
const claudeCodeConfigurationOpen = ref(false);
const claudeCodeConfigurationSection = ref("");
const mcpConfigurationOpen = ref(false);
const mcpConfigurationTarget = ref("");
const mcpConfigurationAction = ref("");
const selectedAgentID = ref("");
const selectedAgentDiagnostics = ref([]);
let removeMainWindowShownListener = null;
const isMac = /Mac|iPhone|iPad|iPod/.test(navigator.platform);
const agentModules = [
  { id: "antigravity", label: "Antigravity WF", hint: "本地代理、模型注入与兼容补丁" },
  { id: "codex", label: "Codex", hint: "Codex Provider 与桌面控制" },
  { id: "claude-code", label: "Claude Code", hint: "Claude Code 网关配置" },
  { id: "cursor", label: "Cursor", hint: "Cursor MCP 配置" },
  { id: "windsurf", label: "Windsurf", hint: "Windsurf MCP 配置" },
];
const antigravityTabs = [
  { label: "总览", value: "dashboard", hint: "运行状态、补丁与快捷启动" },
  { label: "模型", value: "models", hint: "自定义上游模型" },
  { label: "上游", value: "accounts", hint: "上游账户与自动调度" },
  { label: "权限", value: "permissions", hint: "终端命令自动批准" },
];
const themeOptions = [
  { label: "浅色", value: "light" },
  { label: "深色", value: "dark" },
  { label: "跟随系统", value: "system" },
];
const storedTheme = localStorage.getItem("wf-theme");
const embeddedTheme = embeddedParams.get("theme");
const themeMode = ref(
  embeddedMode && ["light", "dark"].includes(embeddedTheme)
    ? embeddedTheme
    : (["light", "dark", "system"].includes(storedTheme) ? storedTheme : "system"),
);
const systemTheme = window.matchMedia("(prefers-color-scheme: dark)");

const activeModule = computed(() => agentModules.find((item) => item.id === activeModuleID.value) || null);
const activeAntigravityTab = computed(() => antigravityTabs.find((item) => item.value === antigravityTab.value) || antigravityTabs[0]);
const currentWorkspace = computed(() => {
  if (activeModuleID.value === "settings") {
    return { label: "设置", hint: "主题、更新、诊断与本机安全服务" };
  }
  if (activeModuleID.value === "antigravity") {
    return { label: "Antigravity WF", hint: activeAntigravityTab.value.hint };
  }
  return activeModule.value || { label: "Agent", hint: "本机 Agent 独立功能区" };
});
const updateProgressPercent = computed(() => Math.min(100, Math.max(0, Number(state.update.progress?.percent) || 0)));
const selectedAgent = computed(() => state.agents.platforms.find((platform) => (platform.agentId || platform.id) === selectedAgentID.value) || null);

function activateAgent(agentID) {
  if (!agentModules.some((item) => item.id === agentID)) return;
  activeModuleID.value = agentID;
}

function agentNavigationState(agentID) {
  return state.agents.platforms.find((platform) => (platform.agentId || platform.id) === agentID)?.state || "unknown";
}

function agentNavigationTone(agentID) {
  return {
    ready: "ok",
    detected: "info",
    degraded: "warn",
    error: "danger",
  }[agentNavigationState(agentID)] || "muted";
}

function applyTheme() {
  const resolved = themeMode.value === "system"
    ? (systemTheme.matches ? "dark" : "light")
    : themeMode.value;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.dataset.themeMode = themeMode.value;
}

function handleSystemThemeChange() {
  if (themeMode.value === "system") applyTheme();
}

function quitAssistant() {
  requestQuit().catch(console.error);
}

function dismissUpdateDialog() {
	dismissedUpdateVersion.value = state.update.info?.latestVersion || "";
	updateDialogOpen.value = false;
}

async function handleInstallUpdate() {
	if (embeddedMode) return;
	updateDialogError.value = "";
	const res = await installLatestUpdate();
	if (!res?.ok) updateDialogError.value = res?.message || "下载更新失败";
}

async function handleRequiredReconnect() {
	repatchError.value = "";
	const res = await applyPatch();
	if (!res?.ok) {
		repatchError.value = res?.message || "重新连接失败";
		return;
	}
	repatchDialogOpen.value = false;
}

function dismissRepatchDialog() {
	if (state.patchBusy) return;
	repatchError.value = "";
	repatchDialogOpen.value = false;
}

function handleMainWindowShown() {
	void loadPatchStatus();
	if (!embeddedMode && state.settings?.updates?.autoCheck !== false) void checkForUpdates();
}

async function handleAgentRefresh() {
	await refreshAgentStatuses();
}

async function handleAgentDiagnose(agentID) {
	selectedAgentID.value = agentID;
	const result = await diagnoseAgent(agentID);
	selectedAgentDiagnostics.value = result?.diagnostics || [];
	agentDetailOpen.value = true;
}

function setAgentActionMessage(agentID, tone, message) {
	state.agents.actionMessages = {
		...state.agents.actionMessages,
		[agentID]: { tone, message },
	};
}

async function handleAgentLaunch(agentID) {
	setAgentActionMessage(agentID, "progress", "正在请求打开应用…");
	const result = await launchDetectedAgent(agentID);
	setAgentActionMessage(
		agentID,
		result?.ok ? "ok" : "error",
		result?.ok ? "已请求打开应用。" : (result?.message || "无法启动该应用。"),
	);
}

async function handleAgentChoose(agentID) {
	setAgentActionMessage(agentID, "progress", "正在打开系统应用选择窗口…");
	const result = await selectAgentDesktopInstallation(agentID);
	setAgentActionMessage(
		agentID,
		result?.ok ? "ok" : "error",
		result?.message || "无法选择应用。",
	);
}

function handleAgentConfigure(agentID, actionID = "") {
	if (agentID === "antigravity") {
		activeModuleID.value = "antigravity";
		antigravityTab.value = "models";
		return;
	}
	if (agentID === "codex") {
		codexConfigurationSection.value = actionID;
		codexConfigurationOpen.value = true;
		return;
	}
	if (agentID === "claude-code") {
		claudeCodeConfigurationSection.value = actionID;
		claudeCodeConfigurationOpen.value = true;
		return;
	}
	if (agentID === "cursor" || agentID === "windsurf") {
		mcpConfigurationTarget.value = agentID;
		mcpConfigurationAction.value = actionID;
		mcpConfigurationOpen.value = true;
		return;
	}
	selectedAgentID.value = agentID;
	selectedAgentDiagnostics.value = state.agents.diagnostics.filter((item) => item.agentId === agentID);
	agentDetailOpen.value = true;
}

// The top-level account page belongs to the Antigravity WF module. Codex and
// Claude Code may choose a compatible redacted credential only inside their
// own configuration modules; they must never be routed into the Antigravity
// account UI or presented as sharing a native client login.
function handleAgentAccounts(agentID) {
	if (agentID === "antigravity") {
		activeModuleID.value = "antigravity";
		antigravityTab.value = "accounts";
	}
}

function handleAgentOpen(agentID) {
	selectedAgentID.value = agentID;
	selectedAgentDiagnostics.value = state.agents.diagnostics.filter((item) => item.agentId === agentID);
	agentDetailOpen.value = true;
}

function handleCodexConfigurationChanged() {
	void refreshAgentStatuses();
}

function handleClaudeCodeConfigurationChanged() {
	void refreshAgentStatuses();
}

function handleMCPConfigurationChanged() {
	void refreshAgentStatuses();
}

watch(themeMode, (value) => {
  localStorage.setItem("wf-theme", value);
  applyTheme();
}, { immediate: true });

watch(
	() => [state.update.info?.available, state.update.info?.skipped, state.update.info?.latestVersion],
	([available, skipped, version]) => {
		if (embeddedMode) {
			updateDialogOpen.value = false;
			return;
		}
		if (available && !skipped && version && version !== dismissedUpdateVersion.value && !repatchDialogOpen.value) {
			updateDialogOpen.value = true;
		}
	},
);

watch(
	() => state.patch.productRepatchRequired,
	(required) => {
		if (!required) {
			repatchDialogOpen.value = false;
			return;
		}
		activeModuleID.value = "antigravity";
		antigravityTab.value = "dashboard";
		updateDialogOpen.value = false;
		repatchError.value = "";
		repatchDialogOpen.value = true;
	},
);

function notifyEmbeddedReady() {
  if (!embeddedMode || window.parent === window) return;
  window.parent.postMessage(
    { type: "xiass-wf-ready", module: activeModuleID.value },
    "*",
  );
}

let embeddedHeightObserver = null;
let embeddedHeightFrame = 0;

function measureEmbeddedContentHeight() {
  const app = document.getElementById("app");
  const shell = document.querySelector(".shell");
  // The iframe viewport is set from the last height we reported. Reading the
  // root scroll height feeds that old viewport size back to the host and can
  // leave a large empty area after an inline sheet closes. Measure only real
  // document content so the host can shrink again.
  const heights = [
    document.body?.scrollHeight || 0,
    app?.scrollHeight || 0,
    app?.getBoundingClientRect().height || 0,
    shell?.scrollHeight || 0,
    shell?.getBoundingClientRect().height || 0,
  ];
  return Math.ceil(Math.max(520, ...heights));
}

function notifyEmbeddedContentHeight() {
  if (!embeddedMode || window.parent === window) return;
  window.parent.postMessage(
    {
      type: "xiass-wf-content-height",
      module: activeModuleID.value,
      height: Math.min(32000, measureEmbeddedContentHeight()),
    },
    "*",
  );
}

function scheduleEmbeddedContentHeight() {
  if (!embeddedMode || embeddedHeightFrame) return;
  embeddedHeightFrame = window.requestAnimationFrame(() => {
    embeddedHeightFrame = 0;
    notifyEmbeddedContentHeight();
  });
}

function startEmbeddedContentHeightReporter() {
  if (!embeddedMode || typeof ResizeObserver === "undefined") {
    scheduleEmbeddedContentHeight();
    return;
  }
  embeddedHeightObserver = new ResizeObserver(scheduleEmbeddedContentHeight);
  [document.body, document.getElementById("app"), document.querySelector(".shell")]
    .filter(Boolean)
    .forEach((element) => embeddedHeightObserver.observe(element));
  window.addEventListener("resize", scheduleEmbeddedContentHeight);
  scheduleEmbeddedContentHeight();
}

function stopEmbeddedContentHeightReporter() {
  window.removeEventListener("resize", scheduleEmbeddedContentHeight);
  embeddedHeightObserver?.disconnect();
  embeddedHeightObserver = null;
  if (embeddedHeightFrame) window.cancelAnimationFrame(embeddedHeightFrame);
  embeddedHeightFrame = 0;
}

function embeddedScrollableAncestorCanConsumeWheel(target, deltaY) {
  let current = target instanceof Element ? target : null;
  while (current && current !== document.documentElement) {
    const style = window.getComputedStyle(current);
    if (["auto", "scroll", "overlay"].includes(style.overflowY)) {
      const maxScrollTop = current.scrollHeight - current.clientHeight;
      if (maxScrollTop > 1) {
        if (deltaY < 0 && current.scrollTop > 1) return true;
        if (deltaY > 0 && current.scrollTop < maxScrollTop - 1) return true;
      }
    }
    current = current.parentElement;
  }
  return false;
}

function relayEmbeddedWheel(event) {
  if (!embeddedMode || window.parent === window || event.defaultPrevented || event.ctrlKey) return;
  const deltaX = Number(event.deltaX) || 0;
  const deltaY = Number(event.deltaY) || 0;
  if (Math.abs(deltaX) < 0.5 && Math.abs(deltaY) < 0.5) return;
  if (embeddedScrollableAncestorCanConsumeWheel(event.target, deltaY)) return;

  // Cross-origin iframe wheel events do not bubble to the host. The host
  // validates origin/source before moving its single document scrollbar.
  event.preventDefault();
  window.parent.postMessage(
    {
      type: "xiass-wf-scroll",
      deltaX: Math.max(-1200, Math.min(1200, deltaX)),
      deltaY: Math.max(-1200, Math.min(1200, deltaY)),
    },
    "*",
  );
}

onMounted(() => {
  systemTheme.addEventListener?.("change", handleSystemThemeChange);
  if (embeddedMode) window.addEventListener("wheel", relayEmbeddedWheel, { capture: true, passive: false });
  bootstrap().then(async () => {
    if (!embeddedMode) return;
    if (activeModuleID.value === "codex") codexConfigurationOpen.value = true;
    if (activeModuleID.value === "claude-code") claudeCodeConfigurationOpen.value = true;
    if (activeModuleID.value === "cursor" || activeModuleID.value === "windsurf") {
      mcpConfigurationTarget.value = activeModuleID.value;
      mcpConfigurationOpen.value = true;
    }
    await nextTick();
    startEmbeddedContentHeightReporter();
    window.requestAnimationFrame(() => window.requestAnimationFrame(() => {
      notifyEmbeddedContentHeight();
      notifyEmbeddedReady();
    }));
  }).catch(console.error);
	const runtime = window.runtime;
	if (typeof runtime?.EventsOn === "function") {
		const unsubscribe = runtime.EventsOn("wf:main-window-shown", handleMainWindowShown);
		if (typeof unsubscribe === "function") removeMainWindowShownListener = unsubscribe;
		else if (typeof runtime.EventsOff === "function") {
			removeMainWindowShownListener = () => runtime.EventsOff("wf:main-window-shown");
		}
	}
});

onUnmounted(() => {
  systemTheme.removeEventListener?.("change", handleSystemThemeChange);
	window.removeEventListener("wheel", relayEmbeddedWheel, true);
	stopEmbeddedContentHeightReporter();
	removeMainWindowShownListener?.();
});
</script>

<template>
  <div class="shell" :class="{ embedded: embeddedMode }">
    <aside class="sidebar" :class="{ mac: isMac }" style="--wails-draggable: drag">
      <div class="brand-mark" title="XIASS Tools">
        <img src="/xiass-tools-logo.png" alt="XIASS Tools" />
      </div>

      <nav class="nav-stack" aria-label="Agent 模块" style="--wails-draggable: no-drag">
        <button
          v-for="item in agentModules"
          :key="item.id"
          class="nav-item"
          :data-agent="item.id"
          :class="{ active: activeModuleID === item.id }"
          :aria-current="activeModuleID === item.id ? 'page' : undefined"
          :title="item.hint"
          @click="activateAgent(item.id)"
        >
          <span class="agent-nav-glyph"><AgentIcon :agent-id="item.id" /></span>
          <span class="agent-nav-copy">{{ item.label }}</span>
          <i class="agent-nav-status" :class="agentNavigationTone(item.id)" :title="agentNavigationState(item.id)" aria-hidden="true"></i>
        </button>
      </nav>

      <div class="sidebar-foot">
        <button
          class="settings-trigger"
          :class="{ active: activeModuleID === 'settings' }"
          type="button"
          title="XIASS Tools 设置"
          aria-label="XIASS Tools 设置"
          style="--wails-draggable: no-drag"
          @click="activeModuleID = 'settings'"
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8M19 12l2-1-2-3-2 .5-2-1L14 5h-4L9 7.5l-2 1L5 8l-2 3 2 1-2 1 2 3 2-.5 2 1 1 2.5h4l1-2.5 2-1 2 .5 2-3-2-1Z" />
          </svg>
          <span>设置</span>
        </button>
        <button
          class="exit-trigger"
          type="button"
          title="退出助手并停止本地代理"
          aria-label="退出助手并停止本地代理"
          style="--wails-draggable: no-drag"
          @click="exitDialogOpen = true"
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M12 3v9m5.66-5.66A8 8 0 1 1 6.34 6.34" />
          </svg>
        </button>
        <span class="version-pill" :title="embeddedMode ? '版本与更新由宿主 XIASS Tools 管理' : 'XIASS Tools v1.8.0'">{{ versionPillLabel }}</span>
      </div>
    </aside>

    <section class="workspace">
      <header class="topbar" style="--wails-draggable: drag">
        <div class="page-title">
          <div class="eyebrow">XIASS TOOLS</div>
          <div class="title-row">
            <h1>{{ currentWorkspace.label }}</h1>
            <span>{{ currentWorkspace.hint }}</span>
          </div>
        </div>
        <SegmentedControl
          v-model="themeMode"
          :options="themeOptions"
          class="theme-control"
          style="--wails-draggable: no-drag"
        />
      </header>

      <nav v-if="activeModuleID === 'antigravity'" class="agent-subnav" aria-label="Antigravity WF 功能导航" style="--wails-draggable: no-drag">
        <button
          v-for="item in antigravityTabs"
          :key="item.value"
          type="button"
          :class="{ active: antigravityTab === item.value }"
          :aria-current="antigravityTab === item.value ? 'page' : undefined"
          @click="antigravityTab = item.value"
        >
          {{ item.label }}
        </button>
      </nav>

      <main class="main">
        <Transition name="page" mode="out-in">
          <Dashboard v-if="activeModuleID === 'antigravity' && antigravityTab === 'dashboard'" key="antigravity-dashboard" />
          <Models v-else-if="activeModuleID === 'antigravity' && antigravityTab === 'models'" key="antigravity-models" />
          <Accounts v-else-if="activeModuleID === 'antigravity' && antigravityTab === 'accounts'" key="antigravity-accounts" />
          <Permissions v-else-if="activeModuleID === 'antigravity' && antigravityTab === 'permissions'" key="antigravity-permissions" />
          <TOTPSettingsCard v-else-if="activeModuleID === 'antigravity' && antigravityTab === 'totp'" key="antigravity-totp" />
          <Settings v-else-if="activeModuleID === 'settings'" key="settings" />
          <Tools
            v-else
            :key="`agent-${activeModuleID}`"
			:platforms="state.agents.platforms"
			:selected-agent-id="activeModuleID"
			:loading="state.agents.loading"
			:action-busy="state.agents.actionBusy"
			:action-messages="state.agents.actionMessages"
			:manual-selections="state.agents.manualSelections"
			:preview="state.agents.preview"
			:message="state.agents.message"
            @refresh="handleAgentRefresh"
			@configure="handleAgentConfigure"
			@diagnose="handleAgentDiagnose"
			@choose="handleAgentChoose"
			@launch="handleAgentLaunch"
			@accounts="handleAgentAccounts"
			@open="handleAgentOpen"
          />
        </Transition>
      </main>
    </section>

    <Modal :open="exitDialogOpen" title="退出助手？" @close="exitDialogOpen = false">
      <p class="exit-copy">退出后会停止本地代理。点击窗口叉号只会隐藏主窗口；也可从系统菜单栏或托盘图标退出。</p>
      <template #footer>
        <Button variant="plain" @click="exitDialogOpen = false">取消</Button>
        <Button variant="danger" @click="quitAssistant">退出助手</Button>
      </template>
    </Modal>

	<Modal :open="agentDetailOpen" :title="selectedAgent ? `${selectedAgent.displayName} 本机状态` : '工具状态'" @close="agentDetailOpen = false">
	  <div class="agent-detail-copy">
		<span v-if="selectedAgent?.message">{{ selectedAgent.message }}</span>
		<span v-if="(selectedAgent?.agentId || selectedAgent?.id) !== 'codex' && selectedAgent?.installation?.root" class="agent-detail-path">{{ selectedAgent.installation.root }}</span>
		<div v-if="selectedAgentDiagnostics.length" class="agent-diagnostic-list">
		  <article v-for="(diagnostic, index) in selectedAgentDiagnostics" :key="`${diagnostic.code}-${index}`" :class="['agent-diagnostic', diagnostic.severity || 'info']">
			<strong>{{ diagnostic.summary }}</strong>
			<span v-if="diagnostic.detail">{{ diagnostic.detail }}</span>
			<span v-if="diagnostic.remediation" class="agent-remediation">{{ diagnostic.remediation }}</span>
		  </article>
		</div>
		<span v-else class="agent-detail-empty">尚无额外诊断信息。</span>
	  </div>
	  <template #footer>
		<Button variant="plain" @click="agentDetailOpen = false">关闭</Button>
	  </template>
	</Modal>

	<CodexConfigurationModal
		:open="codexConfigurationOpen"
		:inline="embeddedMode"
		:section="codexConfigurationSection"
		@close="codexConfigurationOpen = false"
		@changed="handleCodexConfigurationChanged"
	/>

	<ClaudeCodeConfigurationModal
		:open="claudeCodeConfigurationOpen"
		:inline="embeddedMode"
		:section="claudeCodeConfigurationSection"
		@close="claudeCodeConfigurationOpen = false"
		@changed="handleClaudeCodeConfigurationChanged"
	/>

	<MCPConfigurationModal
		:open="mcpConfigurationOpen"
		:inline="embeddedMode"
		:target="mcpConfigurationTarget"
		:action="mcpConfigurationAction"
		@close="mcpConfigurationOpen = false"
		@changed="handleMCPConfigurationChanged"
	/>

	<Modal :open="!embeddedMode && updateDialogOpen" title="发现新版本" @close="dismissUpdateDialog">
	  <div class="global-dialog-copy">
		<strong>XIASS Tools v{{ state.update.info.latestVersion }}</strong>
		<span>当前为 v{{ state.update.info.currentVersion || '—' }}，可以直接下载、校验并启动新版安装程序。</span>
		<span v-if="state.update.info.notes" class="update-notes">{{ state.update.info.notes }}</span>
	  </div>
	  <div v-if="state.update.installing" class="global-progress">
		<div><span>{{ state.update.progress?.message || '正在下载更新' }}</span><strong>{{ updateProgressPercent }}%</strong></div>
		<div class="global-progress-track"><span :style="{ width: `${updateProgressPercent}%` }"></span></div>
	  </div>
	  <div v-if="updateDialogError" class="global-error">{{ updateDialogError }}</div>
	  <template #footer>
		<Button variant="plain" :disabled="state.update.installing" @click="dismissUpdateDialog">稍后</Button>
		<Button variant="filled" :loading="state.update.installing" :disabled="state.update.installing" @click="handleInstallUpdate">一键下载安装</Button>
	  </template>
	</Modal>

	<Modal :open="repatchDialogOpen" title="Antigravity 需要重新连接" :closable="!state.patchBusy" persistent @close="dismissRepatchDialog">
	  <div class="global-dialog-copy">
		<strong>检测到安装版本或连接规则已更新</strong>
		<span>{{ state.patch.productRepatchMessage || '检测到 Antigravity 安装发生变化，请重新连接后继续使用。' }}</span>
		<span>请先完全退出正在运行的 Antigravity。助手会按当前安装结构自动选择兼容注入方式。</span>
		<span>也可以暂时跳过，之后随时在首页手动连接并升级到最新补丁规则。</span>
	  </div>
	  <div v-if="repatchError" class="global-error">{{ repatchError }}</div>
	  <template #footer>
		<Button variant="plain" :disabled="state.patchBusy" @click="dismissRepatchDialog">稍后再说</Button>
		<Button variant="filled" :loading="state.patchBusy" :disabled="state.patchBusy" @click="handleRequiredReconnect">立即重新连接</Button>
	  </template>
	</Modal>
  </div>
</template>

<style scoped>
.shell {
  display: grid;
  grid-template-columns: 184px minmax(0, 1fr);
  height: 100vh;
  background: var(--bg-base);
}

.shell.embedded {
  grid-template-columns: minmax(0, 1fr);
  height: auto;
  min-height: 0;
  min-width: 0;
  background: transparent;
}

.shell.embedded > .sidebar,
.shell.embedded .topbar {
  display: none;
}

.shell.embedded .workspace {
  min-width: 0;
  height: auto;
  min-height: 0;
}

.shell.embedded .agent-subnav {
  display: none;
}

.shell.embedded .main {
  flex: 0 0 auto;
  min-height: 0;
  overflow: visible;
  padding: 8px 0 16px;
}

.agent-detail-copy {
	display: grid;
	gap: 12px;
	color: var(--text-secondary);
	font-size: 13px;
	line-height: 1.6;
}

.agent-detail-path {
	overflow: hidden;
	border: 1px solid var(--separator);
	border-radius: 7px;
	background: var(--bg-inset);
	color: var(--text-tertiary);
	font-family: var(--font-num);
	font-size: 11px;
	padding: 8px 10px;
	text-overflow: ellipsis;
	white-space: nowrap;
}

.agent-diagnostic-list { display: grid; gap: 8px; }

.agent-diagnostic {
	display: grid;
	gap: 3px;
	border: 1px solid var(--separator);
	border-left: 3px solid var(--blue);
	border-radius: 7px;
	background: var(--bg-inset);
	padding: 10px 11px;
}

.agent-diagnostic.warning { border-left-color: var(--orange); }
.agent-diagnostic.error { border-left-color: var(--red); }
.agent-diagnostic strong { color: var(--text-primary); font-size: 12px; }
.agent-diagnostic span { color: var(--text-secondary); font-size: 12px; }
.agent-diagnostic .agent-remediation { color: var(--text-tertiary); }
.agent-detail-empty { color: var(--text-tertiary); }

.sidebar {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 18px;
  padding: 18px 12px 14px;
  border-right: 1px solid var(--separator);
  background: var(--bg-sidebar);
  position: relative;
  z-index: 20;
}

.sidebar.mac {
  padding-top: 42px;
}

.brand-mark {
  width: 100%;
  height: 58px;
  display: grid;
  place-items: center;
  border-radius: 18px;
}

.brand-mark img {
  width: 48px;
  height: 48px;
  object-fit: contain;
}

.nav-stack {
  display: flex;
  flex-direction: column;
  gap: 7px;
  width: 100%;
}

.nav-item {
  width: 100%;
  min-height: 50px;
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr) 8px;
  align-items: center;
  gap: 9px;
  border-radius: 12px;
  color: var(--text-tertiary);
  padding: 6px 9px;
  text-align: left;
  transition: color 0.18s var(--ease), background 0.18s var(--ease), transform 0.18s var(--spring);
}

.nav-item:hover {
  color: var(--text-primary);
  background: var(--bg-fill);
}

.nav-item:active {
  transform: scale(0.96);
}

.nav-item.active {
  color: var(--accent-strong);
  background: var(--accent-soft);
  box-shadow: inset 0 0 0 1px var(--accent-border);
}

.agent-nav-glyph {
  display: grid;
  width: 34px;
  height: 34px;
  place-items: center;
  border: 1px solid var(--separator);
  border-radius: 10px;
  background: var(--bg-inset);
  color: var(--agent-icon-color, var(--accent-strong));
}

.nav-item[data-agent="antigravity"] { --agent-icon-color: #f59b48; }
.nav-item[data-agent="codex"] { --agent-icon-color: #4ac7a4; }
.nav-item[data-agent="claude-code"] { --agent-icon-color: #e99a64; }
.nav-item[data-agent="cursor"] { --agent-icon-color: #9d93ff; }
.nav-item[data-agent="windsurf"] { --agent-icon-color: #42c4e9; }

.agent-nav-glyph :deep(.agent-icon) { width: 21px; height: 21px; }

.nav-item.active .agent-nav-glyph {
  border-color: var(--accent-border);
  background: color-mix(in srgb, var(--accent-soft) 76%, var(--bg-inset));
}

.agent-nav-copy {
  color: inherit;
  font-size: 13px;
  font-weight: 650;
  line-height: 1.2;
  white-space: normal;
}

.agent-nav-status {
  width: 7px;
  height: 7px;
  border-radius: 999px;
  background: var(--text-tertiary);
}

.agent-nav-status.ok { background: var(--green); }
.agent-nav-status.info { background: var(--blue); }
.agent-nav-status.warn { background: var(--orange); }
.agent-nav-status.danger { background: var(--red); }

.agent-nav-status.muted { opacity: .55; }

.settings-trigger {
  display: flex;
  width: 100%;
  min-height: 38px;
  align-items: center;
  justify-content: center;
  gap: 7px;
  border: 1px solid transparent;
  border-radius: 10px;
  color: var(--text-tertiary);
  font-size: 13px;
  font-weight: 650;
}

.settings-trigger:hover,
.settings-trigger.active {
  border-color: var(--separator);
  background: var(--bg-fill);
  color: var(--text-primary);
}

.settings-trigger.active {
  border-color: var(--accent-border);
  background: var(--accent-soft);
  color: var(--accent-strong);
}

.settings-trigger svg {
  width: 16px;
  height: 16px;
  fill: none;
  stroke: currentColor;
  stroke-linecap: round;
  stroke-linejoin: round;
  stroke-width: 1.65;
}

.sidebar-foot {
  margin-top: auto;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  width: 100%;
}

.exit-trigger {
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  border-radius: 10px;
  color: var(--text-tertiary);
  transition: color 0.16s var(--ease), background 0.16s var(--ease);
}

.exit-trigger:hover {
  color: var(--red);
  background: rgba(255, 69, 58, 0.12);
}

.exit-trigger svg {
  width: 17px;
  height: 17px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.9;
  stroke-linecap: round;
}

.exit-copy {
  margin: 0;
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.65;
}

.global-dialog-copy {
  display: flex;
  flex-direction: column;
  gap: 8px;
  color: var(--text-secondary);
  font-size: 13px;
  line-height: 1.6;
}

.global-dialog-copy strong {
  color: var(--text-primary);
  font-size: 15px;
}

.update-notes {
  max-height: 96px;
  overflow: auto;
  padding: 9px 10px;
  border-radius: var(--r-md);
  background: var(--bg-fill);
  white-space: pre-wrap;
}

.global-progress {
  margin-top: 14px;
}

.global-progress > div:first-child {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 7px;
  color: var(--text-secondary);
  font-size: 12px;
}

.global-progress-track {
  height: 5px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--bg-fill);
}

.global-progress-track span {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--accent-strong);
  transition: width 0.18s var(--ease);
}

.global-error {
  margin-top: 12px;
  padding: 9px 10px;
  border-radius: var(--r-md);
  color: var(--red);
  background: rgba(255, 69, 58, 0.1);
  font-size: 12px;
  line-height: 1.5;
}

.version-pill {
  display: inline-flex;
  padding: 4px 8px;
  border-radius: 999px;
  background: var(--bg-fill);
  color: var(--text-tertiary);
  font: 600 10px/1 var(--font-num);
}

.workspace {
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background:
    radial-gradient(circle at 92% 0%, var(--ambient-one), transparent 34%),
    radial-gradient(circle at 20% 10%, var(--ambient-two), transparent 38%),
    var(--bg-base);
}

.topbar {
  min-height: 74px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  padding: 14px 24px;
  border-bottom: 1px solid var(--separator);
  background: var(--topbar-bg);
  backdrop-filter: blur(24px) saturate(140%);
  -webkit-backdrop-filter: blur(24px) saturate(140%);
  flex-shrink: 0;
  position: relative;
  z-index: 10;
}

.page-title {
  min-width: 0;
}

.eyebrow {
  color: var(--accent-strong);
  font-size: 9.5px;
  font-weight: 750;
  letter-spacing: 0.13em;
}

.title-row {
  display: flex;
  align-items: baseline;
  gap: 11px;
}

.title-row h1 {
  margin: 0;
  font-size: 24px;
  line-height: 1.25;
  letter-spacing: -0.035em;
}

.title-row span {
  color: var(--text-secondary);
  font-size: 12px;
}

.theme-control {
  flex-shrink: 0;
}

.main {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.agent-subnav {
  display: flex;
  min-height: 48px;
  align-items: center;
  gap: 6px;
  border-bottom: 1px solid var(--separator);
  background: color-mix(in srgb, var(--topbar-bg) 82%, var(--bg-base));
  padding: 7px 24px;
  flex-shrink: 0;
}

.agent-subnav button {
  min-width: 74px;
  min-height: 32px;
  border: 1px solid transparent;
  border-radius: 9px;
  color: var(--text-secondary);
  padding: 0 13px;
  font-size: 12px;
  font-weight: 680;
}

.agent-subnav button:hover {
  border-color: var(--separator);
  background: var(--bg-fill-hover);
  color: var(--text-primary);
}

.agent-subnav button.active {
  border-color: var(--accent-border);
  background: var(--accent-soft);
  color: var(--accent-strong);
}

.page-enter-active,
.page-leave-active {
  transition: opacity 0.18s var(--ease), transform 0.18s var(--ease);
}

.page-enter-from {
  opacity: 0;
  transform: translateY(5px);
}

.page-leave-to {
  opacity: 0;
  transform: translateY(-3px);
}

@media (max-width: 940px) {
  .shell {
    grid-template-columns: 176px minmax(0, 1fr);
  }

  .sidebar {
    padding-inline: 8px;
  }

  .brand-mark {
    height: 52px;
  }

  .brand-mark img {
    width: 43px;
    height: 43px;
  }

  .title-row span {
    display: none;
  }

  .nav-item {
    grid-template-columns: 30px minmax(0, 1fr) 7px;
    padding-inline: 7px;
  }

  .agent-nav-glyph {
    width: 30px;
    height: 30px;
  }
}
</style>
