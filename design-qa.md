# Desktop shell and horizontal navigation design QA

- Source visual truth: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-63ef8cb0-2259-4512-82e7-ece88986e610.png`
- Sidebar-width feedback source: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-bae00037-994c-496f-a1f8-95259b259144.png`
- Brand/search alignment feedback source: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-c35a0b70-287a-4ce4-9279-6f3268b04f4d.png`
- Identity-footer feedback source: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-f0913ea3-7039-4eeb-b763-fe94625556ba.png`
- Navigation information-architecture feedback source: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-8eb5e550-7d6d-49d6-a1bd-dfd79c5c8829.png`
- Overview summary-card feedback source: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-39ec3dc8-f5bc-4249-9874-73c7d9d7456a.png`
- Hourly trend and breakdown feedback source: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-6d726919-3428-4d52-b49d-e42b1d729a7a.png`
- Connection-health summary feedback source: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-4c3897ce-750d-4ed1-8b8f-c8c54145542b.png`
- Supporting shell references: `codex-clipboard-9512e9f2-7ba2-4c88-8fb1-05061a3daf77.png`, `codex-clipboard-fa0a555c-d258-468e-b347-670aa22e1812.png`, `codex-clipboard-94988b9b-fdd2-4173-9903-1b227881cafa.png`, and `codex-clipboard-33381840-6da8-46eb-b288-c32f1df2a2ce.png` from the conversation attachments.
- Browser-rendered implementation: `artifacts/design-qa/desktop-shell-1100x720.png`
- Wide implementation: `artifacts/design-qa/desktop-shell-1280x800.png`
- Narrow implementation: `artifacts/design-qa/desktop-shell-800x720.png`
- Focused side-by-side comparison: `artifacts/design-qa/navigation-comparison.png`
- Resizable-sidebar implementation: `artifacts/design-qa/sidebar-resizable-1280x720.png`
- Sidebar-width comparison: `artifacts/design-qa/sidebar-width-comparison.png`
- Native titlebar/search implementation: `artifacts/design-qa/native-titlebar-actions-search.png`
- Native tenant-scope footer implementation: `artifacts/design-qa/tenant-scope-footer.png`
- Runtime and analytics navigation implementation: `artifacts/design-qa/navigation-runtime-analytics.png`
- Usage and framework ranking implementation: `artifacts/design-qa/overview-usage-framework-ranking.png`
- Hourly machine/framework trend implementation: `artifacts/design-qa/hourly-machine-framework-trends.png`
- Connection-health summary implementation: `artifacts/design-qa/overview-connection-health-summary.png`
- Viewports: 1100 × 720, 1280 × 800, and 800 × 720 CSS px at device scale 1.
- Pixel dimensions: source 650 × 1062; primary implementation 1100 × 720; focused comparison 919 × 720.
- Density normalization: the source navigation crop was proportionally reduced to 720px high; the implementation was cropped to its 454px-wide primary and secondary rails at native browser density.
- Compared state: light theme, Agents primary group open, Meetings secondary item selected, empty Meetings page, local-machine target.

## Full-view comparison evidence

The implementation now follows the source information architecture: a compact primary application rail, an independently bordered secondary rail, and the functional workspace to the right. The current primary group and secondary page each have a full-row selected state. Search remains directly below the brand block, while the workspace title and machine selector share a single compact top row.

The implementation intentionally preserves AgentMux's existing teal palette, supplied logo, system font stack, radii, and Lucide icon language instead of copying Lark-specific blue styling or product content. At 1100px the resulting tracks are exactly 244px / 210px / 646px with no horizontal overflow; at 1280px they are 244px / 210px / 826px.

## Focused comparison evidence

`artifacts/design-qa/navigation-comparison.png` places the normalized reference and final AgentMux rails in one image. It confirms the same horizontal parent/child relationship, header separation, row-height rhythm, icon/label alignment, current-item emphasis, search placement, and bottom account anchoring. A focused comparison was required because the reference is a tall navigation crop rather than a complete matching AgentMux workspace.

## Required fidelity surfaces

