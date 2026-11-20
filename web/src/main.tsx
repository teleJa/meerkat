import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { HashRouter } from "react-router-dom";
import "./index.css";
import App from "./App";

// The loopback UI does not import Wails' JavaScript runtime. Signal readiness
// so WebView2 executes the native scripts queued for this window.
const windowsBridge = (window as Window & { chrome?: { webview?: { postMessage(message: string): void } } }).chrome?.webview;
if (window.top === window && window.location.pathname === "/") {
  windowsBridge?.postMessage("wails:runtime:ready");
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <HashRouter>
      <App />
    </HashRouter>
  </StrictMode>
);
