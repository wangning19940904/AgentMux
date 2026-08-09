# Design QA — paginated catalog tables

## Evidence

- Visual QA captures were generated locally for comparison and intentionally excluded from the repository.
- Source pixels: 1648 × 3324. The attachment does not expose a CSS viewport or device-density value.
- Desktop pixels and CSS viewport: 1440 × 1000 at device scale 1.
- Responsive pixels and CSS viewport: 760 × 1000 at device scale 1.
- Comparison normalization: the relevant source catalog region was cropped to 1648 × 1720; source and implementation were proportionally normalized to 720 px wide and placed side by side. No density-only differences were treated as findings.
- State: Chinese, light theme, `aliyun-ecs`, framework catalog page 1 with Cursor Agent installed. The CLI target check used local data at `#skills/cli/agent-browser`.

## Full-view comparison evidence

The source shows the framework catalog as a two-column card grid. The implementation intentionally changes that requested surface to a five-column table while retaining the same page shell, section hierarchy, typography, icons, semantic colors, descriptions, requirements, status badges, and actions. Ten rows fit in roughly the space previously used by four cards, materially reducing vertical scanning without compressing action targets.

## Focused region comparison evidence

The combined comparison focuses on the prerequisites and framework catalog. Names and identifiers remain grouped, environment requirements have their own column, status/version information remains readable, and install/update controls are right-aligned consistently. No additional focused crop was needed because the table text and state controls are legible in the 1440 px implementation capture.

## Findings

- No actionable P0, P1, or P2 findings remain.
- Fonts and typography: the existing Inter/system stack, weights, sizes, monospace identifiers, truncation behavior, and hierarchy are preserved.
- Spacing and layout rhythm: row padding, column alignment, section borders, radii, and pagination spacing follow the existing surface system. Desktop has no horizontal page overflow.
- Colors and visual tokens: existing background, border, accent, success, warning, and muted tokens are reused. Installed and deep-linked rows retain clear semantic emphasis.
- Image quality and asset fidelity: the existing AgentMux logo and Lucide UI icons are preserved; no visible product asset was replaced or approximated.
- Copy and content: framework, CLI, and Skill descriptions, identifiers, requirements, versions, status text, and actions are preserved. New pagination copy is localized in English and Chinese.
- Responsive behavior: at 760 px, tables become labeled compact rows, actions remain visible, and the document has no horizontal overflow.

## Primary interactions tested

- Framework pagination: page 1 shows 10 of 11 items; page 2 shows only DeepAgents; previous/next disabled states are correct.
- Skills search: querying `pdf` reduces the table to one row and clearing it restores 10 rows on page 1.
- CLI deep link: `#skills/cli/agent-browser` finds the correct row, scrolls it into view, and applies the install-target highlight.
- CLI, Skills marketplace, and installed Skills each render their own count and pagination footer.
- Browser console warnings and errors checked: none.

## Comparison history

- Pass 1: no product P0/P1/P2 mismatch was found. A distorted full-page browser capture was discarded as a capture artifact; the implementation was recaptured at the same verified 1440 × 1000 desktop viewport. No UI fix was required in response.

## Follow-up polish

- P3: consider making page size user-selectable only if catalogs routinely grow beyond roughly 50 items; the current fixed size of 10 keeps the interface simpler.

final result: passed

---

# Design QA — channel default prompt preview

## Evidence

- Visual QA captures were generated locally for comparison and intentionally excluded from the repository.
- Source pixels: 1384 × 1352. It was normalized exactly to 692 × 676 (half density) for comparison.
- Implementation pixels and CSS viewport: 692 × 676 at device scale 1.
- State: Chinese, light theme, Agent edit drawer, Edit and Complete Preview modes, one disabled Feishu test channel bound to the Agent.

## Full-view comparison evidence

The implementation preserves the source drawer, editable system-prompt area, dashed injected-prompt preview, fixed footer actions, typography, borders, and pale teal token system. The requested content is added inside the existing injected-prompt preview: a localized section heading, the bound channel name, and the exact static Feishu/Lark message prefix returned by the backend.

## Focused region comparison evidence

The full-view side-by-side image keeps the system-prompt editor and injected-prompt preview legible at the same normalized viewport, so a separate crop was not needed. The browser DOM snapshot additionally confirmed all six channel-execution bullets render as a semantic list inside the existing Markdown preview.

## Findings

- No actionable P0, P1, or P2 findings remain.
- Fonts and typography: the new heading, channel source label, paragraphs, list, inline code, weights, and wrapping reuse the existing Markdown typography without introducing a new font or hierarchy.
- Spacing and layout rhythm: the preview keeps its dashed container and padding. The Edit-state live preview has a 360 px maximum; the dedicated Complete Preview uses a 320–560 px range so the composed prompt is readable while remaining internally scrollable and leaving the persistent Save/Delete footer visible.
- Colors and visual tokens: existing background, border, muted text, accent, and button tokens are unchanged.
- Image quality and asset fidelity: no new image asset is required. Existing AgentMux and Lucide assets remain unchanged and sharp.
- Copy and content: English and Chinese help text now names per-message channel defaults. The displayed contract is sourced from the same backend function used by runtime message injection, preventing preview drift.
- Accessibility and behavior: the content remains semantic Markdown. The channel section appears only when a selected channel exposes a non-empty default prompt, and identical defaults from multiple channels are grouped instead of duplicated.

