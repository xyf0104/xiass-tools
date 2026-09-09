<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { open, save } from "@tauri-apps/plugin-dialog";
  import AppIcon from "../components/AppIcon.svelte";
  import { twoFactorApi, totpInput, codeRemaining, type TOTPInput, type TOTPEntry, type TOTPCode, type TOTPResult } from "../lib/twoFactor";
  import { decodeTOTPQrImage, parseTOTPQrPayload, mergeTOTPQrMigrationBatch, migrationBatchCredentials, type MigrationBatch } from "../lib/totpQrImport";
  import { actionButtonRecipe, panelRecipe, routeStackRecipe, spinRecipe, topStripRecipe } from "../../styled-system/recipes";

  interface RecentQuery { id: string; entry: TOTPEntry; input?: TOTPInput; savedId?: string; at: number }
  type ModalMode = "export" | "import" | "delete" | null;
  let entries: TOTPEntry[] = [];
  // These drafts, queries and codes never enter a shared store or localStorage.
  let recent: RecentQuery[] = [];
  let codes: Record<string, TOTPCode> = {};
  let currentQuery: RecentQuery | null = null;
  let loading = false;
  let busy = "";
  let error = "";
  let notice = "";
  let value = "";
  let label = "";
  let algorithm = "SHA1";
  let digits = 6;
  let period = 30;
  let activeList: "saved" | "recent" = "saved";
  let qrInput: HTMLInputElement;
  let migration: MigrationBatch | null = null;
  let migrationSaved = new Set<string>();
  let now = Date.now();
  let clock: ReturnType<typeof setInterval> | undefined;
  let disposed = false;
  let refreshingCodes = false;
  let modal: ModalMode = null;
  let dialog: HTMLDialogElement;
  let password = "";
  let confirmation = "";
  let dialogError = "";
  let deleting: TOTPEntry | null = null;

  $: migrationCredentials = migrationBatchCredentials(migration);
  $: shownEntries = activeList === "saved" ? entries : recent.map((item) => ({ ...item.entry, id: item.id }));
  $: previewCode = currentQuery ? codes[currentQuery.id] : undefined;
  $: modalTitle = modal === "export" ? "导出加密备份" : modal === "import" ? "导入加密备份" : "删除本地验证器";
  $: if (dialog) {
    if (modal && !dialog.open) dialog.showModal();
    else if (!modal && dialog.open) dialog.close();
  }

  function check<T extends TOTPResult>(result: T): T {
    if (!result.ok) throw new Error(result.message || "操作未完成，请重试。");
    return result;
  }
  function message(cause: unknown) { return cause instanceof Error ? cause.message : "操作未完成，请重试。"; }
  function clearFeedback() { error = ""; notice = ""; }
  function clearDraft() { value = ""; label = ""; algorithm = "SHA1"; digits = 6; period = 30; }
  function invalidateQuery() { currentQuery = null; clearFeedback(); }
  function migrationKey(input: TOTPInput) { return [input.secret, input.algorithm, input.digits, input.period].join(":"); }

  async function load() {
    if (loading || busy) return;
    loading = true;
    try {
      const result = check(await twoFactorApi.list());
      if (disposed) return;
      entries = result.entries || [];
      const live = new Set([...entries.map((entry) => entry.id), ...recent.map((item) => item.id)]);
      codes = Object.fromEntries(Object.entries(codes).filter(([id]) => live.has(id)));
      error = "";
    } catch (cause) { if (!disposed) error = message(cause); }
    finally { loading = false; }
  }

  function acceptMigration(raw: string): boolean {
    if (!raw.startsWith("otpauth-migration://")) return false;
    const parsed = parseTOTPQrPayload(raw);
    if (!parsed || parsed.kind !== "migration") throw new Error("迁移链接无效，或未包含支持的 TOTP 账号。");
    if (migration && migration.batchId !== parsed.batch.batchId) throw new Error("请先保存或清除当前迁移批次，再导入另一组二维码。");
    migration = mergeTOTPQrMigrationBatch(migration, parsed.batch);
    currentQuery = null;
    notice = `已识别 ${migration.parts.length} / ${migration.batchSize} 张迁移二维码。收齐后点击“保存到列表”。`;
    value = "";
    return true;
  }

  async function query() {
    if (busy || disposed) return;
    clearFeedback(); currentQuery = null; busy = "query";
    try {
      if (acceptMigration(value.trim())) return;
      if (migration) throw new Error("请先保存或清除当前迁移批次，再查询单个验证器。");
      const input = totpInput(value, label, algorithm, digits, period);
      const result = check(await twoFactorApi.preview(input));
      if (disposed) return;
      const id = `query-${crypto.randomUUID()}`;
      const item: RecentQuery = { id, entry: result.entry, input, at: Date.now() };
      recent = [item, ...recent].slice(0, 12);
      currentQuery = item;
      const live = new Set([...entries.map((entry) => entry.id), ...recent.map((query) => query.id)]);
      codes = { ...Object.fromEntries(Object.entries(codes).filter(([key]) => live.has(key))), [id]: result.code };
      notice = "验证码已生成。查询仅在本次页面内保留；需要长期使用时请保存到列表。";
    } catch (cause) { if (!disposed) error = message(cause); }
    finally { busy = ""; }
  }

  async function saveCurrent() {
    if (busy || loading) return;
    if (migration) { await saveMigration(); return; }
    const item = currentQuery;
    if (!item?.input || item.savedId) return;
    busy = "save"; clearFeedback();
    try {
      const before = new Set(entries.map((entry) => entry.id));
      const result = check(await twoFactorApi.add(item.input));
      if (disposed) return;
      entries = result.entries || entries;
      const saved = entries.find((entry) => !before.has(entry.id));
      // After saving, reference the system vault and release the draft secret.
      if (saved) {
        const updated = { ...item, input: undefined, savedId: saved.id };
        currentQuery = updated;
        recent = recent.map((query) => query.id === item.id ? updated : query);
        if (codes[item.id]) codes = { ...codes, [saved.id]: codes[item.id] };
      } else { currentQuery = null; recent = recent.filter((query) => query.id !== item.id); }
      clearDraft(); activeList = "saved";
      notice = "已保存到系统凭据库。可在下方显示、复制动态验证码，并导出加密备份。";
    } catch (cause) { if (!disposed) error = message(cause); }
    finally { busy = ""; }
  }

  async function saveMigration() {
    if (!migration || busy) return;
    if (migration.parts.length !== migration.batchSize) { error = "迁移二维码尚未收齐，请继续上传剩余二维码。"; return; }
    busy = "migration"; clearFeedback();
    try {
      for (const input of migrationCredentials) {
        const key = migrationKey(input);
        if (migrationSaved.has(key)) continue;
        const result = check(await twoFactorApi.add(input));
        if (disposed) return;
        entries = result.entries || entries;
        migrationSaved.add(key);
      }
      notice = `已保存 ${migrationCredentials.length} 个迁移验证器。`;
      migration = null; migrationSaved = new Set(); activeList = "saved";
    } catch (cause) { if (!disposed) error = `已保存 ${migrationSaved.size} 个，其余尚未完成：${message(cause)} 重试不会重复写入已保存条目。`; }
    finally { busy = ""; }
  }

  async function readCode(id: string) {
    const item = recent.find((query) => query.id === id);
    const result = item?.input ? check(await twoFactorApi.preview(item.input)) : check(await twoFactorApi.code(item?.savedId || id));
    if (!disposed) codes = { ...codes, [id]: result.code };
    return result.code;
  }
  async function showCode(id: string) {
    if (busy) return;
    busy = `code:${id}`; clearFeedback();
    try { await readCode(id); } catch (cause) { if (!disposed) error = message(cause); }
    finally { busy = ""; }
  }
  async function copyCode(id: string) {
    if (busy) return;
    busy = `copy:${id}`; clearFeedback();
    try {
      const code = codeRemaining(codes[id], Date.now()) > 0 ? codes[id] : await readCode(id);
      if (!navigator.clipboard?.writeText) throw new Error("剪贴板不可用，请手动复制当前验证码。");
      await navigator.clipboard.writeText(code.value);
      if (!disposed) notice = "验证码已复制到剪贴板。";
    } catch (cause) { if (!disposed) error = message(cause); }
    finally { busy = ""; }
  }
  async function updateVisibleCodes() {
    now = Date.now();
    if (disposed || busy || refreshingCodes || document.visibilityState === "hidden") return;
    const ids = new Set([...shownEntries.map((entry) => entry.id), ...(currentQuery ? [currentQuery.id] : [])]);
    const expired = [...ids].filter((id) => codes[id] && codeRemaining(codes[id], now) === 0);
    refreshingCodes = true;
    try {
      for (const id of expired) {
        if (disposed) return;
        try { await readCode(id); }
        catch { if (!disposed) { const next = { ...codes }; delete next[id]; codes = next; error = "验证码更新失败，请点击“显示验证码”重试。"; } }
      }
    } finally { refreshingCodes = false; }
  }

  async function decodeQr(file: File | null) {
    if (!file || busy) return;
    busy = "qr"; clearFeedback();
    let shouldQuery = false;
    try {
      const raw = await decodeTOTPQrImage(file);
      if (disposed) return;
      if (!raw) throw new Error("未识别到二维码，请上传清晰的原始图片。");
      if (!acceptMigration(raw)) {
        if (migration) throw new Error("请先保存或清除当前迁移批次，再查询单个验证器。");
        const parsed = parseTOTPQrPayload(raw);
        if (!parsed || parsed.kind !== "uri") throw new Error("仅支持标准 TOTP 或 Google Authenticator 迁移二维码。");
        value = parsed.uri; label = ""; shouldQuery = true;
      }
    } catch (cause) { if (!disposed) error = message(cause); }
    finally { busy = ""; }
    if (shouldQuery && !disposed) await query();
  }
  function pasteQr(event: ClipboardEvent) {
    const file = [...(event.clipboardData?.items || [])].find((item) => item.type.startsWith("image/"))?.getAsFile();
    if (file) { event.preventDefault(); void decodeQr(file); }
  }
  function dropQr(event: DragEvent) {
    const file = [...(event.dataTransfer?.files || [])].find((item) => item.type.startsWith("image/"));
    if (file) { event.preventDefault(); void decodeQr(file); }
  }

  function openModal(mode: ModalMode, entry: TOTPEntry | null = null) {
    if (busy) return;
    password = ""; confirmation = ""; dialogError = ""; deleting = entry; modal = mode;
  }
  function closeModal() {
    if (busy) return;
    modal = null; password = ""; confirmation = ""; dialogError = ""; deleting = null;
  }
  async function submitModal() {
    if (busy || !modal) return;
    dialogError = "";
    if (modal !== "delete" && password.length < 10) { dialogError = "备份密码至少需要 10 个字符。"; return; }
    if (modal === "export" && password !== confirmation) { dialogError = "两次输入的密码不一致。"; return; }
    busy = modal;
    let done = false;
    try {
      let result: TOTPResult;
      if (modal === "delete" && deleting) {
        const id = deleting.id;
        result = check(await twoFactorApi.remove(id));
        const removedQueries = recent.filter((item) => item.savedId === id);
        recent = recent.filter((item) => item.savedId !== id);
        if (currentQuery?.savedId === id) currentQuery = null;
        const next = { ...codes }; delete next[id];
        for (const query of removedQueries) delete next[query.id];
        codes = next;
      } else if (modal === "export") {
        const destination = await save({ title: "导出加密验证器备份", defaultPath: `XIASS-Tools-TOTP-${Date.now()}.json`, filters: [{ name: "XIASS 加密备份", extensions: ["json"] }] });
        if (!destination) { done = true; return; }
        result = check(await twoFactorApi.export(password, destination));
      } else {
        const source = await open({ title: "导入加密验证器备份", multiple: false, directory: false, filters: [{ name: "XIASS 加密备份", extensions: ["json"] }] });
        if (!source || Array.isArray(source)) { done = true; return; }
        result = check(await twoFactorApi.import(password, source));
      }
      if (disposed) return;
      if (result.entries) entries = result.entries;
      else if (modal !== "export") entries = check(await twoFactorApi.list()).entries || [];
      notice = modal === "delete" ? "验证器已删除；如有加密备份，可以从备份恢复。" : result.message;
      done = true;
    } catch (cause) { if (!disposed) dialogError = message(cause); }
    finally { busy = ""; if (done) closeModal(); }
  }
  function clearRecent() {
    if (busy) return;
    const next = { ...codes };
    for (const item of recent) delete next[item.id];
    recent = []; currentQuery = null; codes = next; clearDraft();
  }

  onMount(() => { void load(); clock = setInterval(() => { void updateVisibleCodes(); }, 1000); });
  onDestroy(() => {
    disposed = true; clearInterval(clock);
    recent = []; currentQuery = null; codes = {}; migration = null; migrationSaved.clear();
    clearDraft(); password = ""; confirmation = "";
  });
