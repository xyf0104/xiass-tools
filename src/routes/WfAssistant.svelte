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
  let frameHeight = 680;
  type WfSection = "dashboard" | "models" | "accounts" | "permissions";
  let activeSection: WfSection = "dashboard";
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
    height: "auto",
    overflow: "visible",
    padding: 0,
    background: "color-mix(in srgb, var(--surface) 96%, #22d3ee 4%)"
  });
  const frameClass = css({
    display: "block",
    width: "100%",
    border: 0,
    background: "transparent",
    overflow: "hidden"
  });
  const workflowClass = css({
    display: "grid",
    gap: "12px",
    padding: "18px 20px",
    color: "var(--text-soft)",
    "& strong": { color: "var(--text)", fontSize: "13px", fontWeight: 800 },
    "& p": { margin: 0, color: "var(--text-muted)", fontSize: "12px", lineHeight: 1.55 }
  });
  const stepsClass = css({
    display: "grid",
    gridTemplateColumns: "repeat(5, minmax(0, 1fr))",
    gap: "8px",
    "@media (max-width: 900px)": { gridTemplateColumns: "repeat(2, minmax(0, 1fr))" },
    "@media (max-width: 540px)": { gridTemplateColumns: "1fr" }
  });
  const stepClass = css({
    display: "grid",
    gridTemplateColumns: "28px minmax(0, 1fr)",
    alignItems: "center",
    gap: "9px",
    minHeight: "58px",
    padding: "9px 10px",
    border: "1px solid color-mix(in srgb, var(--border) 86%, transparent)",
    borderRadius: "14px",
    background: "color-mix(in srgb, var(--surface-raised) 76%, transparent)",
    "& span": { color: "var(--text)", opacity: 0.9, fontSize: "11px", fontWeight: 700, lineHeight: 1.35 },
    "& b": {
      display: "grid", placeItems: "center", width: "28px", height: "28px", borderRadius: "10px",
      color: "#fff", fontSize: "12px", fontWeight: 900,
      background: "linear-gradient(135deg, #ff8a3d, #ffb347 38%, #2878ff 78%, #1552c6)",
      boxShadow: "0 8px 20px color-mix(in srgb, #2878ff 22%, transparent)"
    }
  });
  const sectionNavClass = css({
    display: "grid",
    gridTemplateColumns: "minmax(0, .8fr) minmax(0, 1.1fr) minmax(0, 1.6fr) minmax(0, .8fr)",
    gap: "8px",
    alignItems: "center",
    padding: "10px",
    "@media (max-width: 1000px)": { gridTemplateColumns: "repeat(2, minmax(0, 1fr))" },
    "& button": {
      display: "flex",
      minHeight: "46px",
      padding: "10px 12px",
      lineHeight: 1.3,
      alignItems: "center",
      justifyContent: "center",
      gap: "8px"
    },
    "& button svg": { display: "block", flexShrink: 0, alignSelf: "center" },
    "& button[data-active='true']": {
      borderColor: "color-mix(in srgb, #ff8a3d 68%, #2878ff 32%)",
      background: "linear-gradient(135deg, rgba(255,138,61,.28), rgba(40,120,255,.28))",
      color: "#ffffff",
      boxShadow: "0 10px 24px rgba(40,120,255,.18)"
    },
    "& button[data-active='true'] svg": {
      filter: "brightness(0) invert(1) drop-shadow(0 1px 3px rgba(0,0,0,.28))"
    }
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
  const loadingBadgeClass = css({
    position: "absolute",
    top: "14px",
    right: "14px",
    zIndex: 3,
    display: "inline-flex",
    alignItems: "center",
    gap: "8px",
    padding: "8px 11px",
    border: "1px solid color-mix(in srgb, #2878ff 28%, var(--border))",
    borderRadius: "999px",
    background: "color-mix(in srgb, var(--surface-raised) 82%, transparent)",
    color: "var(--text-soft)",
    fontSize: "12px",
    fontWeight: 800,
    pointerEvents: "none",
    boxShadow: "0 10px 24px rgba(40, 120, 255, 0.14)",
    backdropFilter: "blur(16px) saturate(145%)"
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

  // Keep the iframe URL reactive to every visible navigation input. Svelte's
  // legacy reactive dependency analysis cannot see values that are only read
  // inside buildFrameUrl(), so passing the section and theme explicitly avoids
  // stale pages and a perpetual loading state after clicking a WF section.
  $: frameUrl = session ? buildFrameUrl(session, activeSection, theme) : "";

  function resolveTheme() {
    theme = document.documentElement.dataset.theme === "light" ? "light" : "dark";
  }

  function buildFrameUrl(value: WfBridgeSession, section: WfSection, currentTheme: "light" | "dark") {
    const url = new URL(value.url);
    url.searchParams.set("embedded", "1");
    url.searchParams.set("module", "antigravity");
    url.searchParams.set("section", section);
    url.searchParams.set("theme", currentTheme);
    return url.toString();
  }

  function selectSection(section: WfSection) {
    if (section === activeSection) return;
    activeSection = section;
    frameReady = false;
    frameHeight = 680;
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

  function handleFrameLoad() {
    // The embedded WF page can finish rendering even when one optional
    // bootstrap request fails. Treat the iframe load as the visual readiness
    // boundary so the complete feature surface remains usable instead of
    // leaving a blocking "connecting" card over the page forever. The child
    // runtime still sends its authenticated ready event when bootstrap
    // finishes, and the token is delivered here as soon as the document is
    // ready as well.
    frameReady = true;
    deliverToken();
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
    if (event.data?.type === "xiass-wf-content-height") {
      const nextHeight = Number(event.data.height);
      if (Number.isFinite(nextHeight)) {
        frameHeight = Math.max(620, Math.min(32000, Math.ceil(nextHeight)));
      }
      return;
    }
    if (event.data?.type === "xiass-wf-scroll") {
      const workspace = document.querySelector<HTMLElement>(".cs-app-workspace");
      const deltaX = Number(event.data.deltaX) || 0;
      const deltaY = Number(event.data.deltaY) || 0;
      if (workspace && (deltaX || deltaY)) {
        workspace.scrollBy({ left: deltaX, top: deltaY, behavior: "auto" });
      }
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

  <nav class={cx(panelRecipe(), sectionNavClass)} aria-label={$t("wf.sections") }>
    <button class={actionButtonRecipe({ tone: activeSection === "dashboard" ? "primary" : "secondary" })} type="button" data-active={activeSection === "dashboard"} on:click={() => selectSection("dashboard")}>
      <AppIcon name="dashboard" tone="cyan" size={17} />{$t("wf.sectionDashboard")}
    </button>
    <button class={actionButtonRecipe({ tone: activeSection === "models" ? "primary" : "secondary" })} type="button" data-active={activeSection === "models"} on:click={() => selectSection("models")}>
      <AppIcon name="stats" tone="violet" size={17} />{$t("wf.sectionModels")}
    </button>
    <button class={actionButtonRecipe({ tone: activeSection === "accounts" ? "primary" : "secondary" })} type="button" data-active={activeSection === "accounts"} on:click={() => selectSection("accounts")}>
      <AppIcon name="user" tone="cyan" size={17} />{$t("wf.sectionAccounts")}
    </button>
    <button class={actionButtonRecipe({ tone: activeSection === "permissions" ? "primary" : "secondary" })} type="button" data-active={activeSection === "permissions"} on:click={() => selectSection("permissions")}>
      <AppIcon name="key" tone="warning" size={17} />{$t("wf.sectionPermissions")}
    </button>
  </nav>

  <section class={cx(panelRecipe(), workflowClass)} aria-label={$t("wf.workflowTitle")}>
    <div>
      <strong>{$t("wf.workflowTitle")}</strong>
      <p>{$t("wf.workflowHint")}</p>
    </div>
    <div class={stepsClass}>
      <div class={stepClass}><b>1</b><span>{$t("wf.stepAccount")}</span></div>
      <div class={stepClass}><b>2</b><span>{$t("wf.stepModels")}</span></div>
      <div class={stepClass}><b>3</b><span>{$t("wf.stepCapabilities")}</span></div>
      <div class={stepClass}><b>4</b><span>{$t("wf.stepConnect")}</span></div>
      <div class={stepClass}><b>5</b><span>{$t("wf.stepLaunch")}</span></div>
    </div>
  </section>

  <section class={cx(panelRecipe(), shellClass)}>
    {#if frameUrl}
      {#key frameUrl}
        <iframe
          bind:this={frame}
          class={frameClass}
          style={`height: ${frameHeight}px`}
          src={frameUrl}
          title={$t("wf.frameTitle")}
          scrolling="no"
          on:load={handleFrameLoad}
          allow="clipboard-read; clipboard-write"
        ></iframe>
      {/key}
    {/if}

    {#if error || !session}
      <div class={stateClass}>
        <div class={stateCardClass}>
          <AppIcon name={error ? "error" : "rocket"} size={28} />
          <strong>{error ? $t("wf.startFailed") : loading ? $t("wf.connecting") : $t("wf.stopped")}</strong>
          <p>{error ?? (loading ? $t("wf.connectingHint") : status?.lastError ?? $t("wf.stoppedHint"))}</p>
          {#if error || !session}
            <button class={actionButtonRecipe({ tone: "primary" })} type="button" disabled={loading} on:click={start}>
              <AppIcon name="power" size={16} />
              {$t("wf.start")}
            </button>
          {/if}
        </div>
      </div>
    {:else if loading || !frameReady}
      <div class={loadingBadgeClass} role="status" aria-live="polite">
        <AppIcon name="loading" class={spinRecipe()} size={15} />
        {$t("wf.connecting")}
      </div>
    {/if}
  </section>
</div>
