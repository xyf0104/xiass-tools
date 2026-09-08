<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import AppIcon from "../components/AppIcon.svelte";
  import { t } from "../lib/i18n";
  import {
    getWfBridgeSession,
    getWfBridgeStatus,
    handleWfBridgeHostAction,
    stopWfBridge,
    type WfBridgeHostActionRequest,
    type WfBridgeSession,
    type WfBridgeStatus
  } from "../lib/wfBridge";
  import { css, cx } from "../../styled-system/css";
  import {
    actionButtonRecipe,
    panelRecipe,
    routeStackRecipe,
    spinRecipe,
    topActionsRecipe,
    topStripRecipe
  } from "../../styled-system/recipes";

  let frame: HTMLIFrameElement | null = null;
  let session: WfBridgeSession | null = null;
  let status: WfBridgeStatus | null = null;
  let loading = true;
  let frameReady = false;
  let error: string | null = null;
  let theme: "light" | "dark" = "dark";
  let themeObserver: MutationObserver | null = null;

  const heroClass = css({
    position: "relative",
    overflow: "hidden",
    _after: {
      content: '""',
      position: "absolute",
      insetInline: "20px",
      bottom: 0,
      height: "2px",
      borderRadius: "999px",
      background: "linear-gradient(90deg, transparent, #22d3ee 18%, #86efac 78%, transparent)",
      opacity: 0.78
    }
  });
  const shellClass = css({
    position: "relative",
    minHeight: "620px",
    height: "calc(100dvh - 132px)",
    overflow: "hidden",
    padding: 0,
    background: "color-mix(in srgb, var(--surface) 96%, #22d3ee 4%)"
  });
  const frameClass = css({
    display: "block",
    width: "100%",
    height: "100%",
    border: 0,
    background: "transparent"
  });
  const stateClass = css({
    position: "absolute",
    inset: 0,
    zIndex: 2,
    display: "grid",
    placeItems: "center",
    padding: "28px",
    background: "color-mix(in srgb, var(--surface) 92%, transparent)",
    backdropFilter: "blur(12px)"
  });
  const stateCardClass = css({
    display: "grid",
    justifyItems: "center",
    gap: "12px",
    width: "min(460px, 100%)",
    padding: "28px",
    border: "1px solid var(--border)",
    borderRadius: "18px",
    background: "var(--surface-raised)",
    boxShadow: "0 22px 60px rgba(2, 8, 23, 0.18)",
    textAlign: "center",
    color: "var(--text-soft)",
    "& strong": {
      color: "var(--text)",
      fontSize: "18px"
    },
    "& p": {
      margin: 0,
      lineHeight: 1.6,
      overflowWrap: "anywhere"
    }
  });
  const statusClass = css({
    display: "inline-flex",
    alignItems: "center",
    gap: "8px",
    padding: "7px 11px",
    border: "1px solid color-mix(in srgb, #22d3ee 32%, var(--border))",
    borderRadius: "999px",
    background: "color-mix(in srgb, #22d3ee 8%, var(--surface))",
    color: "var(--text-soft)",
    fontSize: "12px",
    fontWeight: 800
  });
  const statusDotClass = css({
    width: "7px",
    height: "7px",
    borderRadius: "999px",
    background: "#86efac",
    boxShadow: "0 0 12px rgba(134, 239, 172, 0.72)"
  });

  $: frameUrl = session ? buildFrameUrl(session) : "";

  function resolveTheme() {
    theme = document.documentElement.dataset.theme === "light" ? "light" : "dark";
  }

  function buildFrameUrl(value: WfBridgeSession) {
    const url = new URL(value.url);
    url.searchParams.set("embedded", "1");
    url.searchParams.set("module", "antigravity");
    url.searchParams.set("section", "dashboard");
    url.searchParams.set("theme", theme);
    return url.toString();
  }

  function expectedOrigin() {
    return session ? new URL(session.url).origin : null;
  }

  function deliverToken() {
    const origin = expectedOrigin();
    if (!session || !origin || !frame?.contentWindow) return;
    frame.contentWindow.postMessage(
      { type: "xiass-wf-auth", token: session.token },
      origin
    );
  }

  function validHostAction(value: unknown): value is WfBridgeHostActionRequest {
    if (!value || typeof value !== "object") return false;
    const candidate = value as Partial<WfBridgeHostActionRequest>;
    return Boolean(
      candidate.requestId &&
      /^[0-9a-f]{64}$/i.test(candidate.requestId) &&
      candidate.kind &&
      [
        "open_url",
        "open_file",
        "open_directory",
        "save_file",
        "claude_code_account_candidates",
        "claude_code_apply_account"
      ].includes(candidate.kind)
    );
  }

  function handleEmbeddedMessage(event: MessageEvent) {
    const origin = expectedOrigin();
    if (!origin || event.origin !== origin || event.source !== frame?.contentWindow) return;
    if (event.data?.type === "xiass-wf-ready") {
      frameReady = true;
      deliverToken();
      return;
    }
    if (event.data?.type !== "xiass-wf-host-action" || !validHostAction(event.data.request)) {
      return;
    }
    if (!session) return;
    void handleWfBridgeHostAction(session.port, event.data.request).catch(() => undefined);
  }

  async function start() {
    loading = true;
    frameReady = false;
    error = null;
    try {
      const nextSession = await getWfBridgeSession();
      if (nextSession.host !== "127.0.0.1" || new URL(nextSession.url).hostname !== "127.0.0.1") {
        throw new Error($t("wf.invalidAddress"));
      }
      session = nextSession;
      status = await getWfBridgeStatus();
    } catch (err) {
      session = null;
      status = await getWfBridgeStatus().catch(() => null);
      const message = err instanceof Error ? err.message : String(err);
      error = message.includes("reading 'invoke'") || message.includes("__TAURI")
        ? $t("wf.desktopOnly")
        : message;
    } finally {
      loading = false;
    }
  }

  async function restart() {
    loading = true;
    await stopWfBridge().catch(() => undefined);
    session = null;
    await start();
  }

  async function stop() {
    loading = true;
    error = null;
    try {
      await stopWfBridge();
      session = null;
      frameReady = false;
      status = await getWfBridgeStatus();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    resolveTheme();
    themeObserver = new MutationObserver(resolveTheme);
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"]
    });
    window.addEventListener("message", handleEmbeddedMessage);
    void start();
  });

  onDestroy(() => {
    themeObserver?.disconnect();
    window.removeEventListener("message", handleEmbeddedMessage);
  });
