<script lang="ts">
  import { onMount } from "svelte";
  import { isTauri } from "@tauri-apps/api/core";
  import { getCurrentWindow } from "@tauri-apps/api/window";
  import AppIcon from "./AppIcon.svelte";

  let doubleClickOrigin: { x: number; y: number } | null = null;
  let maximized = false;

  function currentPlatform() {
    return document.documentElement.dataset.platform;
  }

  function canDrag() {
    return (currentPlatform() === "macos" || currentPlatform() === "windows") && isTauri();
  }

  function isWindows() {
    return currentPlatform() === "windows" && isTauri();
  }

  function reportWindowError(error: unknown) {
    console.warn("XIASS window operation failed:", error);
  }

  function handleMouseDown(event: MouseEvent) {
    doubleClickOrigin = null;
    if (!canDrag() || event.button !== 0) return;
    if (event.target instanceof Element && event.target.closest("button")) return;

    // Match macOS title-bar behaviour: only maximize after releasing the
    // second click without moving. A normal press starts native dragging.
    if (event.detail === 2) {
      doubleClickOrigin = { x: event.clientX, y: event.clientY };
      return;
    }
    if (event.detail > 2) return;

    event.preventDefault();
    void getCurrentWindow().startDragging().catch(reportWindowError);
  }

  function handleMouseMove(event: MouseEvent) {
    if (doubleClickOrigin && (event.clientX !== doubleClickOrigin.x || event.clientY !== doubleClickOrigin.y)) {
      doubleClickOrigin = null;
    }
  }

  function handleMouseUp(event: MouseEvent) {
    const origin = doubleClickOrigin;
    doubleClickOrigin = null;
    if (!canDrag() || event.button !== 0 || event.detail !== 2 || !origin) return;
    if (event.clientX !== origin.x || event.clientY !== origin.y) return;

    void toggleMaximize();
  }

  async function toggleMaximize() {
    try {
      const window = getCurrentWindow();
      await window.toggleMaximize();
      maximized = await window.isMaximized();
    } catch (error) {
      reportWindowError(error);
    }
  }

  async function minimize() {
    try {
      await getCurrentWindow().minimize();
    } catch (error) {
      reportWindowError(error);
    }
  }

  async function close() {
    try {
      // Rust converts CloseRequested into a tray hide, preserving the
      // single-instance restore path for the next app-icon click.
      await getCurrentWindow().close();
    } catch (error) {
      reportWindowError(error);
    }
  }

  onMount(() => {
    if (!isWindows()) return;
    const window = getCurrentWindow();
    void window.setDecorations(false).catch(reportWindowError);
    void window.isMaximized().then((value) => { maximized = value; }).catch(reportWindowError);
  });
</script>

<!-- No native-control replicas and no data-tauri-drag-region: this region
     invokes the public window API once, without the injected drag listener
     handling the same press a second time. Native traffic lights stay above it. -->
<div
  class="xiass-window-drag-region"
  role="presentation"
  aria-hidden={isWindows() ? undefined : "true"}
  on:mousedown={handleMouseDown}
  on:mousemove={handleMouseMove}
  on:mouseleave={() => { doubleClickOrigin = null; }}
  on:mouseup={handleMouseUp}
>
  {#if isWindows()}
    <div class="xiass-window-controls" role="group" aria-label="Window controls">
      <button type="button" class="xiass-window-control" aria-label="Minimize" title="Minimize" on:mousedown={(event) => event.stopPropagation()} on:click={minimize}>
        <AppIcon name="windowMinimize" size={14} />
      </button>
      <button type="button" class="xiass-window-control" aria-label={maximized ? "Restore" : "Maximize"} title={maximized ? "Restore" : "Maximize"} on:mousedown={(event) => event.stopPropagation()} on:click={toggleMaximize}>
        <AppIcon name={maximized ? "windowRestore" : "windowMaximize"} size={14} />
      </button>
      <button type="button" class="xiass-window-control xiass-window-control--close" aria-label="Close" title="Close" on:mousedown={(event) => event.stopPropagation()} on:click={close}>
        <AppIcon name="close" size={14} />
      </button>
    </div>
  {/if}
</div>
