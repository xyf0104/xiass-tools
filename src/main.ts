import "./panda.css";
import "./styles.css";
import App from "./App.svelte";
import { mount } from "svelte";

// Apply the platform marker before the first render. Waiting for an environment
// scan would make Windows briefly take the Linux/unknown layout and, more
// importantly, would make the window and desktop-client flows disagree about
// which native platform is running. Keep this deliberately browser-safe: the
// Vite preview should still render without Tauri being present.
const navigatorPlatform = navigator.platform || navigator.userAgent;
document.documentElement.dataset.platform = /Mac/i.test(navigatorPlatform)
  ? "macos"
  : /Win/i.test(navigatorPlatform)
    ? "windows"
    : "other";

const app = mount(App, {
  target: document.getElementById("app")!
});

export default app;
