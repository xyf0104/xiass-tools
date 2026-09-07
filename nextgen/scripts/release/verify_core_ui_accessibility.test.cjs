const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const NEXTGEN_ROOT = path.resolve(__dirname, '../..');

function read(relativePath) {
  return fs.readFileSync(path.join(NEXTGEN_ROOT, relativePath), 'utf8');
}

function sha256(relativePath) {
  return crypto
    .createHash('sha256')
    .update(fs.readFileSync(path.join(NEXTGEN_ROOT, relativePath)))
    .digest('hex');
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

test('Codex provider searches expose stable accessible names', () => {
  const source = read('src/components/codex/CodexModelProviderManagerView.tsx');
  const requiredLabels = [
    '搜索模型供应商',
    '搜索待测试供应商',
    '搜索已有 API Key…',
    '搜索 API Key',
    '搜索 Codex 实例',
  ];

  for (const label of requiredLabels) {
    assert.match(
      source,
      new RegExp(`aria-label=\\{t\\([\\s\\S]{0,240}?${escapeRegExp(label)}`),
    );
  }
  assert.doesNotMatch(source, /placeholder=\{t\("common\.search", "搜索\.\.\."\)\}/);
});

test('Codex provider toolbars and editor modal expose accessible controls', () => {
  const source = read('src/components/codex/CodexModelProviderManagerView.tsx');
  const toolbarStart = source.indexOf('<div className="toolbar">');
  const toolbarEnd = source.indexOf('{error &&', toolbarStart);
  const toolbar = source.slice(toolbarStart, toolbarEnd);

  assert.ok(toolbarStart >= 0 && toolbarEnd > toolbarStart);
  for (const label of [
    'accounts.view.compact',
    'common.shared.view.grid',
    'common.shared.refreshQuota',
    'codex.modelProviders.add',
    'codex.modelProviders.quickConfig.title',
  ]) {
    assert.ok(
      toolbar.includes(`aria-label={t("${label}"`),
      `toolbar icon button must expose ${label}`,
    );
  }
  assert.match(toolbar, /aria-label=\{`\$\{t\("common\.delete", "删除"\)\} \(\$\{selectedProviderIds\.size\}\)`\}/);
  assert.match(source, /className="card-actions"[\s\S]*aria-label=\{enableProviderLabel\}/);
  assert.match(source, /className="modal codex-provider-modal"[\s\S]*role="dialog"[\s\S]*aria-modal="true"[\s\S]*aria-labelledby="codex-provider-dialog-title"/);
  assert.match(source, /<h2\s+id="codex-provider-dialog-title"\s+ref=\{providerDialogTitleRef\}\s+tabIndex=\{-1\}/);
  assert.match(source, /if \(!showModal\) return;[\s\S]*providerDialogTitleRef\.current\?\.focus\(\);[\s\S]*\}, \[showModal\]\);/);
});

