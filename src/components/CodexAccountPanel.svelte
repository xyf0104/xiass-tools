<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { t, type TranslationKey } from "../lib/i18n";
  import { codexAccountErrorKey, discardCodexAccountSession, importCodexAccountJson, importLocalCodexAccount, pollCodexAccountSession,
    startCodexAccountLogin, type CodexAccountSession } from "../lib/codexAccounts";
  import AppIcon from "./AppIcon.svelte";
  import { actionButtonRecipe, wizardButtonRowRecipe, wizardCodexAuthCardRecipe,
    wizardInlineNoticeRecipe, spinRecipe } from "../../styled-system/recipes";
  import { css } from "../../styled-system/css";

  export let mode: "oauth" | "json" = "oauth";
  export let session: CodexAccountSession | null = null;
  export let accountIndex = 0;
  export let busy = false;
  export let disabled = false;
  export let replacing = false;

  let input: HTMLInputElement;
  let jsonText = "";
  let reveal = false;
  let loading = false;
  let error: string | null = null;
  let generation = 0;
  let destroyed = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  $: waiting = session?.status === "waiting";
  $: busy = loading || waiting;
  $: account = session?.status === "ready" ? session.accounts[accountIndex] : null;

  const panelClass = css({ display: "grid", gap: "12px", minWidth: 0 });
  const fieldClass = css({ display: "grid", gap: "8px", minWidth: 0, "& textarea": { minHeight: "100px", maxHeight: "220px", resize: "vertical", fontFamily: "monospace", fontSize: "12px" } });
  const hintClass = css({ fontSize: "12px", color: "var(--text-muted)", lineHeight: 1.6, overflowWrap: "anywhere" });
  function message(code: unknown) {
    return $t(codexAccountErrorKey(code));
  }
  function discard(id?: string) {
    if (id) void discardCodexAccountSession(id).catch(() => {});
  }
  function clear() {
    generation += 1;
    clearTimeout(timer);
    discard(session?.id);
    session = null;
    accountIndex = 0;
    loading = false;
    busy = false;
    error = null;
  }
  async function run(operation: () => Promise<CodexAccountSession>) {
    if (disabled || loading || session?.status === "waiting") return;
    clear();
    const request = generation;
    loading = true;
    try {
      const next = await operation();
      if (destroyed || request !== generation) { discard(next.id); return; }
      session = next;
      jsonText = "";
      reveal = false;
      if (next.status === "waiting") schedulePoll(next.id, request);
    } catch (err) {
      if (!destroyed && request === generation) error = message(err);
    } finally {
      if (!destroyed && request === generation) loading = false;
    }
  }
  function schedulePoll(id: string, request: number) {
    clearTimeout(timer);
    timer = setTimeout(() => { void poll(id, request); }, 1500);
  }
  async function poll(id: string, request = generation) {
    try {
      const next = await pollCodexAccountSession(id);
      if (destroyed || request !== generation) { discard(next.id); return; }
      session = next;
      if (next.status === "waiting") schedulePoll(id, request);
      if (next.status === "failed") error = message(next.errorCode);
    } catch (err) {
      if (!destroyed && request === generation) { clear(); error = message(err); }
    }
  }
  async function importFile(event: Event) {
    const fileInput = event.currentTarget as HTMLInputElement;
    const file = fileInput.files?.[0];
    fileInput.value = "";
    if (!file || disabled || loading || session?.status === "waiting") return;
    if (file.size > 1024 * 1024) { clear(); error = $t("codexAccount.tooLarge"); return; }
    await run(async () => importCodexAccountJson(await file.text()));
  }
  onMount(() => {
    if (session?.status === "waiting") schedulePoll(session.id, generation);
  });
  onDestroy(() => {
    destroyed = true;
    generation += 1;
    clearTimeout(timer);
    // Ready selections belong to the parent form so Next/Back keeps them.
    // Pending login and in-flight responses are never left running on close.
    if (session?.status === "waiting") {
      discard(session.id);
      session = null;
    }
    loading = false;
    busy = false;
    jsonText = "";
  });
</script>

