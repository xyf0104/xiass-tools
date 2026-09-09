<script lang="ts">
  import { isTauri } from "@tauri-apps/api/core";
  import { getCurrentWindow } from "@tauri-apps/api/window";

  let doubleClickOrigin: { x: number; y: number } | null = null;

  function canDrag() {
    return document.documentElement.dataset.platform === "macos" && isTauri();
  }

  function reportWindowError(error: unknown) {
    console.warn("XIASS window operation failed:", error);
  }

  function handleMouseDown(event: MouseEvent) {
    doubleClickOrigin = null;
    if (!canDrag() || event.button !== 0) return;

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

    void getCurrentWindow().toggleMaximize().catch(reportWindowError);
  }
</script>

<!-- No native-control replicas and no data-tauri-drag-region: this region
     invokes the public window API once, without the injected drag listener
     handling the same press a second time. Native traffic lights stay above it. -->
<div
  class="xiass-window-drag-region"
  role="presentation"
  aria-hidden="true"
  on:mousedown={handleMouseDown}
  on:mousemove={handleMouseMove}
  on:mouseleave={() => { doubleClickOrigin = null; }}
  on:mouseup={handleMouseUp}
></div>