- Fonts and typography: passed. The established system font stack and AgentMux weights are preserved; primary and secondary labels remain readable without wrapping at the target widths.
- Spacing and layout rhythm: passed. The 244px/210px rail proportions, 42–44px navigation rows, 58px headers, borders, and page padding create the same clear three-column hierarchy without clipping.
- Colors and visual tokens: passed. Existing accent, muted, border, elevated-surface, hover, and selected tokens are used consistently across both rails.
- Image quality and asset fidelity: passed. The supplied AgentMux logo is reused at native quality; standard controls use the existing Lucide library, with no placeholder imagery, handcrafted SVGs, or CSS-art substitutes.
- Copy and content: passed. The current product's Chinese and English labels remain intact, with new accessible labels for the primary navigation, submenu collapse action, and meeting-page description.
- Accessibility and behavior: passed. Group buttons expose expanded state, collapse and quick-action controls have labels and focus states, locked tenancy navigation remains enforced, and the mobile rail remains keyboard-addressable.
- User-adjustable sizing: passed. Both vertical separators expose accessible range values, support pointer dragging, Left/Right and Home/End keys, Shift-modified 1px steps, double-click reset, and persisted widths with bounded restoration.

## Interaction verification

- Collapsed the secondary rail and confirmed the grid changed from `244px 210px 646px` to `244px 856px`, then reopened it from the active primary group.
- Selected a different primary group and confirmed navigation to its default child (`System → Machines`).
- Selected Overview and confirmed the secondary rail closed automatically.
- Opened the quick-settings popover and confirmed its fixed bounds stayed inside the 1100 × 720 viewport without sidebar clipping.
- Opened the Meetings page and confirmed Join Meeting exists only in the page toolbar, opens the manual join dialog, and is absent from the global workspace header.
- Verified the 800px breakpoint hides both desktop rails, exposes only the flattened bottom navigation, and has no horizontal overflow.
- Checked a fresh browser session after the final build: no console errors were reported.
- Automated checks cover deep-link group ownership, Overview collapse state, tenancy-to-System mapping, default group destinations, and integrated macOS titlebar options.
- Adjusted the primary rail from 208px to 240px and the secondary rail from 184px to its 152px minimum using keyboard controls, restored both defaults by double-click, and confirmed a 224px primary width survived reload.

## Comparison history

### Iteration 1

- Finding: P2 — at the 800px breakpoint the older `.sidebar .nav` rule overrode the new primary-rail hiding rule, leaving both desktop and mobile navigation mounted visibly.
- Fix: added a final, higher-specificity `.sidebar .primary-nav { display: none; }` rule inside the 820px breakpoint.
- Post-fix evidence: `artifacts/design-qa/desktop-shell-800x720.png` reports the primary rail as hidden, mobile navigation as flex, secondary rail as hidden, and document width equal to the 800px viewport.

### Iteration 2

- Finding: P2 — the original 244px / 210px rail defaults consumed too much horizontal space in the packaged desktop window and left no user control over the split.
- Fix: reduced defaults to 208px / 184px, tightened the brand asset and quick-action sizing, added draggable accessible separators, bounded widths to 184–320px and 152–300px, and persisted both choices locally.
- Post-fix evidence: `artifacts/design-qa/sidebar-width-comparison.png` shows the denser default rails; browser interaction checks confirm live grid updates, bounds, reset, persistence, and zero document overflow.

### Iteration 3

- Finding: P2 — at the narrow primary-rail width, placing Settings and Web UI inside the brand row truncated the app name and made the full-width search field feel crowded.
- Fix: moved both quick actions into an absolute native-titlebar action row at 14px from the top, restored the brand row to logo-plus-copy, and reduced the search field to a 34px-high inset control with smaller icon, spacing, and text.
- Post-fix evidence: `artifacts/design-qa/native-titlebar-actions-search.png` shows Settings and Web UI aligned with the macOS traffic lights, the full AgentMux name visible at the persisted 184px minimum width, and the compact search field separated from the brand block without clipping.

### Iteration 4

- Finding: P2 — the identity footer spent scarce sidebar width on a decorative avatar and implied switching through a chevron without providing a real tenant scope change.
- Fix: removed the avatar, reduced the footer to identity plus permission hint, and added an administrator-only tenant selector backed by a server-enforced tenant scope header. Disabled tenants are omitted, invalid scopes are rejected, and tenant credentials cannot change their own scope.
- Post-fix evidence: `artifacts/design-qa/tenant-scope-footer.png` shows the compact two-line footer with no avatar; the accessibility tree exposes the administrator/tenant selector and instance-level permission hint, while server tests verify tenant isolation and non-elevation.

### Iteration 5