## Primary interactions tested

- Opening the Agent editor with a bound Feishu channel rendered one `渠道消息默认注入（每条入站消息）` section.
- Deselecting the channel changed the section count from 1 to 0; selecting it again restored the count to 1.
- Clicking Preview changed the field title to `注入后的完整提示词`, rendered exactly one preview block, and showed the base prompt, channel-default section, and bound-channel log-path section once each.
- The Edit-state secondary `将注入的提示词` block is hidden in Preview mode, so the composed prompt is not duplicated. Returning to Edit restores the textarea and live injected preview.
- The browser console was checked after loading and interaction; no errors were reported.
- Backend API verification confirmed Feishu channel responses include a non-empty `default_message_prompt`; non-Feishu channels return no default.

## Comparison history

- Pass 1: no P0/P1/P2 mismatch was found in the requested surface. The added content follows the source preview container and existing product tokens, so no visual rework was required.

## Implementation checklist

- [x] Reuse the runtime prompt as the backend source of truth.
- [x] Expose the default prompt on channel list responses.
- [x] Group and display defaults for currently selected channels.
- [x] Keep non-Feishu channels and unbound Agents unchanged.
- [x] Verify binding toggles, prompt mode switching, console state, and responsive drawer containment.

## Follow-up polish

- No remaining P3 item is required for handoff.

final result: passed

---

# Design QA — session run status and stop control

## Evidence

- Visual QA captures for the reference, same-state, running, stopped, responsive, and comparison views were generated locally and intentionally excluded from the repository.
- Source and desktop implementation pixels: 1678 × 1246. Browser-reported CSS viewport: 1678 × 1402 at device scale 1; the in-app browser capture surface is 1678 × 1246.
- Responsive CSS viewport and pixels: 820 × 1000 at device scale 1.
- Same-state comparison: Chinese, light theme, local Codex CLI session selected, one tool card expanded. Feature-state comparison: AgentMux-managed channel conversation selected, `running` status visible, and stop control available.

## Full-view comparison evidence

The same-state comparison preserves the verified session layout, split-pane proportions, selected-row treatment, transcript density, filter alignment, and tool-card behavior. Compact run-status badges are added to list rows and beside the detail title without changing the existing information hierarchy or reducing transcript space.

## Focused region comparison evidence

The focused comparison isolates the session list and detail header before and after the feature. A running conversation uses the existing green semantic token and displays a single destructive stop action at the top-right of the detail header. The stop control remains visually separate from resume, copy, terminal, and delete actions, and it disappears after interruption succeeds.

## Findings

- No actionable P0, P1, or P2 findings remain.
- Fonts and typography: existing Inter/system typography, weights, truncation, timestamps, and compact badge labels are preserved. Status copy remains readable at the list's existing density.
- Spacing and layout rhythm: badges use the established 20 px compact-pill height. The detail title, status badge, and stop button align without changing the header's vertical rhythm. Desktop and 820 px layouts have no document-level horizontal overflow.
- Colors and visual tokens: running/completed use the existing success token; queued/waiting/stopping/stopped use warning; failed uses danger; idle/offline remain neutral. The stop button reuses the established danger-action treatment.
- Image quality and asset fidelity: no new raster imagery is required. Existing AgentMux and Lucide assets remain sharp; the stop glyph comes from the project's current icon library.
- Copy and content: status labels and stop confirmation/success text are localized in English and Chinese. The labels distinguish running, queued, waiting for input, stopping, completed, failed, cancelled, stopped, offline, and idle.
- Interaction and accessibility: the stop action is a real button with a confirmation step, disabled feedback while stopping, and a stale-task ID guard so an old action cannot stop a newer task.

## Primary interactions tested

- Session rows refresh their runtime state every 10 seconds and on manual refresh.
- A running managed conversation displayed `进行中` in the list and detail header and exposed exactly one stop button.
- Clicking stop opened a confirmation dialog. Accepting it called the local stop endpoint, changed the state to `已停止`, removed the stop button, and showed `已请求停止，会话正在结束。`.
- Backend tests verify both ordinary managed turns and durable Codex task turns use the same stop surface; only work owned by this AgentMux process is stoppable.
- Local/native history rows display `空闲` and never expose a stop button, avoiding unsafe process-level termination.
- At 820 × 1000, the running badge and stop button remain visible and document `scrollWidth` equals `clientWidth` (820 px).
- Browser console warnings and errors checked: none.

## Comparison history

- Iteration 1 — P2: the running status appeared in both the detail title and the facts row, creating redundant visual noise. Fix: kept status beside the title and removed the duplicate facts-row badge.
- Iteration 2 — pass: same-state full-view comparison found no remaining layout regression; the focused running/stopped states confirmed the destructive action's visibility and successful removal.