</script>

<svelte:window on:paste={pasteQr} on:drop={dropQr} on:dragover={(event) => { if (event.dataTransfer?.types.includes("Files")) event.preventDefault(); }} />

<div class={`${routeStackRecipe({ width: "full" })} two-factor-page`} data-two-factor>
  <section class={topStripRecipe()}>
    <div><h1><AppIcon name="shield" tone="violet" size={28} />2FA 管理</h1><p>查询、保存和复制动态验证码，在本机完成。</p></div>
    <span class="vault-badge"><AppIcon name="shield" tone="violet" size={18} />{loading ? "读取中…" : `${entries.length} 个本机验证器`}</span>
  </section>
  <div class="local-notice"><AppIcon name="info" size={20} /><p>保存的密钥由 Keychain / 系统凭据库保护，不会同步到云端。临时查询离开本页即清除；换机或删除前，请先导出加密备份。</p></div>
  {#if error}<div class="feedback error" role="alert">{error}</div>{/if}
  {#if notice}<div class="feedback notice" role="status">{notice}</div>{/if}

  <section class={`${panelRecipe()} query-panel`} aria-labelledby="totp-query-title">
    <h2 id="totp-query-title"><AppIcon name="key" tone="warning" size={20} />查询验证码</h2>
    <div class="query-grid">
      <form class="query-main" on:submit|preventDefault={query}>
        <label for="totp-value">验证器链接或 Base32 密钥</label>
        <textarea id="totp-value" bind:value rows="2" autocomplete="off" spellcheck="false" placeholder="otpauth://totp/… 或 Base32 Secret" disabled={Boolean(busy)} on:input={invalidateQuery}></textarea>
        <details class="advanced"><summary>名称与高级参数（可选）</summary><div class="advanced-fields">
          <label>识别名称<input bind:value={label} maxlength="200" disabled={Boolean(busy)} placeholder="例如：XIASS API" on:input={invalidateQuery} /></label>
          <label>算法<select bind:value={algorithm} disabled={Boolean(busy)} on:change={invalidateQuery}><option>SHA1</option><option>SHA256</option><option>SHA512</option></select></label>
          <label>位数<select bind:value={digits} disabled={Boolean(busy)} on:change={invalidateQuery}><option value={6}>6 位</option><option value={8}>8 位</option></select></label>
          <label>周期（秒）<input type="number" min="15" max="120" bind:value={period} disabled={Boolean(busy)} on:input={invalidateQuery} /></label>
        </div><p>标准链接自带的算法、位数和周期优先；高级参数用于手动密钥。</p></details>
        <div class="actions">
          <button class={actionButtonRecipe({ tone: "primary" })} type="submit" disabled={Boolean(busy) || !value.trim()}><AppIcon name={busy === "query" ? "loading" : "search"} tone="action" class={busy === "query" ? spinRecipe() : ""} />{busy === "query" ? "查询中…" : "查询"}</button>
          <button class={actionButtonRecipe()} type="button" disabled={Boolean(busy) || loading || (!currentQuery?.input && !migration)} on:click={saveCurrent}><AppIcon name="save" tone="success" />{busy === "save" || busy === "migration" ? "保存中…" : currentQuery?.savedId ? "已保存" : "保存到列表"}</button>
          <button class={actionButtonRecipe()} type="button" disabled={Boolean(busy)} on:click={() => qrInput.click()}><AppIcon name={busy === "qr" ? "loading" : "upload"} tone="cyan" class={busy === "qr" ? spinRecipe() : ""} />{busy === "qr" ? "识别中…" : "上传二维码图片"}</button>
        </div>
        <input bind:this={qrInput} class="hidden-file" type="file" accept="image/png,image/jpeg,image/webp,image/gif" aria-label="选择验证器二维码图片" on:change={(event) => { const file = event.currentTarget.files?.[0] || null; event.currentTarget.value = ""; void decodeQr(file); }} />
      </form>
      <div class="query-preview">
        {#if currentQuery}
          <strong>{currentQuery.entry.label}</strong>
          <output aria-label="查询结果验证码">{codeRemaining(previewCode, now) > 0 ? previewCode?.value : "··· ···"}</output>
          <span>{codeRemaining(previewCode, now) > 0 ? `${codeRemaining(previewCode, now)} 秒后更新` : "等待更新"}</span>
          <button class={actionButtonRecipe({ compact: true })} type="button" disabled={Boolean(busy)} on:click={() => void copyCode(currentQuery!.id)}><AppIcon name="copy" tone="cyan" size={16} />复制验证码</button>
        {:else}<AppIcon name="shield" tone="violet" size={32} /><strong>{migration ? "迁移二维码已识别" : "暂无查询数据"}</strong><span>{migration ? "收齐后可批量保存到列表" : "输入密钥后点击查询，即时显示验证码"}</span>{/if}
      </div>
    </div>
    {#if migration}<div class="migration-summary"><div><strong>Google Authenticator 迁移进度</strong><p>已识别 {migration.parts.length} / {migration.batchSize} 张，包含 {migrationCredentials.length} 个 TOTP 账号。</p></div><button class={actionButtonRecipe({ compact: true })} type="button" disabled={Boolean(busy)} on:click={() => { migration = null; migrationSaved = new Set(); }}>清除批次</button></div>{/if}
    <p class="query-hint">支持标准 TOTP 链接、Base32 密钥及 Google Authenticator 迁移二维码。也可直接粘贴或拖入二维码图片，识别过程不会上传图片。</p>
  </section>

  <section class={`${panelRecipe()} saved-panel`} aria-label="验证器列表">
    <div class="list-header">
      <div class="list-tabs" role="group" aria-label="切换验证器列表">
        <button type="button" class:active={activeList === "saved"} aria-pressed={activeList === "saved"} on:click={() => activeList = "saved"}><AppIcon name="shield" tone="violet" />已保存 <small>{entries.length}</small></button>
        <button type="button" class:active={activeList === "recent"} aria-pressed={activeList === "recent"} on:click={() => activeList = "recent"}><AppIcon name="clock" tone="warning" />近期查询 <small>{recent.length}</small></button>
      </div>
      <div class="actions">
        <button class={actionButtonRecipe({ compact: true })} type="button" disabled={Boolean(busy) || loading} on:click={load} aria-label="刷新验证器列表"><AppIcon name="refresh" size={16} />刷新</button>
        <button class={actionButtonRecipe({ compact: true })} type="button" disabled={Boolean(busy)} on:click={() => openModal("import")}><AppIcon name="download" tone="cyan" size={16} />导入</button>
        <button class={actionButtonRecipe({ compact: true })} type="button" disabled={!entries.length || Boolean(busy)} on:click={() => openModal("export")}><AppIcon name="upload" tone="warning" size={16} />导出</button>
      </div>
    </div>
    {#if activeList === "recent"}<div class="recent-hint"><span>临时查询只在本次页面内保留，最多 12 条。</span><button type="button" class={actionButtonRecipe({ compact: true })} disabled={Boolean(busy) || !recent.length} on:click={clearRecent}>清除查询</button></div>{/if}
    {#if shownEntries.length}<div class="entry-list">
      {#each shownEntries as entry (entry.id)}
        <article class="entry-row">
          <div class="entry-identity"><span class="entry-mark"><AppIcon name="shield" tone="violet" size={22} /></span><div><strong>{entry.label}</strong><span>{entry.issuer || "本地验证器"}{entry.account ? ` · ${entry.account}` : ""}</span><small>{entry.algorithm} · {entry.digits} 位 · {entry.period} 秒</small></div></div>
          <div class="entry-code"><output aria-label={`${entry.label} 的动态验证码`}>{codeRemaining(codes[entry.id], now) > 0 ? codes[entry.id].value : "··· ···"}</output><span>{codeRemaining(codes[entry.id], now) > 0 ? `${codeRemaining(codes[entry.id], now)} 秒后更新` : "点击显示验证码"}</span></div>
          <div class="actions entry-actions">
            <button class={actionButtonRecipe({ compact: true })} type="button" disabled={Boolean(busy)} on:click={() => void showCode(entry.id)}><AppIcon name="eye" size={16} />{codes[entry.id] ? "刷新" : "显示验证码"}</button>
            <button class={actionButtonRecipe({ compact: true })} type="button" disabled={Boolean(busy)} on:click={() => void copyCode(entry.id)} aria-label={`复制 ${entry.label} 的验证码`}><AppIcon name="copy" tone="cyan" size={16} />复制</button>
            {#if activeList === "saved"}<button class={actionButtonRecipe({ compact: true })} type="button" disabled={Boolean(busy)} on:click={() => openModal("delete", entry)} aria-label={`删除 ${entry.label}`}><AppIcon name="delete" tone="danger" size={16} /></button>{/if}
          </div>
        </article>
      {/each}
    </div>{:else}<div class="empty-list"><AppIcon name={activeList === "saved" ? "shield" : "clock"} tone="violet" size={30} /><strong>{loading ? "正在读取本机验证器…" : activeList === "saved" ? "还没有保存的验证器" : "还没有临时查询"}</strong><span>{activeList === "saved" ? "在上方查询后保存，或导入已有的加密备份。" : "输入链接或密钥，点击“查询”即可开始。"}</span></div>{/if}
  </section>
</div>

<dialog bind:this={dialog} class="two-factor-dialog" aria-labelledby="totp-dialog-title" on:cancel|preventDefault={closeModal}>
  <form on:submit|preventDefault={submitModal}>
    <h2 id="totp-dialog-title">{modalTitle}</h2>
    {#if modal === "delete"}<p>确认从系统凭据库删除“{deleting?.label}”？如未备份，删除后无法恢复。</p>
    {:else}<p>{modal === "export" ? "备份使用 AES-256-GCM 加密。请分别保管密码和文件；现有备份不会被覆盖。" : "输入导出时设置的密码，然后选择 XIASS Tools 生成的加密备份。"}</p>
      <label>备份密码<input type="password" bind:value={password} autocomplete="new-password" disabled={Boolean(busy)} /></label>
      {#if modal === "export"}<label>再次输入密码<input type="password" bind:value={confirmation} autocomplete="new-password" disabled={Boolean(busy)} /></label>{/if}
    {/if}
    {#if dialogError}<div class="feedback error" role="alert">{dialogError}</div>{/if}
    <div class="dialog-actions"><button class={actionButtonRecipe()} type="button" disabled={Boolean(busy)} on:click={closeModal}>取消</button><button class={actionButtonRecipe({ tone: modal === "delete" ? "secondary" : "primary" })} type="submit" disabled={Boolean(busy)}><AppIcon name={busy ? "loading" : modal === "delete" ? "delete" : modal === "export" ? "upload" : "download"} tone={modal === "delete" ? "danger" : "action"} class={busy ? spinRecipe() : ""} />{busy ? "处理中…" : modal === "delete" ? "确认删除" : modal === "export" ? "选择保存位置" : "选择文件并导入"}</button></div>
  </form>
</dialog>

<style>
  .two-factor-page { padding-bottom: 14px; }
  h1, h2 { display: flex; align-items: center; gap: 10px; }
  h1 { margin: 0 !important; }
  h2 { margin: 0; color: var(--text); font-size: 17px; line-height: 1.4; }
  .vault-badge { display: inline-flex; align-items: center; gap: 8px; padding: 10px 14px; border: 1px solid var(--border); border-radius: 99px; color: var(--text-soft); background: var(--surface-soft); font-size: 12px; font-weight: 700; white-space: nowrap; }
  .local-notice { display: flex; gap: 10px; align-items: center; padding: 14px 16px; border: 1px solid var(--border); border-radius: 16px; background: var(--surface); }
  .local-notice p { margin: 0; color: var(--text-soft); font-size: 13px; line-height: 1.65; }
  .local-notice :global(svg) { flex-shrink: 0; }
  .feedback { padding: 12px 14px; border: 1px solid var(--border); border-radius: 14px; font-size: 13px; line-height: 1.6; overflow-wrap: anywhere; }
  .feedback.error { color: var(--danger-text); background: color-mix(in srgb, var(--danger) 12%, var(--surface)); border-color: color-mix(in srgb, var(--danger) 40%, var(--border)); }
  .feedback.notice { color: var(--info-text); background: color-mix(in srgb, var(--cyan) 9%, var(--surface)); }
  .query-panel { padding: 20px; }
  .query-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(210px, .36fr); gap: 18px; margin-top: 18px; }
  .query-main { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  label { display: grid; gap: 7px; color: var(--text-soft); font-size: 13px; font-weight: 700; }
  textarea { min-height: 76px; padding: 12px; resize: vertical; font: 13px/1.5 ui-monospace, "SFMono-Regular", monospace; }
  .advanced summary { cursor: pointer; color: var(--text-soft); font-size: 12px; padding: 6px 0; }
  .advanced-fields { display: grid; grid-template-columns: 2fr 1fr 1fr 1fr; gap: 10px; margin-top: 8px; }
  .advanced p, .query-hint { color: var(--text-soft); font-size: 12px; line-height: 1.7; }
  .query-hint { margin: 16px 0 0; }
  .actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .actions :global(button) { min-height: 40px; }
  .query-preview { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; padding: 20px 14px; border: 1px solid var(--border); border-radius: 18px; background: var(--surface-soft); text-align: center; }
  .query-preview strong { color: var(--text); font-size: 14px; overflow-wrap: anywhere; }
  .query-preview output { color: var(--accent-strong); font: 800 clamp(26px, 3vw, 34px)/1.25 ui-monospace, "SFMono-Regular", monospace; letter-spacing: .08em; font-variant-numeric: tabular-nums; }
  .query-preview span { color: var(--text-soft); font-size: 12px; line-height: 1.5; }
  .hidden-file { display: none; }
  .migration-summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 14px; padding: 14px; border: 1px solid var(--border); border-radius: 14px; background: var(--surface-soft); }
  .migration-summary p { margin: 6px 0 0; color: var(--text-soft); font-size: 12px; }
  .saved-panel { overflow: hidden; }
  .list-header { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; padding: 14px 18px; border-bottom: 1px solid var(--border); }
  .list-tabs { display: flex; align-items: center; gap: 8px; }
  .list-tabs button { display: flex; align-items: center; gap: 8px; min-height: 44px; color: var(--text-soft); background: transparent; box-shadow: none; border-color: transparent; font-size: 13px; font-weight: 750; }
  .list-tabs button.active { color: var(--text); background: var(--surface-soft); border-color: var(--border-strong); }
  .list-tabs small { min-width: 20px; font-variant-numeric: tabular-nums; }
  .recent-hint { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 10px 18px; color: var(--text-soft); font-size: 12px; }
  .entry-row { display: grid; grid-template-columns: minmax(160px, 1fr) 140px auto; gap: 16px; align-items: center; padding: 16px 18px; }
  .entry-row + .entry-row { border-top: 1px solid var(--border-subtle); }
  .entry-identity { display: flex; align-items: center; gap: 12px; min-width: 0; }
  .entry-identity > div { display: grid; gap: 5px; min-width: 0; }
  .entry-identity strong { color: var(--text); font-size: 14px; overflow-wrap: anywhere; }
  .entry-identity span, .entry-identity small { color: var(--text-soft); font-size: 12px; overflow-wrap: anywhere; }
  .entry-mark { display: grid; place-items: center; flex: 0 0 40px; height: 40px; border: 1px solid var(--border); border-radius: 14px; background: var(--surface-soft); }
  .entry-code { display: grid; gap: 5px; }
  .entry-code output { color: var(--accent-strong); font: 750 22px/1.2 ui-monospace, "SFMono-Regular", monospace; letter-spacing: .08em; font-variant-numeric: tabular-nums; }
  .entry-code span { font-size: 12px; color: var(--text-soft); }
  .entry-actions { justify-content: flex-end; }
  .empty-list { display: grid; justify-items: center; gap: 12px; padding: 42px 20px; text-align: center; color: var(--text-soft); }
  .empty-list strong { font-size: 14px; color: var(--text); }
  .empty-list span { font-size: 13px; line-height: 1.6; }
  .two-factor-dialog { width: min(490px, calc(100vw - 48px)); max-height: calc(100vh - 64px); padding: 24px; border: 1px solid var(--border-strong); border-radius: 22px; background: var(--surface-raised); color: var(--text); box-shadow: var(--shadow); }
  .two-factor-dialog::backdrop { background: rgba(1, 13, 22, .6); backdrop-filter: blur(10px); }
  .two-factor-dialog form { display: grid; gap: 16px; }
  .two-factor-dialog p { margin: 0; color: var(--text-soft); line-height: 1.7; font-size: 13px; }
  .dialog-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
  @media (max-width: 1150px) { .entry-row { grid-template-columns: minmax(0, 1fr) 140px; } .entry-actions { grid-column: 1 / -1; } }
  @media (max-width: 1000px) { .query-grid { grid-template-columns: 1fr; } .advanced-fields { grid-template-columns: 1fr 1fr; } .query-preview { min-height: 150px; } }
</style>