- Finding: P2 — the previous `连接与集成` and `运维治理` buckets mixed model configuration, external event entry points, meetings, and historical operating data, making their intent hard to predict.
- Fix: renamed the groups to `连接与自动化` and `运行与分析`; moved Meetings beside Channels & Triggers, moved Session History beside Observability and Usage, and moved LLM Providers/Routing plus Provider Health into Agents. Search vocabulary retains the previous terms as aliases.
- Post-fix evidence: `artifacts/design-qa/navigation-runtime-analytics.png` shows the Runtime & Analytics rail opening on Session History; native accessibility checks also confirm Connections & Automation contains Channels & Triggers plus Meetings, while Agents contains LLM Providers & Routing and Provider Health.

### Iteration 6

- Finding: P2 — the Overview summary split tokens, cost, requests, sessions, and framework usage into too many peer cards, obscuring the two actual questions: total usage and which frameworks dominate it.
- Fix: consolidated the summary into one Usage card with Token, distinct Session, and estimated amount metrics plus a persistent RMB/USD toggle; added a second card ranking the top three runtimes by Token usage with framework icons, totals, and share.
- Post-fix evidence: `artifacts/design-qa/overview-usage-framework-ranking.png` shows the two-card layout with live local usage, Codex ranked at 100%, and the currency control restored to RMB after exercising both currency options.

### Iteration 7

- Finding: P2 — the Token trend used a sparse time series and could not explain which framework or machine contributed to a change.
- Fix: added true hourly aggregation, normalized the selected range to 24/168/720 hourly slots, and added Total, Framework, and Machine views. Framework and machine views render up to five independent series plus an `Other` roll-up, with legends showing each series' Token total and share.
- Compatibility finding: P2 — older remote nodes ignore the hourly period and return daily bucket keys, which initially caused their machine lines to appear at zero.
- Compatibility fix: hourly-capable nodes retain exact hourly data; legacy daily buckets are distributed evenly across the day's 24 display slots until those nodes are upgraded, keeping totals and shares accurate without a false zero line.
- Post-fix evidence: `artifacts/design-qa/hourly-machine-framework-trends.png` shows a 24-point Today chart with independent `ecs_cn` and `aliyun-swas-sg` lines and 73% / 27% legends; native accessibility verification also confirms the Framework view exposes Codex CLI at 100%.

### Iteration 8

- Finding: P2 — the Overview health section listed provider configuration rows rather than actual service availability, omitted bot-channel connectivity, and left its last-updated timestamp at the bottom of a long page.
- Fix: replaced the provider table with a compact two-row Connection Health summary. Enabled channels are classified from their live connection state and error field; model services use the Provider monitor snapshot, distinguish unhealthy from unchecked services, and refresh every 30 seconds. Only rows needing attention expose a Resolve action, linking directly to Channels & Triggers or Provider Health. The last-updated timestamp now sits beside the range control at the top of Statistics.
- Post-fix evidence: `artifacts/design-qa/overview-connection-health-summary.png` shows one disconnected bot channel and two unchecked model services with separate counts and actions. Native interaction verification confirms the channel action opens `#connect`, the model action opens the Provider tab at `#gateway`, and the app returns to the Overview health section afterward.

## Remaining P3 follow-up

- The secondary label density remains intentionally unchanged; it can be tightened separately if the user later wants a more compact navigation rhythm.

final result: passed

---

# Framework catalogue simplification design QA

- Source visual truth: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-813dd48b-a4e7-45cf-8014-8aa618327c12.png`
- Focused row-density source: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-bb297517-e87c-4622-b972-19bd3c877f1a.png`
- Browser-rendered implementation: `artifacts/design-qa/frameworks-installed-only.png`
- Install-dialog implementation: `artifacts/design-qa/frameworks-install-dialog.png`
- Narrow implementation: `artifacts/design-qa/frameworks-installed-narrow.png`
- Source pixel dimensions: 2200 × 1440 full view and 1594 × 1260 focused view.
- Implementation pixel dimensions: 2200 × 1246 desktop content viewport and 800 × 1094 narrow viewport, device scale 1.
- Density normalization: source and desktop implementation were compared at the same 2200px width. The source is a complete desktop-app window while the in-app browser reserves 194px of vertical browser chrome, so the comparison used the shared application shell and catalogue region rather than the source-only bottom continuation.
- Compared state: light theme, Agents group open, Frameworks selected, local-machine target, four installed frameworks, stable update checks.