</script>

<div class={routeStackRecipe({ width: "full" })} data-xiass-wf-assistant>
  <section class={cx(topStripRecipe(), heroClass)}>
    <div>
      <h1>{$t("wf.title")}</h1>
      <p>{$t("wf.subtitle")}</p>
    </div>
    <div class={topActionsRecipe()}>
      {#if status?.running}
        <span class={statusClass}>
          <span class={statusDotClass}></span>
          {$t("wf.running")}
        </span>
      {/if}
      <button class={actionButtonRecipe()} type="button" disabled={loading} on:click={restart}>
        <AppIcon name={loading ? "loading" : "restart"} class={loading ? spinRecipe() : ""} size={16} />
        {$t("wf.restart")}
      </button>
      <button class={actionButtonRecipe()} type="button" disabled={loading || !status?.running} on:click={stop}>
        <AppIcon name="stop" size={16} />
        {$t("wf.stop")}
      </button>
    </div>
  </section>

  <section class={cx(panelRecipe(), shellClass)}>
    {#if frameUrl}
      <iframe
        bind:this={frame}
        class={frameClass}
        src={frameUrl}
        title={$t("wf.frameTitle")}
        on:load={deliverToken}
        allow="clipboard-read; clipboard-write"
      ></iframe>
    {/if}

    {#if loading || (session && !frameReady) || error || !session}
      <div class={stateClass}>
        <div class={stateCardClass}>
          <AppIcon name={error ? "error" : loading || session ? "loading" : "rocket"} class={loading || (session && !frameReady) ? spinRecipe() : ""} size={28} />
          <strong>{error ? $t("wf.startFailed") : session ? $t("wf.connecting") : $t("wf.stopped")}</strong>
          <p>{error ?? status?.lastError ?? (session ? $t("wf.connectingHint") : $t("wf.stoppedHint"))}</p>
          {#if error || !session}
            <button class={actionButtonRecipe({ tone: "primary" })} type="button" disabled={loading} on:click={start}>
              <AppIcon name="power" size={16} />
              {$t("wf.start")}
            </button>
          {/if}
        </div>
      </div>
    {/if}
  </section>
</div>