<section class={panelClass} aria-label={$t("codexAccount.title")} aria-busy={busy}>
  <div class={wizardCodexAuthCardRecipe()}>
    <div>
      <strong>{$t(mode === "json" ? "codexAccount.importTitle" : "codexAccount.oauthTitle")}</strong>
      <span>{$t(replacing ? "codexAccount.replaceHint" : "codexAccount.stagedHint")}</span>
    </div>
    <div class={wizardButtonRowRecipe()}>
      {#if mode === "oauth"}
        <button class={actionButtonRecipe({ tone: "primary" })} type="button" disabled={disabled || busy} on:click={() => run(startCodexAccountLogin)}>
          <AppIcon name="externalLink" size={17} />{$t("codexAccount.login")}
        </button>
        <button class={actionButtonRecipe()} type="button" disabled={disabled || busy} on:click={() => run(importLocalCodexAccount)}>
          <AppIcon name="user" size={16} />{$t("codexAccount.useLocal")}
        </button>
      {:else}
        <button class={actionButtonRecipe({ tone: "primary" })} type="button" disabled={disabled || busy} on:click={() => input.click()}>
          <AppIcon name="upload" size={17} />{$t("codexAccount.chooseFile")}
        </button>
        <input bind:this={input} type="file" accept=".json,application/json" hidden on:change={importFile} />
      {/if}
    </div>
  </div>
  {#if mode === "json" && !account}
    <label class={fieldClass}>
      {$t("codexAccount.jsonLabel")}
      <textarea bind:value={jsonText} rows="4" disabled={disabled || busy} autocomplete="off" autocapitalize="off" spellcheck={false}
        style={`-webkit-text-security: ${reveal ? "none" : "disc"}`} placeholder={$t("codexAccount.jsonPlaceholder")}></textarea>
    </label>
    <div class={wizardButtonRowRecipe()}>
      <button class={actionButtonRecipe({ tone: "primary" })} type="button" disabled={disabled || busy || !jsonText.trim()} on:click={() => run(() => importCodexAccountJson(jsonText))}>
        <AppIcon name="check" size={17} />{$t("codexAccount.validate")}
      </button>
      <button class={actionButtonRecipe()} type="button" disabled={disabled || busy} aria-pressed={reveal} on:click={() => reveal = !reveal}>
        <AppIcon name={reveal ? "eyeOff" : "eye"} size={16} />{$t(reveal ? "codexAccount.hide" : "codexAccount.show")}
      </button>
    </div>
  {/if}
  {#if waiting || loading}
    <div class={wizardButtonRowRecipe()} role="status">
      <AppIcon name="loading" size={16} class={spinRecipe()} />
      <span>{$t(waiting ? "codexAccount.waiting" : "common.loading")}</span>
      <button class={actionButtonRecipe()} type="button" disabled={disabled} on:click={clear}>{$t("common.cancel")}</button>
    </div>
  {/if}
  {#if error}<div class={wizardInlineNoticeRecipe({ tone: "error" })} role="alert">{error}</div>{/if}
  {#if session?.status === "ready"}
    {#if session.accounts.length > 1}
      <label class={fieldClass}>
        {$t("codexAccount.chooseAccount")}
        <select bind:value={accountIndex} disabled={disabled || busy}>
          {#each session.accounts as item, index}<option value={index}>{index + 1}. {item.label}{item.accountId ? ` · ${item.accountId}` : ""}</option>{/each}
        </select>
      </label>
    {/if}
    {#if account}
      <div class={wizardInlineNoticeRecipe({ tone: "success" })} role="status">
        <AppIcon name="check" size={16} /> {account.label} · {$t(session.source === "oauth" ? "codexAccount.oauthReady" : "codexAccount.importReady")}
      </div>
      {#each account.warnings as warning}<small class={hintClass}>{$t(warning as TranslationKey)}</small>{/each}
      <button class={actionButtonRecipe()} type="button" disabled={disabled || busy} on:click={clear}>{$t("codexAccount.clear")}</button>
    {/if}
  {/if}
  <small class={hintClass}>{$t("codexAccount.privacy")}</small>
</section>