## Findings

- No actionable P0, P1, or P2 differences remain. The implementation intentionally removes the source subtitle and prerequisite card, replaces the full 40-item catalogue with installed-only rows, and replaces the five dense columns with Framework, Version/update, and Actions.
- The disabled Uninstall state for Cursor Agent and TRAE CLI is intentional: their catalogue entries do not expose a verified non-destructive automatic removal command. The button remains visible and explains the limitation; npm-managed frameworks and OpenCode have executable uninstall paths.
- No raster imagery was required by this screen. The supplied AgentMux logo remains unchanged and all control icons come from the product's existing Lucide dependency.

## Full-view comparison evidence

The reference and final implementation were opened together in one comparison input. The AgentMux shell, teal accent system, surface borders, typography, navigation proportions, and catalogue placement are preserved. The red-boxed source content is absent, so the catalogue becomes the first surface beneath the workspace header. The new Install framework action is aligned in the catalogue header and the installed count reflects the visible rows.

## Focused comparison evidence

The focused source and final implementation were also opened together. The source's kind identifier, machine field, description, environment variables, installed/routable state, login-status badge, and stacked status pills are gone from local rows. Each row now exposes company and framework type as compact tags, one version/update block, and only Check updates/Update, Uninstall, and Login actions. A focused comparison was required because the small labels and button grouping are not reliably judged from the full-shell view.

## Required fidelity surfaces

- Fonts and typography: passed. Existing system fonts, weights, line heights, monospace version styling, truncation, and hierarchy are preserved; row names remain visually primary and tags remain legible.
- Spacing and layout rhythm: passed. Desktop rows are compact and evenly divided; the header action aligns with the count/title; action groups use one horizontal rhythm. At 800px rows stack into labelled sections without overlap or horizontal page overflow.
- Colors and visual tokens: passed. Existing accent, surface, border, green success, amber update, red danger, muted, hover, focus, and disabled tokens are reused.
- Image quality and asset fidelity: passed. The existing AgentMux logo is reused and standard UI controls use the installed icon library; no placeholder imagery, emoji, handcrafted SVG, or CSS-art substitute was introduced.
- Copy and content: passed. Chinese and English strings cover installed-only empty state, install selection, company/type metadata, version/update state, uninstall confirmation and limitations, and operation success states.
- Accessibility and behavior: passed. The install selector is a labelled modal with close controls, every action is a semantic button, disabled uninstall states include explanatory titles, update/loading states are announced through existing progress UI, and the narrow layout has no horizontal overflow.

## Interaction verification

- Confirmed the local catalogue renders exactly four installed frameworks and excludes Gemini CLI, OpenCode, and Qoder from the main table.
- Opened Install framework and confirmed those three uninstalled, supported frameworks appear with company/type tags and individual Install buttons; closed the dialog through its labelled close control.
- Confirmed every installed row exposes Check updates or Update, Uninstall, and Login, with unsupported automatic uninstalls visibly disabled.
- Confirmed update checks settle into three Up to date states and one Codex update-available state.
- Verified the 800 × 1094 responsive state has `scrollWidth === innerWidth === 800`, keeps all row actions reachable, and preserves the mobile navigation shell.
- Checked the browser console after the final interaction state: no errors were reported.
- Automated checks passed for the frontend production build and the framework/server Go packages, including safe npm uninstall command ownership and rejection of unsupported automatic uninstall.
- No real framework was installed or uninstalled during visual QA; destructive-path behavior was verified through confirmation UI and isolated unit tests.

## Comparison history

- Initial full and focused comparisons found no actionable P0/P1/P2 mismatch against the requested simplification, so no post-capture repair iteration was required.

## Follow-up polish

- No remaining P3 issue is required for handoff.

final result: passed

---

# Agent registry simplification design QA

