import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import "./styles.css";
import { registerServiceWorker, syncThemeColor } from "./pwa/register";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);

syncThemeColor();
void registerServiceWorker();
