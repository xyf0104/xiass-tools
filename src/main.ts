import "./panda.css";
import "./styles.css";
import App from "./App.svelte";
import { mount } from "svelte";

// Apply the macOS traffic-light safe area before the first render. Waiting for
// an environment scan would briefly place the brand under native controls.
document.documentElement.dataset.platform = /Mac/i.test(navigator.platform) ? "macos" : "other";

const app = mount(App, {
  target: document.getElementById("app")!
});

export default app;