- Source visual truth: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-681ee96a-8009-4e78-9c9b-87f22482b0bd.png`
- Removal reference: `/var/folders/9k/dn93qqs10jd9y5y25jlf0nkr0000gn/T/codex-clipboard-470d4950-63ef-447d-93e3-1ddee744708e.png`
- Browser-rendered implementation: `artifacts/design-qa/2026-08-30-agent-registry/implementation-handoff-1100x720.png`
- Wide implementation: `artifacts/design-qa/2026-08-30-agent-registry/implementation-desktop-2200x1440.png`
- Narrow implementation: `artifacts/design-qa/2026-08-30-agent-registry/implementation-narrow-800x900.png`
- Full-view side-by-side comparison: `artifacts/design-qa/2026-08-30-agent-registry/comparison-full-final.png`
- Focused registry comparison: `artifacts/design-qa/2026-08-30-agent-registry/comparison-focus-final.png`
- Viewports: 1100 × 720 primary desktop CSS px, 2200 × 1440 wide CSS px, and 800 × 900 narrow CSS px, all at device scale 1.
- Pixel dimensions: source 2200 × 1440; normalized source 1100 × 720; primary implementation 1100 × 720; full comparison 2200 × 760; focused comparison 1399 × 690.
- Density normalization: the source capture is a Retina-density 2200 × 1440 image and was downsampled to 1100 × 720 before comparison with the 1× browser render.
- Compared state: light theme, Agents group open, all-machine target, five Agent rows, Provider/channel aggregation fully loaded.

## Findings

- No actionable P0, P1, or P2 differences remain. The implementation intentionally replaces the source subtitle, registration-count badge, source/status/channel-count pills, and bottom metric card with the user-requested compact summary, switches, merged model labels, local brand marks, and row actions.
- Residual test gap: no live `config.toml` row was present in the browser dataset, so its disabled switch/delete state is covered by the shared `isConfigManaged` logic and automated helper tests rather than a live screenshot.
- Residual destructive-path gap: the delete confirmation/cancellation path was exercised without deleting a real Agent; successful deletion continues through the existing tested API endpoint.

## Full-view comparison evidence

`comparison-full-final.png` places the normalized source and final browser render in one image. The same AgentMux shell, card surface, row dividers, icon language, typography, and teal token system are preserved. The bottom Agents metric surface is absent, leaving the registry as the sole page surface. The title, four-part summary, Refresh/New controls, and all row actions remain visible without horizontal overflow.

## Focused comparison evidence

`comparison-focus-final.png` makes the registry details legible side by side. It confirms that the long explanatory copy and registration count were removed; status is now a switch beside each name; ordinary source badges are gone; route and model information is one pill; bound Feishu channels use a real local brand mark; and Edit/Delete remain grouped at row end. A focused comparison was required because these small controls and logos are not reliably judged from the full-shell image.

## Required fidelity surfaces

- Fonts and typography: passed. Existing system fonts, weights, line heights, truncation, and hierarchy are preserved; the new summary stays on one desktop line and remains readable when it wraps at narrow widths.
- Spacing and layout rhythm: passed. Header actions share one row, row metadata and actions have consistent gaps, switches align optically with names, and the 800px layout stacks without clipping or overlap.
- Colors and visual tokens: passed. Existing accent, muted, surface, border, danger, enabled, focus, and disabled tokens are reused; the delete action is visually distinct without overpowering Edit.
- Image quality and asset fidelity: passed. Feishu/Lark, WeChat, DingTalk, Telegram, Slack, and Discord marks are embedded local PNG assets sourced from official/open icon collections, remain sharp at 18px, and avoid remote runtime dependencies or placeholder art.
- Copy and content: passed. Chinese and English strings cover the four metrics, short New action, account/custom-route model summaries, empty channel state, switch actions, notices, and delete confirmation.
- Accessibility and behavior: passed. Status controls expose `role=switch`, checked state, action names, focus styling, busy state, and read-only disabling; channel groups expose bound channel names; buttons remain keyboard-addressable.

## Interaction verification

- Toggled the unbound `test` Agent off and back on; browser state changed `true → false → true` and displayed the matching success notices.
- Opened `test` through the row Edit action and closed its existing detail drawer.
- Opened New from the compact header action and closed the `新建 Agent` drawer.
- Exercised the row Delete confirmation/cancellation path and confirmed the Agent remained present.
- Verified all-machine aggregation after asynchronous fleet loading: 5 Agents, 1 effective Provider, 3 machines, and 4 bound channels.
- Verified the 800 × 900 breakpoint uses the mobile shell, keeps Refresh/New together, stacks row metadata/actions cleanly, and has no visible overflow.
- Checked browser console warnings/errors after the final loaded state: none.

## Comparison history

- Initial normalized comparison found no P0/P1/P2 mismatch against the requested redesign, so no post-capture visual repair iteration was required.

## Follow-up polish

- No remaining P3 visual issue is necessary for handoff.

final result: passed