test('Codex provider discovery keeps fetched models as an explicit selectable catalog', () => {
  const controller = read('src/components/codex/CodexModelProviderManager.tsx');
  const view = read('src/components/codex/CodexModelProviderManagerView.tsx');
  const styles = read('src/styles/pages/codex-account-dialogs.css');

  assert.match(controller, /setDiscoveredModelCatalog\(models\)/);
  assert.match(controller, /toggleDiscoveredModel/);
  assert.match(controller, /toggleAllDiscoveredModels/);
  assert.match(controller, /updateSelectedModelCatalog/);
  assert.match(view, /type="checkbox"/);
  assert.match(view, /codex-provider-model-catalog__list/);
  assert.match(view, /手动编辑模型 ID/);
  assert.match(styles, /\.codex-provider-modal \.codex-provider-model-catalog__list\s*\{/);
  assert.match(styles, /grid-template-columns:\s*repeat\(auto-fit, minmax\(190px, 1fr\)\);/);
  assert.match(styles, /\.codex-provider-modal \.codex-provider-model-catalog__item:has\(input:checked\)/);
});

test('Codex new-provider dialog keeps the complete original provider catalog', () => {
  const controller = read('src/components/codex/CodexModelProviderManager.tsx');
  const source = read('src/components/codex/CodexModelProviderManagerView.tsx');
  const addAccountDialog = read('src/pages/CodexAddAccountDialog.tsx');
  const presets = read('src/utils/codexProviderPresets.ts');

  assert.match(source, /CODEX_API_PROVIDER_PRESETS\.filter/);
  assert.match(source, /CODEX_API_PROVIDER_CUSTOM_ID/);
  assert.match(controller, /useSponsorStore/);
  assert.match(controller, /sponsorProviderTemplates/);
  assert.match(controller, /handleSelectSponsorTemplate/);
  assert.match(controller, /sourceTag:\s*selectedSponsorTemplate\?\.id/);
  assert.match(source, /sponsorProviderTemplates\.map/);
  assert.match(source, /handleSelectSponsorTemplate\(template\)/);
  assert.match(addAccountDialog, /sponsorApiProviderTemplates\.map/);
  assert.match(addAccountDialog, /handleSelectApiProviderPreset\(template\.id\)/);
  assert.match(presets, /export const CODEX_API_PROVIDER_PRESETS/);
  assert.doesNotMatch(presets, /XIASS_VISIBLE_CODEX_PROVIDER_IDS/);
  assert.doesNotMatch(presets, /XIASS_VISIBLE_CODEX_API_PROVIDER_PRESETS/);
});

test('original provider and account-switching controls remain available', () => {
  const claudePage = read('src/pages/ClaudeAccountsPage.tsx');
  const claudePresets = read('src/utils/claudeProviderPresets.ts');
  const claudeDesktopPresets = read('src/utils/claudeDesktopProviderPresets.ts');
  const quickSettings = read('src/components/QuickSettingsPopover.tsx');
  const codexSettings = read('src/pages/SettingsCodexPlatformPanel.tsx');
  const codexController = read('src/pages/useCodexAccountsBaseController.tsx');

  assert.match(claudePage, /CLAUDE_API_PROVIDER_PRESETS/);
  assert.match(claudePage, /CLAUDE_DESKTOP_GATEWAY_PROVIDER_PRESETS/);
  assert.doesNotMatch(claudePage, /XIASS_VISIBLE_CLAUDE/);
  assert.doesNotMatch(claudePresets, /XIASS_VISIBLE_CLAUDE/);
  assert.doesNotMatch(claudeDesktopPresets, /XIASS_VISIBLE_CLAUDE/);
  assert.match(codexController, /state\.state\?\.sponsorModule \?\? null/);

  for (const control of [
    'openclaw_auth_overwrite_on_switch',
    'hermes_auth_overwrite_on_switch',
    'opencode_auth_overwrite_on_switch',
    'opencode_sync_on_switch',
  ]) {
    assert.match(quickSettings, new RegExp(control));
  }
  for (const control of [
    'opencodeAuthOverwriteOnSwitch',
    'openclawAuthOverwriteOnSwitch',
    'hermesAuthOverwriteOnSwitch',
    'opencodeSyncOnSwitch',
    "handlePickAppPath('opencode')",
  ]) {
    assert.match(codexSettings, new RegExp(escapeRegExp(control)));
  }
});

test('XIASS material layer cannot alter Cockpit geometry or typography', () => {
  const styles = read('src/styles/xiass-glass-overlay.css');
  const stylesOutsideDedicatedLogoRules = styles
    .replace(/\.brand-logo__asset\s*\{[\s\S]*?\n\}/g, '')
    .replace(/\.brand-logo__asset--light\s*\{[\s\S]*?\n\}/g, '')
    .replace(/html\[data-theme='light'\] \.brand-logo__asset--(?:dark|light)\s*\{[\s\S]*?\n\}/g, '');

  assert.doesNotMatch(
    stylesOutsideDedicatedLogoRules,
    /^\s*(?:width|height|min-width|min-height|max-width|max-height|padding|margin|gap|display|position|inset|top|right|bottom|left|overflow|white-space|text-overflow|overflow-wrap|font-size|line-height|flex|grid|order)\s*:/m,
  );
  assert.match(styles, /backdrop-filter:/);
  assert.match(styles, /box-shadow:/);
  assert.match(styles, /border-color:/);
});

test('Codex provider save preview follows XIASS-managed storage instead of legacy paths', () => {
  const controller = read('src/components/codex/CodexModelProviderManager.tsx');
  const view = read('src/components/codex/CodexModelProviderManagerView.tsx');
  const changelog = read('CHANGELOG.md');
  const changelogZhCn = read('CHANGELOG.zh-CN.md');
  const fr = JSON.parse(read('src/locales/fr.json'));
  const ptBr = JSON.parse(read('src/locales/pt-br.json'));

  assert.match(
    controller,
    /providerStorePath:\s*"~\/\.xiass_tools\/codex_model_providers\.json"/,
  );
  assert.match(
    controller,
    /join\(home, "\.xiass_tools", "codex_model_providers\.json"\)/,
  );
  assert.doesNotMatch(controller, /\.antigravity_cockpit\/codex_model_providers\.json/);
  assert.match(view, /previewPaths\.providerStorePath/);
  assert.match(view, /"XIASS Tools 模型供应商数据"/);

  assert.match(changelog, /from earlier supported releases/);
  assert.doesNotMatch(changelog, /legacy Cockpit account data/);
  assert.match(changelogZhCn, /历史版本的账号数据/);
  assert.doesNotMatch(changelogZhCn, /旧 Cockpit 账号数据/);

  assert.equal(fr.settings.network.apiTitle, 'API locale XIASS Tools');
  assert.match(fr.codex.modelProviders.preview.providerStoreTitle, /XIASS Tools/);
  assert.equal(ptBr.settings.network.apiTitle, 'API local do XIASS Tools');
  assert.match(ptBr.codex.modelProviders.preview.providerStoreTitle, /XIASS Tools/);
});

test('dashboard stat cards keep their accessible interaction and original text hierarchy', () => {
  const source = read('src/pages/DashboardPage.tsx');
  const styles = read('src/pages/DashboardPage.css');

  assert.match(source, /className=\{`stat-icon-bg \$\{iconClass\} stat-icon-trigger`\}/);
  assert.match(
    source,
    /aria-label=\{`\$\{label\} · \$\{t\('dashboard\.interactiveIcon', '互动图标'\)\}`\}/,
  );
  assert.doesNotMatch(source, /className=\{`stat-icon-bg[^]*?onClick=\{\(event\) =>/);

  const halfLabelRule = styles.match(/\.half-label\s*\{([\s\S]*?)\}/)?.[1] || '';
  const emptySlotRule = styles.match(/\.empty-slot\s*\{([\s\S]*?)\}/)?.[1] || '';
  const emptyTextRule = styles.match(/\.empty-slot-text, \.no-data-text\s*\{([\s\S]*?)\}/)?.[1] || '';
  assert.match(halfLabelRule, /color:\s*var\(--text-muted\);/);
  assert.match(emptySlotRule, /color:\s*var\(--text-muted\);/);
  assert.match(emptyTextRule, /color:\s*var\(--text-muted\);/);
});

test('theme preference is persisted before a quick Settings-page navigation can restore stale UI', () => {
  const source = read('src/pages/SettingsPage.tsx');
  const app = read('src/App.tsx');
  const boot = read('index.html');
  const preference = read('src/utils/themePreference.ts');

  assert.match(source, /const pendingImmediateThemeRef = useRef<string \| null>\(null\);/);
  assert.match(source, /const handleThemePreferenceChange = \(rawTheme: string\) => \{/);
  assert.match(source, /generalStateRevisionRef\.current \+= 1;/);
  assert.match(source, /if \(stateRevisionAtStart !== generalStateRevisionRef\.current\)/);
  assert.match(source, /controllerDisposedRef\.current/);
  assert.match(source, /await invoke\('patch_general_config', \{ updates: \{ theme: nextTheme \} \}\);/);
  assert.match(source, /dispatchThemePreferenceIntent\(nextTheme\);/);
  assert.match(source, /setTheme: handleThemePreferenceChange,/);
  assert.match(
    source,
    /key === 'theme' && pendingImmediateThemeRef\.current === value/,
  );
  assert.match(app, /let visualConfigRequestVersion = 0;/);
  assert.match(app, /requestVersion !== visualConfigRequestVersion/);
  assert.match(app, /THEME_PREFERENCE_INTENT_EVENT/);
  assert.match(app, /resolveThemePreference\(config\.theme\)/);
  assert.match(source, /resolveThemePreference\(config\.theme\)/);
  assert.match(preference, /export const readPersistedThemePreference/);
  assert.match(preference, /return persistedPreference \?\? normalizedNativePreference;/);
  assert.match(boot, /xiass\.tools\.theme-preference/);
  assert.match(
    boot,
    /html\[data-theme="light"\][\s\S]*?radial-gradient\(circle at 12% 10%, rgba\(29, 78, 216, 0\.12\), transparent 45%\),[\s\S]*?linear-gradient\(180deg, #f8f7f4 0%, #eef1f6 100%\);/,
  );
  assert.match(boot, /document\.documentElement\.setAttribute\('data-theme', resolvedTheme\);/);
});

test('settings switches inherit their visible row title when the switch label has no text', () => {
  const source = read('src/pages/SettingsPageView.tsx');

  assert.match(source, /const wrappingLabel = control\.closest\('label'\);/);
  assert.match(source, /wrappingLabel\?\.textContent\?\.trim\(\)/);
  assert.match(source, /control\.setAttribute\('aria-label', label\);/);
  assert.doesNotMatch(source, /\|\| control\.closest\('label'\)/);
});

test('Codex session title search has an accessible name', () => {
  const source = read('src/components/codex/CodexSessionManager.tsx');
  assert.match(
    source,
    /aria-label=\{t\('codex\.sessionManager\.search\.titleAria', '按会话标题搜索'\)\}/,
  );
});

test('Codex session toolbar retains the original Cockpit layout rules', () => {
  const styles = read('src/styles/pages/codex-session-manager.css');
  const copyRule = styles.match(/\.codex-session-row__copy-button\s*\{([\s\S]*?)\}/)?.[1] || '';

  assert.doesNotMatch(styles, /@container\b/);
  assert.match(copyRule, /width:\s*28px;/);
  assert.match(copyRule, /height:\s*28px;/);
});

test('Codex API service retains its original page-level layout', () => {
  const styles = read('src/pages/CodexApiServicePage.css');

  assert.doesNotMatch(styles, /@container\b/);
  assert.match(styles, /\.codex-api-service-hero\s*\{/);
});

test('WebDAV actions keep their original settings controls', () => {
  const source = read('src/components/SettingsWebdavSyncSection.tsx');
  const styles = read('src/pages/settings/Settings.css');

  assert.match(source, /settings-webdav-status-row/);
  assert.doesNotMatch(styles, /@container\b/);
  assert.match(source, /className="settings-webdav-remote-toggle"/);
  assert.match(source, /aria-expanded=\{isRemoteExpanded\}/);
  assert.match(source, /<ChevronRight size=\{16\} aria-hidden="true" \/>/);
  assert.doesNotMatch(source, /settings-webdav-remote-header"\s*\n\s*style=\{\{ cursor:/);
});

test('settings controls retain Cockpit sizing and original responsive behavior', () => {
  const styles = read('src/pages/settings/Settings.css');

  assert.doesNotMatch(styles, /@container\b/);
  assert.match(styles, /\.settings-row\s*\{/);
});

test('Antigravity restores its integrated WF workspace while other primary pages keep Cockpit\'s direct hierarchy', () => {
  const app = read('src/App.tsx');
  const layoutStyles = read('src/styles/layout.css');
  const componentStyles = read('src/styles/components.css');
  const mainWrapper = componentStyles.match(/\.main-wrapper\s*\{([\s\S]*?)\n\}/)?.[1] || '';

  assert.equal((app.match(/<XiassAgentWorkspace/g) || []).length, 1);
  assert.match(app, /<XiassAgentWorkspace\s+module="antigravity"/);
  for (const panelId of ['official-accounts', 'instances', 'wakeup', 'verification']) {
    assert.match(app, new RegExp(`id: '${panelId}'`));
  }
  assert.match(app, /<AccountsPage onNavigate=\{setPage\} \/>/);
  assert.match(app, /<CodexAccountsPage \/>/);
  assert.match(app, /<ClaudeAccountsPage subPlatform="desktop" \/>/);
  assert.match(app, /<CursorAccountsPage \/>/);
  assert.match(app, /<WindsurfAccountsPage \/>/);
  assert.doesNotMatch(mainWrapper, /height:\s*100dvh;/);
  assert.doesNotMatch(mainWrapper, /overflow-y:\s*auto;/);
  assert.match(layoutStyles, /--side-nav-classic-width-expanded:\s*205px;/);
  assert.match(layoutStyles, /--side-nav-classic-width-collapsed:\s*84px;/);
});

test('window drag affordance retains the original Cockpit title-bar target', () => {
  const app = read('src/App.tsx');
  const layoutStyles = read('src/styles/layout.css');
  const dragRegion = layoutStyles.match(/\.drag-region\s*\{([\s\S]*?)\n\}/)?.[1] || '';

  assert.match(app, /className="drag-region"\s+data-tauri-drag-region\s+onMouseDown=\{handleDragStart\}/);
  assert.doesNotMatch(app, /drag-region__handle/);
  assert.match(dragRegion, /-webkit-app-region:\s*drag;/);
});

test('classic sidebar retains Cockpit\'s external collapse-handle placement', () => {
  const source = read('src/components/layout/SideNav.tsx');
  const styles = read('src/styles/layout.css');
  const handleRule = styles.match(/(?:^|\n)\.side-nav-classic-handle\s*\{([\s\S]*?)\}/)?.[1] || '';
  const handleIndex = source.indexOf('className={`side-nav-classic-handle');
  const closingNavIndex = source.lastIndexOf('</nav>');

  assert.ok(handleIndex > closingNavIndex);
  assert.match(styles, /--side-nav-classic-width-collapsed:\s*84px;/);
  assert.match(handleRule, /position:\s*fixed;/);
  assert.match(handleRule, /transform:\s*translate\(var\(--side-nav-classic-width-expanded\), -50%\);/);
});

test('2FA restores Cockpit\'s native vault rather than replacing it with an embedded frame', () => {
  const embeddedRoots = [
    path.resolve(NEXTGEN_ROOT, '..', 'macos', 'source', 'frontend'),
    path.resolve(NEXTGEN_ROOT, '..', 'windows', 'source', 'frontend'),
  ];

  for (const root of embeddedRoots) {
    const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8');
    const app = fs.readFileSync(path.join(root, 'src', 'App.vue'), 'utf8');
    assert.match(html, /<meta name="theme-color" content="#071c27"\s*\/>/);
    assert.match(html, /new URLSearchParams\(window\.location\.search\)\.get\('theme'\)/);
    assert.match(html, /html\[data-theme="light"\][\s\S]*?background:\s*#f6f5f2;/);
    assert.doesNotMatch(html, /background:\s*(?:white|#fff(?:fff)?)(?:;|\s)/i);
    assert.match(app, /type:\s*"xiass-wf-ready"/);
  }

  const twoFactor = read('src/pages/TwoFactorAuthPage.tsx');
  const mfaVault = read('src/utils/mfaVault.ts');
  assert.match(twoFactor, /<MfaVaultManager \/>/);
  assert.doesNotMatch(twoFactor, /<iframe/);
  assert.doesNotMatch(twoFactor, /wfBridge/);
  assert.doesNotMatch(mfaVault, /clearLegacyMfaBrowserStorage/);
});

test('embedded WF workspaces hand their full document height to the main XIASS scroll surface', () => {
  const workspace = read('src/pages/XiassAgentWorkspace.tsx');
  const workspaceStyles = read('src/pages/XiassAgentWorkspace.css');

  assert.match(workspace, /const EMBEDDED_WORKSPACE_MIN_HEIGHT = 520;/);
  assert.match(workspace, /const EMBEDDED_WORKSPACE_MAX_HEIGHT = 32_000;/);
  assert.match(workspace, /function normalizeEmbeddedWorkspaceHeight\(value: unknown\): number \| null/);
  assert.match(workspace, /event\.data\?\.type === 'xiass-wf-content-height'/);
  assert.match(workspace, /setIframeContentHeight\(\(current\)/);
  assert.match(workspace, /--xiass-embedded-frame-height/);
  assert.match(workspace, /activeTarget\?\.kind !== 'native' \|\| iframeContentHeight === null/);
  assert.match(
    workspaceStyles,
    /height:\s*var\(--xiass-embedded-frame-height, var\(--workspace-frame-fallback\)\);/,
  );

  for (const platform of ['macos', 'windows']) {
    const app = fs.readFileSync(
      path.resolve(NEXTGEN_ROOT, '..', platform, 'source', 'frontend', 'src', 'App.vue'),
      'utf8',
    );
    const styles = fs.readFileSync(
      path.resolve(NEXTGEN_ROOT, '..', platform, 'source', 'frontend', 'src', 'style', 'global.css'),
      'utf8',
    );
    assert.match(app, /type:\s*"xiass-wf-content-height"/);
    assert.match(app, /new ResizeObserver\(scheduleEmbeddedContentHeight\)/);
    assert.match(app, /const shell = document\.querySelector\("\.shell"\);/);
    assert.doesNotMatch(app, /document\.documentElement\.scrollHeight/);
    assert.match(app, /\[document\.body, document\.getElementById\("app"\), document\.querySelector\("\.shell"\)\]/);
    assert.match(app, /\.shell\.embedded \{[\s\S]*?height:\s*auto;/);
    assert.match(app, /\.shell\.embedded \.main \{[\s\S]*?overflow:\s*visible;/);
    assert.match(
      styles,
      /:root\[data-embedded="true"\],\s*:root\[data-embedded="true"\] body,\s*:root\[data-embedded="true"\] #app\s*\{[\s\S]*?height:\s*auto;[\s\S]*?overflow:\s*visible;/,
    );
  }
});

test('embedded agent toolbars remove duplicate titles while preserving local actions', () => {
  for (const platform of ['macos', 'windows']) {
    const tools = fs.readFileSync(
      path.resolve(NEXTGEN_ROOT, '..', platform, 'source', 'frontend', 'src', 'views', 'Tools.vue'),
      'utf8',
    );

    assert.match(tools, /:global\(:root\[data-embedded="true"\] \.tools-view\)/);
    assert.match(tools, /:global\(:root\[data-embedded="true"\] \.tools-heading\)\s*\{\s*display:\s*none;/);
    assert.match(tools, /:global\(:root\[data-embedded="true"\] \.tools-header\)\s*\{[\s\S]*?min-height:\s*38px;[\s\S]*?justify-content:\s*flex-end;/);
    assert.doesNotMatch(tools, /:global\(:root\[data-embedded="true"\]\) \./);
    assert.match(tools, /grid-auto-flow:\s*column;/);
    assert.match(tools, /overflow-x:\s*auto;/);
  }
});

test('unified account workspaces start compact while keeping explanatory notices available', () => {
  const sharedPageHook = read('src/hooks/useProviderAccountsPage.ts');
  const claudePage = read('src/pages/ClaudeAccountsPage.tsx');
  const glassStyles = read('src/styles/xiass-liquid-glass.css');

  assert.match(sharedPageHook, /const flowNoticePreferenceKey = flowNoticeCollapsedKey\s*\? `\$\{flowNoticeCollapsedKey\}\.v2`/);
  assert.match(sharedPageHook, /return persisted === null \? true : persisted === '1';/);
  assert.match(claudePage, /CLAUDE_FLOW_NOTICE_COLLAPSED_KEY = 'agtools\.claude\.flow_notice_collapsed\.v2';/);
  assert.match(claudePage, /return stored !== 'false';/);
  assert.match(glassStyles, /\.xiass-agent-workspace__panel \.ghcp-flow-notice-toggle\s*\{[\s\S]*?min-height:\s*36px !important;/);
  assert.match(glassStyles, /\.xiass-agent-workspace__panel :is\(\.ghcp-accounts-page, \.codex-accounts-page\)\s*\{[\s\S]*?gap:\s*10px;/);
  assert.match(glassStyles, /\.xiass-agent-workspace__panel \.toolbar\s*\{[\s\S]*?padding:\s*7px 9px;/);
  assert.match(glassStyles, /\.xiass-agent-workspace__panel \.toolbar :is\([\s\S]*?min-height:\s*44px !important;/);
});

test('light theme restores the original Cockpit canvas and avoids dark loading flashes', () => {
  const styles = read('src/styles/xiass-liquid-glass.css');
  const base = read('src/styles/base.css');
  const workspace = read('src/pages/XiassAgentWorkspace.css');
  const boot = read('index.html');
  const lightBackground = styles.match(
    /\[data-theme='light'\] body,\s*html\[data-theme='light'\] body\s*\{([\s\S]*?)\n\}/,
  )?.[1] || '';
  const lightTokens = styles.match(
    /html\[data-theme='light'\]\s*\{([\s\S]*?)\n\}/,
  )?.[1] || '';

  assert.match(base, /--bg-card:\s*rgba\(255, 255, 255, 0\.75\);/);
  assert.match(lightTokens, /--primary:\s*#1d4ed8;/);
  assert.match(lightTokens, /--accent:\s*#0ea5a5;/);
  assert.match(lightTokens, /--bg-primary:\s*#f6f5f2;/);
  assert.match(lightTokens, /--bg-secondary:\s*#ffffff;/);
  assert.match(lightTokens, /--bg-card:\s*rgba\(255, 255, 255, 0\.75\);/);
  assert.match(lightTokens, /--text-primary:\s*#0f172a;/);
  assert.match(lightBackground, /linear-gradient\(180deg, #f8f7f4 0%, #eef1f6 100%\)\s*!important;/);
  assert.doesNotMatch(lightBackground, /xiass-api-space-bg/);
  assert.match(lightBackground, /color:\s*var\(--text-primary\);/);
  assert.match(lightBackground, /color-scheme:\s*light;/);
  assert.match(styles, /html\[data-theme='light'\]\s*\{[\s\S]*?--xiass-glass-blur:\s*none;/);
  assert.match(
    styles,
    /html\[data-theme='light'\] #app-boot-splash\s*\{[\s\S]*?radial-gradient\(circle at 12% 10%, rgba\(29, 78, 216, 0\.12\), transparent 45%\),[\s\S]*?linear-gradient\(180deg, #f8f7f4 0%, #eef1f6 100%\)\s*!important;[\s\S]*?color:\s*#0f172a;/,
  );
  assert.match(workspace, /--embedded-frame-background, var\(--bg-primary\)/);
  assert.match(workspace, /--embedded-frame-background:\s*var\(--bg-primary\);/);
  assert.match(
    boot,
    /html\[data-theme="light"\][\s\S]*?radial-gradient\(circle at 12% 10%, rgba\(29, 78, 216, 0\.12\), transparent 45%\),[\s\S]*?linear-gradient\(180deg, #f8f7f4 0%, #eef1f6 100%\);/,
  );
  assert.doesNotMatch(boot, /#06131f 0%, #082630 58%, #0a313b 100%/);
  assert.match(styles, /Restore the original light presentation after the shared dark glass rules/);
  assert.match(
    styles,
    /html\[data-theme='light'\] :is\([\s\S]*?\.modal-content,[\s\S]*?backdrop-filter:\s*none !important;/,
  );
  assert.match(
    styles,
    /html\[data-theme='light'\] \.settings-container \.group-title[\s\S]*?color:\s*var\(--text-primary\) !important;[\s\S]*?text-shadow:\s*none;/,
  );
  assert.doesNotMatch(styles, /\.settings-container \.group-title[\s\S]{0,180}color:\s*#effcff;/);
  assert.match(
    styles,
    /html\[data-theme='light'\] \.side-nav\.side-nav-classic\s*\{[\s\S]*?background:\s*var\(--bg-secondary\) !important;/,
  );
  assert.match(
    styles,
    /html\[data-theme='light'\] \.side-nav\.side-nav-classic \.nav-item\.active\s*\{[\s\S]*?background:\s*var\(--primary-light\);[\s\S]*?color:\s*var\(--primary\);/,
  );
  assert.match(
    styles,
    /\.card,[\s\S]*?background:\s*linear-gradient\([\s\S]*?var\(--xiass-glass-surface\)\s*!important;/,
  );

  for (const platform of ['macos', 'windows']) {
    const embeddedStyles = fs.readFileSync(
      path.resolve(NEXTGEN_ROOT, '..', platform, 'source', 'frontend', 'src', 'style', 'global.css'),
      'utf8',
    );
    const embeddedLightTokens = embeddedStyles.match(
      /:root\[data-theme="light"\]\s*\{([\s\S]*?)\n\}/,
    )?.[1] || '';
    assert.match(embeddedLightTokens, /--bg-base:\s*#f6f5f2;/);
    assert.match(embeddedLightTokens, /--bg-elevated:\s*#ffffff;/);
    assert.match(embeddedLightTokens, /--bg-card:\s*rgba\(255, 255, 255, 0\.75\);/);
    assert.match(embeddedLightTokens, /--text-primary:\s*#0f172a;/);
    assert.match(embeddedLightTokens, /--accent:\s*#1d4ed8;/);
  }
});

test('light material keeps interactive text legible and restores the transparent XIASS empty-state mark', () => {
  const material = read('src/styles/xiass-glass-overlay.css');
  const accountsOverview = read('src/pages/AccountsOverviewView.tsx');
  const sideNav = read('src/components/layout/SideNav.tsx');
  const settings = read('src/pages/SettingsPageView.tsx');
  const apiKeyFun = read('src/pages/ApiKeyFunPage.tsx');
  const components = read('src/styles/components.css');

  assert.match(material, /--text-tertiary:\s*#475569;/);
  assert.match(material, /--text-muted:\s*#64748b;/);
  assert.match(material, /\.modal-tab\.active\s*\{[\s\S]*?background:\s*var\(--gradient-primary\) !important;[\s\S]*?color:\s*#fff !important;/);
  assert.match(material, /html\[data-theme='light'\] \.modal-tab:not\(\.active\):hover:not\(:disabled\)\s*\{[\s\S]*?color:\s*var\(--text-primary\) !important;/);
  assert.match(material, /html\[data-theme='light'\] \.btn\.btn-primary:hover:not\(:disabled\)\s*\{[\s\S]*?color:\s*#fff !important;/);
  assert.match(
    material,
    /\.brand-logo,[\s\S]*?\.empty-state \.icon\.empty-state-brand-mark[\s\S]*?background:\s*transparent !important;[\s\S]*?border-color:\s*transparent !important;[\s\S]*?box-shadow:\s*none !important;/,
  );
  assert.match(accountsOverview, /import xiassToolsLogo from '\.\.\/\.\.\/src-tauri\/icons\/icon\.png';/);
  assert.match(accountsOverview, /import xiassToolsLightLogo from '\.\.\/assets\/xiass-tools-logo-light\.png';/);
  for (const source of [sideNav, settings, apiKeyFun]) {
    assert.match(source, /src-tauri\/icons\/icon\.png/);
  }
  assert.equal(
    sha256('src-tauri/icons/icon.png'),
    '578a1efa0454ab664a8616c4f0d448f433ae85c615396fd4c4638a95ead16d12',
  );
  assert.equal(
    sha256('src/assets/xiass-tools-logo-light.png'),
    '4c07e8f4c3b7e1c9d9ced303441d1958278522ccde45981eb2e7470274badc0c',
  );
  assert.match(accountsOverview, /className="icon empty-state-brand-mark"/);
  assert.match(accountsOverview, /empty-state-brand-mark__asset--dark/);
  assert.match(accountsOverview, /empty-state-brand-mark__asset--light/);
  assert.match(components, /\.empty-state \.empty-state-brand-mark__asset\s*\{/);
  assert.match(components, /html\[data-theme='light'\] \.empty-state \.empty-state-brand-mark__asset--dark\s*\{[\s\S]*?display:\s*none;/);
  assert.match(components, /html\[data-theme='light'\] \.empty-state \.empty-state-brand-mark__asset--light\s*\{[\s\S]*?display:\s*block;/);
});

test('browser preview reads the app version from the release source of truth', () => {
  const source = read('src/main.tsx');

  assert.match(source, /import packageJson from "\.\.\/package\.json";/);
  assert.match(source, /const BROWSER_PREVIEW_APP_VERSION = packageJson\.version;/);
  assert.match(source, /if \(command === "plugin:app\|version"\)\s*\{\s*return BROWSER_PREVIEW_APP_VERSION;/);
  assert.doesNotMatch(source, /return "1\.7\.0";/);
});

test('About keeps upstream attribution behind an accessible license-notice dialog', () => {
  const source = read('src/pages/SettingsPageView.tsx');
  const service = read('src/services/legalNoticesService.ts');

  assert.match(source, /const \[licenseNoticeOpen, setLicenseNoticeOpen\] = useState\(false\);/);
  assert.match(source, /useEscClose\(licenseNoticeOpen, \(\) => setLicenseNoticeOpen\(false\)\);/);
  assert.match(source, /aria-haspopup="dialog"/);
  assert.match(source, /aria-expanded=\{licenseNoticeOpen\}/);
  assert.match(source, /role="dialog"[\s\S]*aria-modal="true"[\s\S]*aria-labelledby="settings-license-title"/);
  assert.match(source, /role="tablist"[\s\S]*settings-license-tab/);
  assert.match(source, /settings-license-document[\s\S]*role="tabpanel"/);
  assert.match(source, /onKeyDown=\{\(event\) => handleLegalNoticeTabKeyDown\(event, index\)\}/);
  assert.match(source, /onKeyDown=\{handleLicenseDialogKeyDown\}/);
  assert.match(source, /data-license-dialog-focusable/);
  assert.match(source, /legalNoticesReloadVersion/);
  assert.match(source, /role="alert"/);
  assert.match(source, /retryLegalNoticeLoad/);
  assert.match(source, /aria-busy=\{legalNoticesLoading\}/);
  assert.match(source, /loadLegalNotices\(\)/);
  assert.match(service, /invoke<unknown>\('load_legal_notices'\)/);
  assert.match(service, /LEGAL_NOTICE_IDS/);
  assert.match(service, /notices\.length !== LEGAL_NOTICE_IDS\.length/);
  assert.match(service, /returnedIds\.size !== LEGAL_NOTICE_IDS\.length/);
  assert.doesNotMatch(source, /github\.com\/jlcodes99\/cockpit-tools|CC BY-NC-SA 4\.0/);
  assert.doesNotMatch(source, /settings\.about\.upstreamAttribution/);
});

test('reachable workspaces avoid structural emoji and broad transitions', () => {
  const announcementSource = read('src/components/AnnouncementCenter.tsx');
  const announcementStyles = read('src/components/AnnouncementCenter.css');
  const instanceStyles = read('src/styles/pages/instances.css');
  const apiServiceSource = read('src/pages/CodexApiServiceView.tsx');
  assert.doesNotMatch(announcementSource, /🚨/);
  assert.doesNotMatch(announcementStyles, /transition:\s*all/);
  assert.doesNotMatch(instanceStyles, /transition:\s*all/);
  assert.doesNotMatch(apiServiceSource, />\s*→\s*</);
  assert.match(apiServiceSource, /<ArrowRight size=\{16\} aria-hidden="true" \/>/);

  const embeddedRoots = [
    path.resolve(NEXTGEN_ROOT, '..', 'macos', 'source', 'frontend'),
    path.resolve(NEXTGEN_ROOT, '..', 'windows', 'source', 'frontend'),
  ];
  for (const root of embeddedRoots) {
    const dashboard = fs.readFileSync(path.join(root, 'src', 'views', 'Dashboard.vue'), 'utf8');
    const accounts = fs.readFileSync(path.join(root, 'src', 'views', 'Accounts.vue'), 'utf8');
    assert.doesNotMatch(dashboard, /transition:\s*all/);
    assert.doesNotMatch(accounts, /◌/);
    assert.match(accounts, /class="empty-icon" aria-hidden="true"/);
  }
});

test('instance editor exposes a named modal, labelled fields and full-size choices', () => {
  const source = read('src/components/InstancesManager.tsx');
  const styles = read('src/styles/pages/instances.css');

  assert.match(source, /role="dialog"\s*\n\s*aria-modal="true"\s*\n\s*aria-labelledby="instance-editor-title"/);
  assert.match(source, /<h2 id="instance-editor-title">/);
  for (const id of [
    'instance-editor-name',
    'instance-editor-path',
    'instance-editor-working-dir',
    'instance-editor-extra-args',
  ]) {
    assert.match(source, new RegExp(`htmlFor="${id}"`));
    assert.match(source, new RegExp(`id="${id}"`));
  }
  const optionRule = styles.match(/\.instances-page \.instance-init-mode-option\s*\{([\s\S]*?)\}/)?.[1] || '';
  assert.match(optionRule, /min-height:\s*44px;/);
  assert.doesNotMatch(optionRule, /transition:\s*all/);
});

test('Claude account entry fields have explicit accessible names', () => {
  const source = read('src/pages/ClaudeAccountsPage.tsx');

  assert.match(source, /aria-label=\{t\('claude\.desktopOAuth\.nameLabel', '账号名称'\)\}/);
  assert.match(source, /aria-label=\{t\('claude\.oauth\.callbackLabel', '回调链接或授权 code'\)\}/);
  assert.match(source, /aria-label=\{t\('claude\.oauth\.emailLabel', '邮箱提示'\)\}/);
});

test('Cursor and Windsurf OAuth progress and errors are live regions', () => {
  for (const file of ['src/pages/CursorAccountsPage.tsx', 'src/pages/WindsurfAccountsPage.tsx']) {
    const source = read(file);
    assert.match(source, /className="add-status error" role="alert" aria-live="assertive"/);
    assert.match(source, /className="add-status loading" role="status" aria-live="polite"/);
    assert.match(source, /aria-label=\{t\('accounts\.oauth\.linkLabel', '授权链接'\)\}/);
    assert.match(source, /aria-label=\{t\('common\.shared\.oauth\.deviceCode', '设备验证码'\)\}/);
    assert.match(source, /aria-label=\{t\('common\.copy', '复制'\)\}/);
  }
  assert.match(read('src/pages/WindsurfAccountsPage.tsx'), /htmlFor="windsurf-oauth-manual-callback"/);
});

test('Codex session copy actions retain their original compact control geometry', () => {
  const styles = read('src/styles/pages/codex-session-manager.css');
  const copyRule = styles.match(/\.codex-session-row__copy-button\s*\{([\s\S]*?)\}/)?.[1] || '';

  assert.match(copyRule, /width:\s*28px;/);
  assert.match(copyRule, /height:\s*28px;/);
  assert.match(copyRule, /transition:\s*all\s+0\.18s\s+ease;/);
});

test('all primary shell and embedded WF actions keep a 44px interaction target', () => {
  const liquidStyles = read('src/styles/xiass-liquid-glass.css');
  assert.match(liquidStyles, /:where\(button, \[role='button'\], \[role='tab'\]\)\s*\{[^}]*min-width:\s*44px\s*!important;[^}]*min-height:\s*44px\s*!important;/s);
  assert.match(liquidStyles, /\.btn:not\(\.btn-sm\)[^}]*min-height:\s*44px\s*!important;/s);

  const embeddedRoots = [
    path.resolve(NEXTGEN_ROOT, '..', 'macos', 'source', 'frontend'),
    path.resolve(NEXTGEN_ROOT, '..', 'windows', 'source', 'frontend'),
  ];

  for (const root of embeddedRoots) {
    const globalStyles = fs.readFileSync(path.join(root, 'src', 'style', 'global.css'), 'utf8');
    const buttonComponent = fs.readFileSync(path.join(root, 'src', 'components', 'ui', 'Button.vue'), 'utf8');
    const dashboard = fs.readFileSync(path.join(root, 'src', 'views', 'Dashboard.vue'), 'utf8');
    assert.match(globalStyles, /:root\[data-embedded="true"\] button\s*\{[^}]*min-width:\s*44px;[^}]*min-height:\s*44px\s*!important;/s);
    assert.match(buttonComponent, /\.s-md\s*\{[^}]*min-height:\s*44px;/s);
    assert.match(buttonComponent, /\.s-sm\s*\{[^}]*min-height:\s*44px;/s);
    assert.match(buttonComponent, /:aria-busy="loading \|\| undefined"/);
    assert.match(buttonComponent, /aria-hidden="true"/);
    assert.match(buttonComponent, /<span class="btn-label"><slot \/><\/span>/);
    assert.doesNotMatch(buttonComponent, /<slot v-else\s*\/>/);
    assert.match(dashboard, /aria-label="刷新首页"/);
  }
});

test('embedded WF controls preserve keyboard focus rings on both platforms', () => {
  const roots = [
    path.resolve(NEXTGEN_ROOT, '..', 'macos', 'source', 'frontend', 'src', 'style', 'global.css'),
    path.resolve(NEXTGEN_ROOT, '..', 'windows', 'source', 'frontend', 'src', 'style', 'global.css'),
  ];

  for (const file of roots) {
    const styles = fs.readFileSync(file, 'utf8');
    assert.match(styles, /select:focus-visible[\s\S]{0,180}?outline:\s*2px\s+solid/);
    assert.match(styles, /select:focus:not\(:focus-visible\)\s*\{\s*outline:\s*none;/);
    assert.doesNotMatch(styles, /select:focus\s*\{\s*outline:\s*none;/);
  }
});

test('verification table checkbox hit areas meet the 44px interaction target', () => {
  const styles = read('src/styles/pages/wakeup-verification.css');
  const targetRule = styles.match(/\.verification-check-target\s*\{([\s\S]*?)\}/)?.[1] || '';

  assert.match(targetRule, /width:\s*44px;/);
  assert.match(targetRule, /height:\s*44px;/);
});

test('platform layout editor exposes a named modal dialog', () => {
  const source = read('src/components/PlatformLayoutModal.tsx');

  assert.match(source, /role="dialog"/);
  assert.match(source, /aria-modal="true"/);
  assert.match(source, /aria-labelledby="platform-layout-dialog-title"/);
  assert.match(source, /<h2 id="platform-layout-dialog-title">/);
});

test('liquid glass form surfaces do not erase checkbox and radio state colors', () => {
  const styles = read('src/styles/xiass-liquid-glass.css');
  const formSurfaceRule = styles.match(
    /\/\* Form controls remain stronger than cards[^]*?\*\/\s*([^]*?)\{([^]*?)\}/,
  );

  assert.ok(formSurfaceRule, 'liquid glass form-surface rule should exist');
  const selectors = formSurfaceRule[1];
  assert.match(selectors, /input:not\(\[type='checkbox'\]\)/);
  assert.match(selectors, /:not\(\[type='radio'\]\)/);
  assert.match(selectors, /:not\(\[type='range'\]\)/);
  assert.match(selectors, /:not\(\[type='color'\]\)/);
  assert.match(selectors, /:not\(\[type='file'\]\)/);
  assert.doesNotMatch(selectors, /(^|\n)input\s*,/);
});

test('announcement list, detail and image preview expose named modal dialogs', () => {
  const source = read('src/components/AnnouncementCenter.tsx');

  assert.match(source, /aria-labelledby="announcement-list-dialog-title"/);
  assert.match(source, /id="announcement-list-dialog-title"/);
  assert.match(source, /aria-labelledby="announcement-detail-dialog-title"/);
  assert.match(source, /id="announcement-detail-dialog-title"/);
  assert.match(source, /aria-label=\{t\('announcement\.imagePreview', '图片预览'\)\}/);
});

test('critical icon-only dismiss and account actions expose explicit accessible names', () => {
  const checks = [
    ['src/components/AccountGroupModal.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/CloseConfirmDialog.tsx', /className="close-dialog-x"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/CodexAccountGroupModal.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/FileCorruptedModal.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/GroupSettingsModal.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/SilentUpdateToast.tsx', /className="silent-update-toast-close"[\s\S]{0,180}?aria-label=\{t\('common\.close'/],
    ['src/components/TagEditModal.tsx', /className="btn btn-secondary tag-add-btn"[\s\S]{0,260}?aria-label=\{t\('common\.cancel'/],
    ['src/components/codebuddy/CodebuddySessionManager.tsx', /className="codex-session-manager__search-clear"[\s\S]{0,180}?aria-label=\{t\('common\.clear'/],
    ['src/components/codebuddy-suite/CodebuddySuiteCheckinModal.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/codebuddy-suite/TraeAutoCheckinConfigModal.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/codebuddy-suite/TraeCheckinModal.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/codebuddy-suite/WorkbuddyAutoCheckinConfigModal.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/components/codex/CodexWakeupContent.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/pages/CodebuddyAccountsPage.tsx', /className="action-btn success"[\s\S]{0,320}?aria-label=\{t\('common\.shared\.switchAccount'/],
    ['src/pages/CodexAccountsOverviewPanel.tsx', /setMessage\(null\)[\s\S]{0,160}?aria-label=\{t\("common\.close"/],
    ['src/pages/CodexAccountsView.tsx', /className="modal-close"[\s\S]{0,180}?aria-label=\{t\("common\.close"/],
    ['src/pages/GitHubCopilotAccountsPage.tsx', /setMessage\(null\)[\s\S]{0,160}?aria-label=\{t\('common\.close'/],
    ['src/pages/KiroAccountsPage.tsx', /setMessage\(null\)[\s\S]{0,160}?aria-label=\{t\('common\.close'/],
    ['src/pages/WakeupVerificationPage.tsx', /className="modal-close"[^>]*aria-label=\{t\('common\.close'/],
    ['src/pages/ZedAccountsPage.tsx', /setMessage\(null\)[\s\S]{0,160}?aria-label=\{t\('common\.close'/],
  ];

  for (const [relativePath, pattern] of checks) {
    assert.match(read(relativePath), pattern, `${relativePath} should name its icon-only action`);
  }

  const sharedAccounts = read('src/components/codebuddy-suite/CodebuddySuiteAccountsSharedView.tsx');
  for (const labelKey of [
    'common.shared.switchAccount',
    'accounts.editTags',
    'common.shared.refreshQuota',
    'common.shared.export.title',
    'common.delete',
  ]) {
    assert.match(
      sharedAccounts,
      new RegExp(`className="action-btn(?: success| danger)?"[\\s\\S]{0,360}?aria-label=\\{t\\("${escapeRegExp(labelKey)}"`),
    );
  }
});

test('Workbuddy history disclosure button performs one named stateful action', () => {
  const source = read('src/components/codebuddy-suite/WorkbuddyAutoCheckinConfigModal.tsx');
  const disclosure = source.match(
    /<button[\s\S]{0,900}?className="btn-icon-toggle"[\s\S]{0,900}?<\/button>/,
  )?.[0] || '';

  assert.match(disclosure, /type="button"/);
  assert.match(disclosure, /event\.stopPropagation\(\);/);
  assert.match(disclosure, /toggleExpand\(log\.id\);/);
  assert.match(disclosure, /aria-label=\{/);
  assert.match(disclosure, /common\.collapse/);
  assert.match(disclosure, /common\.expand/);
  assert.match(disclosure, /aria-expanded=\{isExpanded\}/);
});

test('page titles retain Cockpit\'s gradient treatment while manual blocks use vector icons', () => {
  const componentStyles = read('src/styles/components.css');
  const manualSource = read('src/pages/ManualPage.tsx');
  const titleRule = componentStyles.match(/\.page-title\s*\{([\s\S]*?)\}/)?.[1] || '';

  assert.match(titleRule, /background:\s*var\(--gradient-primary\);/);
  assert.match(titleRule, /-webkit-text-fill-color:\s*transparent;/);
  assert.match(manualSource, /<Lightbulb size=\{16\} aria-hidden="true" \/>/);
  assert.match(manualSource, /<ListChecks size=\{16\} aria-hidden="true" \/>/);
  assert.match(manualSource, /<TriangleAlert size=\{16\} aria-hidden="true" \/>/);
  assert.doesNotMatch(manualSource, /<h4>💡|<h4>🎯|<h4>⚠️/);
});

test('Codex config recovery service keeps the Tauri contract metadata-only', () => {
  const service = read('src/services/codexService.ts');
  const types = read('src/types/codex.ts');
  const metadata = types.match(/export interface CodexConfigBackupInfo\s*\{([\s\S]*?)\n\}/)?.[1] || '';
  const verification = types.match(
    /export interface CodexConfigBackupVerification\s*\{([\s\S]*?)\n\}/,
  )?.[1] || '';
  const restore = types.match(/export interface CodexConfigRestoreResult\s*\{([\s\S]*?)\n\}/)?.[1] || '';

  assert.match(service, /export async function listCodexConfigBackups\(\): Promise<CodexConfigBackupInfo\[\]>\s*\{[\s\S]{0,180}?invoke\('list_codex_config_backups'\)/);
  assert.match(service, /export async function verifyCodexConfigBackup\([\s\S]{0,220}?invoke\('verify_codex_config_backup', \{ backupId \}\)/);
  assert.match(service, /export async function restoreCodexConfigBackup\([\s\S]{0,220}?invoke\('restore_codex_config_backup', \{ backupId \}\)/);

  assert.match(metadata, /\bcreatedAt:\s*number;/);
  assert.match(metadata, /\bsource:\s*string;/);
  assert.match(metadata, /\bbytes:\s*number;/);
  assert.match(metadata, /\bvalid:\s*boolean;/);
  assert.doesNotMatch(metadata, /\b(?:path|content|snapshot|backupDir|configPath)\b/i);
  assert.match(verification, /\bid:\s*string;/);
  assert.match(verification, /\bvalid:\s*boolean;/);
  assert.match(restore, /\brestoredBackupId:\s*string;/);
  assert.match(restore, /\bsafetyBackupId:\s*string;/);
  assert.doesNotMatch(restore, /\b(?:path|content|snapshot|backupDir|configPath)\b/i);
});

test('Codex config recovery UI guards a verified restore without rendering secret config data', (t) => {
  const componentDirectory = path.join(NEXTGEN_ROOT, 'src', 'components', 'codex');
  const candidates = [
    'CodexConfigRecoveryModal.tsx',
    'CodexConfigBackupsModal.tsx',
    'CodexConfigBackupRecoveryModal.tsx',
  ];
  let recoveryPath = candidates
    .map((candidate) => path.join(componentDirectory, candidate))
    .find((candidate) => fs.existsSync(candidate));

  if (!recoveryPath && fs.existsSync(componentDirectory)) {
    const stack = [componentDirectory];
    while (stack.length > 0 && !recoveryPath) {
      const directory = stack.pop();
      for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
        const entryPath = path.join(directory, entry.name);
        if (entry.isDirectory()) {
          stack.push(entryPath);
          continue;
        }
        if (!/\.(?:tsx|ts)$/.test(entry.name)) continue;
        const source = fs.readFileSync(entryPath, 'utf8');
        if (
          source.includes('verifyCodexConfigBackup(') &&
          source.includes('restoreCodexConfigBackup(')
        ) {
          recoveryPath = entryPath;
          break;
        }
      }
    }
  }

  if (!recoveryPath) {
    t.skip('Codex configuration recovery UI has not been added yet');
    return;
  }

  const source = fs.readFileSync(recoveryPath, 'utf8');
  const quickConfig = read('src/components/codex/CodexQuickConfigCard.tsx');

  assert.match(source, /role="dialog"/);
  assert.match(source, /aria-modal=(?:"true"|\{true\})/);
  assert.match(source, /aria-labelledby=/);
  assert.match(source, /verifyCodexConfigBackup\(/);
  assert.match(source, /restoreCodexConfigBackup\(/);
  assert.match(source, /useEscClose\(open && !interactionLocked, requestClose\)/);
  assert.match(source, /disabled=\{interactionLocked\}/);
  assert.match(
    source,
    /if\s*\([^)]*(?:verification|verified|canRestore|restoreReady)[^)]*\)[\s\S]{0,900}?restoreCodexConfigBackup\(/i,
    'restoring must remain guarded by a current verification result',
  );
  assert.doesNotMatch(source, /\b(?:backup|selectedBackup)\.(?:content|path|configPath|snapshot|sha256)\b/i);
  assert.doesNotMatch(source, /getCodexConfigTomlPath\(/);
  assert.match(
    quickConfig,
    /(?:CodexConfigRecovery|CodexConfigBackup|showRecovery|恢复点)/,
    'the quick-config dialog should expose the recovery entry point',
  );
});