## Implementation checklist

- [x] Show status on every session row and the selected session header.
- [x] Refresh runtime state without clearing the current selection.
- [x] Show stop only for a live AgentMux-managed turn.
- [x] Interrupt ordinary turns and durable Codex tasks through the real runtime controller.
- [x] Confirm before stopping and reject stale task actions.
- [x] Verify desktop, responsive, running, and stopped states.

## Follow-up polish

- No remaining P3 item is required for handoff.

final result: passed

---

# Design QA — session transcript and tool-call rendering

## Evidence

- Visual QA captures for the source, normalized source, desktop, responsive, and comparison views were generated locally and intentionally excluded from the repository.
- Source pixels: 1678 × 1388. The attachment does not expose a CSS viewport or device-density value.
- The source was cropped to 1678 × 1246 so the source and implementation could be compared at identical pixel dimensions.
- Implementation pixels: 1678 × 1246. Browser-reported CSS viewport: 1678 × 1544 at device scale 1; the in-app browser capture surface is 1678 × 1246.
- Responsive implementation: 820 × 1000 CSS pixels and image pixels at device scale 1.
- State: Chinese, light theme, local Codex CLI session selected, 155 tool calls in the transcript, one `exec` card expanded for input/output inspection and the remaining tool cards collapsed.

## Full-view comparison evidence

The reference establishes the session page's pale teal accent, white surfaces, restrained borders, compact filters, selected-row treatment, and dense operational typography. The implementation preserves those visual tokens inside the existing AgentMux desktop shell. The desktop split view is an intentional product extension of the reference's list-first layout: clicking a row keeps the session list visible and renders the conversation beside it, which directly satisfies the requested browse-and-inspect workflow without introducing a separate route.

## Focused region comparison evidence

The focused comparison makes the toolbar, selected session row, list density, transcript hierarchy, tool status, and expanded input/output blocks legible together. The implementation retains the reference's compact spacing and semantic teal while adding a clear chat/tool hierarchy. The expanded card is contained within the transcript column and does not cover the following card or the persistent session actions.

## Findings

- No actionable P0, P1, or P2 findings remain.
- Fonts and typography: the existing Inter/system sans stack, optical weights, sizes, line heights, monospace payload rendering, truncation, and hierarchy remain consistent with the source and the surrounding AgentMux UI.
- Spacing and layout rhythm: desktop list/detail tracks, toolbar alignment, row padding, card gaps, radii, borders, and transcript spacing are coherent. The responsive layout stacks the detail below the list and has no document-level horizontal overflow.
- Colors and visual tokens: existing background, border, muted text, teal accent, success, error, and focus tokens are reused. Tool success/error/pending states remain semantically distinguishable.
- Image quality and asset fidelity: no raster product imagery is required by this screen. The existing AgentMux logo and Lucide UI icons are sharp and consistently sized; no source asset was replaced with a placeholder or handcrafted approximation.
- Copy and content: user/Agent labels, tool input/output labels, status text, timestamps, copy actions, and empty/loading/error text are localized in English and Chinese. Message content is rendered as Markdown while raw tool payloads preserve whitespace in code blocks.
- Interaction and accessibility: tool headers are real buttons with `aria-expanded`; tool input/output is absent from the DOM while collapsed, visible when expanded, and copy controls remain reachable.

## Primary interactions tested

- Clicking a session row loads its transcript in the detail pane.
- A freshly loaded transcript reported 155 tool cards, zero open cards, zero tool bodies, and `aria-expanded="false"` on the first tool header.
- Opening an `exec` card exposed both tool input and tool output, changed `aria-expanded` to `true`, and retained the other cards' collapsed state.
- Post-fix geometry: the expanded card measured 574 px high, its body 515 px high, and the next item started 12 px after the card's bottom, confirming no overlap.
- At 820 × 1000, both document and body `scrollWidth` equaled `clientWidth` (820 px); no horizontal page overflow was present.
- Browser console warnings and errors checked on a fresh tab: none.

## Comparison history

- Iteration 1 — P1: collapsed tool cards inherited a two-pixel grid row and were visually unreadable. Fix: made each tool card its own grid container with a 56 px minimum summary height and visible overflow. Post-fix evidence: collapsed cards measure 58 px total with a 56 px summary.
- Iteration 2 — P1: an expanded tool body grew outside its 56 px parent and overlapped following cards. Fix: allowed the tool card grid item to use `height: max-content`; measured expanded bottom 1019 px and next-card top 1031 px.

## Implementation checklist

- [x] User and Agent messages render in a chat-style transcript.
- [x] Tool name, status, timestamp, input, and output render as dedicated cards.
- [x] Tool cards default to collapsed and expand independently.
- [x] Claude, Codex JSONL, and current Codex app-server transcript formats are normalized by the backend.
- [x] Desktop and responsive browser states were visually verified.

## Follow-up polish

- No remaining P3 item is required for handoff.

final result: passed
