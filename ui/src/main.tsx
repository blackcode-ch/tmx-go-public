import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import "./lib/theme";
import App from "./App";
import { createTransport } from "./lib/transport";
import { start } from "./lib/store";
import { applyTextSize, savedTextSize } from "./lib/text-size";

// The text size saved on this device, before the first render.
applyTextSize(savedTextSize(), false);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

void createTransport().then(start);
